//go:build unix

package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm/contracts/state"
	"github.com/akopichin/afm/pkg/mcpserver"
	"github.com/akopichin/afm/pkg/runregistry"
)

// fakeChild is an afm process as the host sees it: /api/runtime, and the real stage-1 MCP service on /mcp.
type fakeChild struct {
	t        *testing.T
	srv      *httptest.Server
	instance string
	runsDir  string

	mu     sync.Mutex
	svc    *mcpserver.Service
	runID  string
	prev   []string
	status state.RunStatus
	// dropTool names a tool whose call is applied and then answered with a broken connection, once.
	dropTool string
	counts   map[string]int
	pauses   atomic.Int32
	gates    map[string]*callGate
}

// callGate holds the next request of one JSON-RPC method inside the run until released; with fail the request is
// then answered by a broken connection instead, with refuse by an error response of the run.
type callGate struct {
	entered, release chan struct{}
	fail, refuse     bool
}

// gate holds the next call of method; the returned gate reports when it arrived.
func (c *fakeChild) gate(method string, fail bool) *callGate {
	g := &callGate{entered: make(chan struct{}), release: make(chan struct{}), fail: fail}
	c.mu.Lock()
	if c.gates == nil {
		c.gates = map[string]*callGate{}
	}
	c.gates[method] = g
	c.mu.Unlock()
	return g
}

func newFakeChild(t *testing.T, instance, runID string) *fakeChild {
	t.Helper()
	c := &fakeChild{t: t, instance: instance, runID: runID, status: state.RunStatusRunning, runsDir: t.TempDir(), counts: map[string]int{}}
	c.svc = c.newService(runID)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/runtime", func(w http.ResponseWriter, _ *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		_ = json.NewEncoder(w).Encode(Runtime{ProtocolVersion: 1, Mode: "core", RunID: c.runID, Ready: true, InstanceID: c.instance})
	})
	mux.HandleFunc("/mcp", c.serveMCP)
	c.srv = httptest.NewServer(mux)
	t.Cleanup(func() {
		// The host keeps streams open to the run; end them first or Close waits for them.
		c.mu.Lock()
		svc := c.svc
		c.mu.Unlock()
		_ = svc.Close(context.Background())
		c.srv.CloseClientConnections()
		c.srv.Close()
	})
	return c
}

type childReader struct{ c *fakeChild }

func (r childReader) Snapshot() state.RunState {
	r.c.mu.Lock()
	defer r.c.mu.Unlock()
	return state.RunState{
		RunStatus: r.c.status, StageOrder: []string{"build"},
		Stages: map[string]state.StageState{"build": {Status: state.StatusRunning}},
	}
}
func (childReader) History() ([]state.Transition, error) { return nil, nil }

type childCommands struct{ c *fakeChild }

func (childCommands) Revise(context.Context, string, string) error { return nil }
func (childCommands) CheckRevise(string) error                     { return nil }
func (childCommands) Retry(context.Context, string) error          { return nil }
func (cc childCommands) Pause(context.Context, string) error       { cc.c.pauses.Add(1); return nil }
func (childCommands) Continue(context.Context, string) error       { return nil }
func (childCommands) Answer(string, string, string, string, bool) error {
	return nil
}

func (c *fakeChild) newService(runID string) *mcpserver.Service {
	svc, err := mcpserver.New(mcpserver.Config{
		RunID: runID, Reader: childReader{c}, Commands: childCommands{c}, Groups: mcpserver.AllGroups(),
	})
	if err != nil {
		c.t.Fatal(err)
	}
	return svc
}

// nextCycle moves the process on to another run: the old service closes its sessions, as a repeat cycle does.
func (c *fakeChild) nextCycle(runID string) {
	c.mu.Lock()
	old := c.svc
	c.prev = append(c.prev, c.runID)
	c.runID = runID
	c.svc = c.newService(runID)
	c.mu.Unlock()
	_ = old.Close(context.Background())
}

func (c *fakeChild) setStatus(s state.RunStatus) { c.mu.Lock(); c.status = s; c.mu.Unlock() }

func (c *fakeChild) count(key string) int { c.mu.Lock(); defer c.mu.Unlock(); return c.counts[key] }

// rpcCall is the part of a JSON-RPC request the fake run counts.
type rpcCall struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params rpcCallName     `json:"params"`
}

type rpcCallName struct {
	Name string `json:"name"`
}

func (c *fakeChild) serveMCP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var rpc rpcCall
	_ = json.Unmarshal(body, &rpc)
	c.mu.Lock()
	c.counts[rpc.Method]++
	if rpc.Method == "tools/call" {
		c.counts["tool:"+rpc.Params.Name]++
	}
	drop := rpc.Method == "tools/call" && rpc.Params.Name == c.dropTool
	if drop {
		c.dropTool = ""
	}
	g := c.gates[rpc.Method]
	delete(c.gates, rpc.Method)
	svc := c.svc
	c.mu.Unlock()
	if g != nil {
		close(g.entered)
		select {
		case <-g.release:
		case <-r.Context().Done():
			return
		}
		if g.fail {
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
			}
			return
		}
		if g.refuse {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"refused by the run"}}`, rpc.ID)
			return
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if drop {
		svc.ServeHTTP(httptest.NewRecorder(), r) // applied, but the answer never arrives
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	}
	svc.ServeHTTP(w, r)
}

// publish writes the child's registry record; launchID makes it the process of a host launch.
func (c *fakeChild) publish(regDir, launchID string) runregistry.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	marker, _ := runregistry.ProcessStart(os.Getpid())
	rec := runregistry.Record{
		InstanceID: c.instance, LaunchID: launchID, PID: os.Getpid(), ProcessStart: marker, Mode: "core",
		APIURL: c.srv.URL, RunID: c.runID, PreviousRunIDs: slices.Clone(c.prev), RunsDir: c.runsDir, Ready: true,
		Project: runregistry.Project{Root: "/work/app", RunName: "demo"}, StartedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := runregistry.Publish(regDir, rec); err != nil {
		c.t.Fatal(err)
	}
	return rec
}

type hostFixture struct {
	t        *testing.T
	host     *Host
	regDir   string
	inv      *Inventory
	sessions *Sessions
	gw       *Gateway
	launcher *Launcher
	ctl      *Controller
	srv      *httptest.Server
	client   *sdkmcp.ClientSession
	updates  chan string
}

func newHostFixture(t *testing.T, groups HostGroups) *hostFixture {
	t.Helper()
	host, _ := acquire(t)
	f := &hostFixture{t: t, host: host, regDir: t.TempDir(), updates: make(chan string, 64)}
	disc := &Discovery{RegistryDir: f.regDir, Now: time.Now}
	f.inv = &Inventory{Host: host, Discovery: disc, Now: time.Now}
	f.sessions = &Sessions{}
	f.launcher = NewLauncher(LauncherConfig{Host: host, Config: Config{Limits: Limits{MaxRunning: 4, MaxStarting: 2}}, Discovery: disc})
	f.ctl = &Controller{Runs: f.inv, Status: f.sessions, Grace: 200 * time.Millisecond}
	f.gw = NewGateway(GatewayConfig{Inventory: f.inv, Launcher: f.launcher, Controller: f.ctl, Sessions: f.sessions, Groups: groups, Version: "test"})
	f.srv = httptest.NewServer(f.gw.Handler())
	f.client = f.connect()
	t.Cleanup(func() {
		_ = f.client.Close()
		f.gw.Close()
		f.srv.Close()
		f.launcher.Close()
	})
	return f
}

func (f *hostFixture) connect() *sdkmcp.ClientSession { return f.connectNotifying(f.updates) }

// connectNotifying opens a host client whose resource updates arrive on updates.
func (f *hostFixture) connectNotifying(updates chan string) *sdkmcp.ClientSession {
	f.t.Helper()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "1"}, &sdkmcp.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, req *sdkmcp.ResourceUpdatedNotificationRequest) { updates <- req.Params.URI },
	})
	cs, err := client.Connect(context.Background(), &sdkmcp.StreamableClientTransport{Endpoint: f.srv.URL}, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	return cs
}

// managed makes the child a launch of this host: it answers under the instance id the host gives that launch.
func (f *hostFixture) managed(c *fakeChild, requestID string) {
	f.t.Helper()
	if _, _, err := f.host.CreateReceipt(requestID, baseParams, LaunchIdentity{}, time.Now()); err != nil {
		f.t.Fatal(err)
	}
	c.mu.Lock()
	c.instance = InstanceIDFor(LaunchID(requestID))
	c.mu.Unlock()
	c.publish(f.regDir, LaunchID(requestID))
}

func (f *hostFixture) call(name string, args map[string]any) (map[string]any, bool) {
	f.t.Helper()
	return callOn(f.t, f.client, name, args)
}

func callOn(t *testing.T, cs *sdkmcp.ClientSession, name string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out map[string]any
	raw, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(raw, &out)
	return out, res.IsError
}

func (f *hostFixture) toolNames() []string {
	f.t.Helper()
	var names []string
	for tool, err := range f.client.Tools(context.Background(), nil) {
		if err != nil {
			f.t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

func TestGatewayOffersToolsByGroup(t *testing.T) {
	hostTools := []string{toolRunsList, toolReport}
	runTools := []string{runtimeTool, toolRunStart, toolAttach, toolCancel, toolRelease}
	readTools := []string{"afm_artifact_read", "afm_events_read", "afm_run_status", "afm_run_wait"}
	tests := []struct {
		name   string
		groups string
		want   []string
	}{
		{"all", "reply,stages,approve,budget,runs", slices.Concat(hostTools, runTools, readTools,
			[]string{"afm_budget_adjust", "afm_budget_resume", "afm_interaction_reply", "afm_stage_continue", "afm_stage_decide", "afm_stage_pause"})},
		{"read only", "read", slices.Concat(hostTools, readTools)},
		{"runs only", "runs", slices.Concat(hostTools, runTools, readTools)},
		{"reply", "reply", slices.Concat(hostTools, readTools, []string{"afm_interaction_reply"})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groups, err := ParseHostGroups(tt.groups)
			if err != nil {
				t.Fatal(err)
			}
			got := newHostFixture(t, groups).toolNames()
			want := slices.Clone(tt.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("tools = %v\nwant    %v", got, want)
			}
		})
	}
}

func TestParseHostGroups(t *testing.T) {
	all, err := ParseHostGroups("reply,stages,approve,budget,runs")
	if err != nil || !all.Runs || len(all.Child.Names()) != 4 {
		t.Fatalf("all = %+v, %v", all, err)
	}
	if only, err := ParseHostGroups("runs"); err != nil || !only.Runs || len(only.Child.Names()) != 0 {
		t.Errorf("runs = %+v, %v", only, err)
	}
	if read, err := ParseHostGroups("read"); err != nil || read.Runs || len(read.Child.Names()) != 0 {
		t.Errorf("read = %+v, %v", read, err)
	}
	for _, bad := range []string{"read,runs", "root", "reply,wat"} {
		if _, err := ParseHostGroups(bad); err == nil {
			t.Errorf("ParseHostGroups(%q) must fail", bad)
		}
	}
}

func TestForwardedToolsRequireRunID(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	for tool, err := range f.client.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(tool.Name, "afm_") || slices.Contains([]string{toolRunsList, toolRunStart, toolAttach, toolCancel, toolRelease, toolReport, runtimeTool}, tool.Name) {
			continue
		}
		raw, _ := json.Marshal(tool.InputSchema)
		var schema struct {
			Required []string `json:"required"`
		}
		_ = json.Unmarshal(raw, &schema)
		if !slices.Contains(schema.Required, "run_id") {
			t.Errorf("%s does not require run_id: %s", tool.Name, raw)
		}
	}
}

func TestForwardingReadsAnOwnedRunThroughItsOwnEndpoint(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")

	status, isErr := f.call("afm_run_status", map[string]any{"run_id": "run-1"})
	if isErr || status["run_id"] != "run-1" || status["run_status"] != "running" {
		t.Fatalf("status = %v (error %v)", status, isErr)
	}
	// afm_events_read takes no run_id on the run's endpoint: the host must not pass it on.
	events, isErr := f.call("afm_events_read", map[string]any{"run_id": "run-1"})
	if isErr {
		t.Fatalf("events = %v", events)
	}
	if child.count("tool:afm_events_read") != 1 {
		t.Errorf("the run's endpoint saw %d events_read calls", child.count("tool:afm_events_read"))
	}
	if n := child.count("initialize"); n != 1 {
		t.Errorf("sessions opened to the run = %d, want one reused session", n)
	}
}

func TestForwardingRefusesRunsTheHostDoesNotOwn(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	child.publish(f.regDir, LaunchID("another-host")) // a launch of another host is only discovered

	body, isErr := f.call("afm_run_status", map[string]any{"run_id": "run-1"})
	if !isErr || body["code"] != codeNotAttached {
		t.Errorf("discovered run: %v (error %v)", body, isErr)
	}
	body, isErr = f.call("afm_run_status", map[string]any{"run_id": "ghost"})
	if !isErr || body["code"] != kindNotFound {
		t.Errorf("unknown run: %v", body)
	}
	if child.count("initialize") != 0 {
		t.Error("the host connected to a run it does not own")
	}
}

func TestAttachThenForward(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	child.publish(f.regDir, "")

	body, isErr := f.call(toolAttach, map[string]any{"endpoint": child.srv.URL})
	if isErr || body["run_id"] != "run-1" {
		t.Fatalf("attach = %v", body)
	}
	if status, isErr := f.call("afm_run_status", map[string]any{"run_id": "run-1"}); isErr || status["run_id"] != "run-1" {
		t.Errorf("status after attach = %v", status)
	}
	body, isErr = f.call(toolAttach, map[string]any{"endpoint": "http://192.0.2.1:8080"})
	if !isErr || body["code"] != "invalid" {
		t.Errorf("a non-loopback attach must be refused: %v", body)
	}
}

func TestStaleRunIDGetsRunChangedNotTheNextRunsData(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")
	if _, isErr := f.call("afm_run_status", map[string]any{"run_id": "run-1"}); isErr {
		t.Fatal("first status failed")
	}

	child.nextCycle("run-2")
	child.publish(f.regDir, LaunchID("req"))

	body, isErr := f.call("afm_run_status", map[string]any{"run_id": "run-1"})
	if !isErr || body["code"] != codeRunChanged || body["current_run_id"] != "run-2" {
		t.Fatalf("old run id: %v (error %v)", body, isErr)
	}
	body, isErr = f.call("afm_run_status", map[string]any{"run_id": "run-2"})
	if isErr || body["run_id"] != "run-2" {
		t.Fatalf("new run id: %v (error %v)", body, isErr)
	}
	// A control call with the old id must not reach the engine either.
	body, isErr = f.call("afm_stage_pause", map[string]any{"run_id": "run-1", "stage_id": "build"})
	if !isErr || body["code"] != codeRunChanged || child.pauses.Load() != 0 {
		t.Errorf("control with a stale run id: %v, pauses %d", body, child.pauses.Load())
	}
}

// The registry record can lag a repeat: the session's own status decides which run it answers for.
func TestSessionVerificationCatchesARecordThatLags(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")
	child.nextCycle("run-2") // the record still says run-1

	body, isErr := f.call("afm_run_status", map[string]any{"run_id": "run-1"})
	if !isErr || body["code"] != codeRunChanged || body["current_run_id"] != "run-2" {
		t.Errorf("lagging record: %v (error %v)", body, isErr)
	}
}

func TestControlIsAppliedOnceWhenTheConnectionBreaks(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")
	if _, isErr := f.call("afm_run_status", map[string]any{"run_id": "run-1"}); isErr {
		t.Fatal("warm-up status failed")
	}

	child.mu.Lock()
	child.dropTool = "afm_stage_pause"
	child.mu.Unlock()
	body, isErr := f.call("afm_stage_pause", map[string]any{"run_id": "run-1", "stage_id": "build"})
	if !isErr || body["code"] != codeOutcomeUnknown {
		t.Fatalf("broken connection: %v (error %v)", body, isErr)
	}
	if n := child.pauses.Load(); n != 1 {
		t.Errorf("pause applied %d times, want exactly once", n)
	}
	if n := child.count("tool:afm_stage_pause"); n != 1 {
		t.Errorf("the pause was sent %d times, want once", n)
	}
	// The next call reconnects and verifies the run again.
	if _, isErr := f.call("afm_run_status", map[string]any{"run_id": "run-1"}); isErr {
		t.Error("the host did not recover after the broken connection")
	}
}

func TestReadOnlyCallIsRetriedOnceAfterABrokenConnection(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")
	if _, isErr := f.call("afm_run_status", map[string]any{"run_id": "run-1"}); isErr {
		t.Fatal("warm-up status failed")
	}
	child.mu.Lock()
	child.dropTool = "afm_events_read"
	child.mu.Unlock()
	if body, isErr := f.call("afm_events_read", map[string]any{"run_id": "run-1"}); isErr {
		t.Fatalf("a read must survive one broken connection: %v", body)
	}
	if n := child.count("tool:afm_events_read"); n != 2 {
		t.Errorf("events_read reached the run %d times, want 2 (broken, then retried)", n)
	}
}

func TestGroupIsCheckedOnEveryCall(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")
	f.gw.cfg.Groups = HostGroups{} // permissions narrowed after the tools were registered
	body, isErr := f.call("afm_stage_pause", map[string]any{"run_id": "run-1", "stage_id": "build"})
	if !isErr || body["code"] != "not_allowed" || child.pauses.Load() != 0 {
		t.Errorf("pause with the group off: %v, pauses %d", body, child.pauses.Load())
	}
	body, isErr = f.call(toolCancel, map[string]any{"run_id": "run-1"})
	if !isErr || body["code"] != "not_allowed" {
		t.Errorf("cancel with runs off: %v", body)
	}
}

func TestWaitsPerRunAreBounded(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")
	status, _ := f.call("afm_run_status", map[string]any{"run_id": "run-1"})
	cursor, _ := status["cursor"].(string)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	for range maxForwardedWaits {
		wg.Go(func() {
			_, _ = f.client.CallTool(ctx, &sdkmcp.CallToolParams{Name: "afm_run_wait",
				Arguments: map[string]any{"run_id": "run-1", "cursor": cursor, "timeout_seconds": 30}})
		})
	}
	eventually(t, "the waits to reach the run", func() bool { return child.count("tool:afm_run_wait") == maxForwardedWaits })
	body, isErr := f.call("afm_run_wait", map[string]any{"run_id": "run-1", "cursor": cursor, "timeout_seconds": 30})
	if !isErr || body["code"] != codeWaitCapacity {
		t.Errorf("the ninth wait: %v (error %v)", body, isErr)
	}
	cancel()
	eventually(t, "the cancellation to reach the run", func() bool { return child.count("notifications/cancelled") > 0 })
}

func TestResourcesAreReadThroughTheRunsSession(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")
	res, err := f.client.ReadResource(context.Background(), &sdkmcp.ReadResourceParams{URI: "afm://runs/run-1/status"})
	if err != nil || len(res.Contents) != 1 || !strings.Contains(res.Contents[0].Text, `"run_id":"run-1"`) {
		t.Fatalf("status resource = %+v, %v", res, err)
	}
	if _, err := f.client.ReadResource(context.Background(), &sdkmcp.ReadResourceParams{URI: "afm://runs/ghost/status"}); err == nil {
		t.Error("a resource of an unknown run must not resolve")
	}
}

func TestSubscriptionsAreRelayedOncePerResource(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")
	secondUpdates := make(chan string, 64)
	second := f.connectNotifying(secondUpdates)
	t.Cleanup(func() { _ = second.Close() })
	const uri = "afm://runs/run-1/status"
	ctx := context.Background()

	if err := f.client.Subscribe(ctx, &sdkmcp.SubscribeParams{URI: uri}); err != nil {
		t.Fatal(err)
	}
	if err := second.Subscribe(ctx, &sdkmcp.SubscribeParams{URI: uri}); err != nil {
		t.Fatal(err)
	}
	if n := child.count("resources/subscribe"); n != 1 {
		t.Errorf("the run was asked to subscribe %d times, want once for two host clients", n)
	}
	// One client unsubscribing twice must not count as the other leaving.
	for range 2 {
		if err := f.client.Unsubscribe(ctx, &sdkmcp.UnsubscribeParams{URI: uri}); err != nil {
			t.Fatal(err)
		}
	}
	if n := child.count("resources/unsubscribe"); n != 0 {
		t.Errorf("the run was unsubscribed while a client still listens (%d)", n)
	}

	// A live change of the run reaches the client that is still subscribed, and only that one.
	child.setStatus(state.RunStatusFinished)
	select {
	case got := <-secondUpdates:
		if got != uri {
			t.Errorf("update for %s, want %s", got, uri)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the remaining subscriber got no update of the run")
	}
	select {
	case got := <-f.updates:
		t.Errorf("the client that unsubscribed got an update for %s", got)
	default:
	}

	if err := second.Unsubscribe(ctx, &sdkmcp.UnsubscribeParams{URI: uri}); err != nil {
		t.Fatal(err)
	}
	if n := child.count("resources/unsubscribe"); n != 1 {
		t.Errorf("unsubscribes after the last client left = %d, want 1", n)
	}
}

// A host client that disconnects without unsubscribing must not keep the run subscribed.
func TestDisconnectedHostClientReleasesItsSubscriptions(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")
	second := f.connectNotifying(make(chan string, 8))
	const uri = "afm://runs/run-1/status"
	if err := second.Subscribe(context.Background(), &sdkmcp.SubscribeParams{URI: uri}); err != nil {
		t.Fatal(err)
	}
	_ = second.Close()
	eventually(t, "the run to be unsubscribed", func() bool { return child.count("resources/unsubscribe") == 1 })
}

func TestLostSessionNotifiesSubscribersOnce(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")
	const uri = "afm://runs/run-1/status"
	if err := f.client.Subscribe(context.Background(), &sdkmcp.SubscribeParams{URI: uri}); err != nil {
		t.Fatal(err)
	}
	child.nextCycle("run-2") // the old service closes its sessions

	select {
	case got := <-f.updates:
		if got != uri {
			t.Errorf("update for %s", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no update after the session was lost")
	}
	select {
	case extra := <-f.updates:
		t.Errorf("a second update for %s", extra)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestRunsListShowsOwnershipLivenessAndActions(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	managed := newFakeChild(t, testInstance, "run-m")
	f.managed(managed, "req-m")
	manual := newFakeChild(t, otherInstance, "run-a")
	manual.publish(f.regDir, "")
	discovered := newFakeChild(t, "0123456789abcdef0123456789abcdef", "run-d")
	discovered.publish(f.regDir, LaunchID("another-host"))

	body, isErr := f.call(toolRunsList, nil)
	if isErr {
		t.Fatal(body)
	}
	rows := map[string]map[string]any{}
	for _, r := range body["runs"].([]any) {
		row := r.(map[string]any)
		rows[row["run_id"].(string)] = row
	}
	m, a, d := rows["run-m"], rows["run-a"], rows["run-d"]
	if m["ownership"] != "managed" || m["liveness"] != "active" || m["run_status"] != "running" || !slices.Contains(toStrings(m["actions"]), "cancel") {
		t.Errorf("managed row = %v", m)
	}
	if a["ownership"] != "attached" || a["liveness"] != "active" || a["instance_id"] != otherInstance || a["run_status"] != "running" ||
		len(toStrings(a["actions"])) != 0 {
		t.Errorf("verified manual row = %v, want attached and active without actions on its process", a)
	}
	if d["ownership"] != "discovered" || !slices.Contains(toStrings(d["actions"]), "attach") || d["run_status"] != nil {
		t.Errorf("discovered row = %v", d)
	}

	managed.setStatus(state.RunStatusFinished)
	body, _ = f.call(toolRunsList, nil)
	for _, r := range body["runs"].([]any) {
		if row := r.(map[string]any); row["run_id"] == "run-m" && !slices.Contains(toStrings(row["actions"]), "release") {
			t.Errorf("a finished held run must offer release: %v", row)
		}
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestCancelAndReleaseThroughTheGateway(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")
	var sent []syscall.Signal
	f.ctl.Signal = func(_ int, _ string, sig syscall.Signal) error { sent = append(sent, sig); return nil }
	f.ctl.Alive = func(int, string) bool { return false } // the run stops at once

	body, isErr := f.call(toolRelease, map[string]any{"run_id": "run-1"})
	if !isErr || body["code"] != "precondition_failed" || len(sent) != 0 {
		t.Errorf("release of a running run: %v, signals %v", body, sent)
	}
	body, isErr = f.call(toolCancel, map[string]any{"run_id": "run-1"})
	if isErr || body["state"] != "stopped" || len(sent) != 1 || sent[0] != syscall.SIGINT {
		t.Errorf("cancel: %v, signals %v", body, sent)
	}
	child.setStatus(state.RunStatusFinished)
	sent = nil
	body, isErr = f.call(toolRelease, map[string]any{"run_id": "run-1"})
	if isErr || body["state"] != "stopped" {
		t.Errorf("release: %v", body)
	}
}

func TestAttachRefusalDoesNotEchoCredentials(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	body, isErr := f.call(toolAttach, map[string]any{"endpoint": "http://user:hunter2@127.0.0.1:1"}) //nolint:gosec // the credential is the case under test
	raw, _ := json.Marshal(body)
	if !isErr || strings.Contains(string(raw), "hunter2") {
		t.Errorf("attach with credentials: error %v, body %s", isErr, raw)
	}
}

func TestAttachedRunCannotBeCancelled(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	child.publish(f.regDir, "")
	if _, isErr := f.call(toolAttach, map[string]any{"endpoint": child.srv.URL}); isErr {
		t.Fatal("attach failed")
	}
	f.ctl.Signal = func(int, string, syscall.Signal) error { t.Error("an attached run was signalled"); return nil }
	body, isErr := f.call(toolCancel, map[string]any{"run_id": "run-1"})
	if !isErr || body["code"] != "not_managed" {
		t.Errorf("cancel of an attached run: %v", body)
	}
}

func TestReportReadsAFinishedRunFromItsFiles(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	rec := child.publish(f.regDir, LaunchID("req"))
	if _, _, err := f.host.CreateReceipt("req", baseParams, LaunchIdentity{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(rec.RunsDir, "run-1")
	writeFinishedRun(t, runDir)
	if _, err := f.host.UpdateReceipt(LaunchID("req"), time.Now(), func(r *Receipt) {
		r.State, r.RunID, r.RunsDir, r.InstanceID = StateExited, "run-1", rec.RunsDir, testInstance
	}); err != nil {
		t.Fatal(err)
	}
	// The process is gone: no record, no endpoint.
	if err := runregistry.Remove(f.regDir, testInstance); err != nil {
		t.Fatal(err)
	}
	child.srv.Close()

	body, isErr := f.call(toolReport, map[string]any{"run_id": "run-1"})
	if isErr {
		t.Fatalf("report = %v", body)
	}
	run, _ := body["run"].(map[string]any)
	stages, _ := body["stages"].([]any)
	if run["id"] != "run-1" || len(stages) != 1 {
		t.Errorf("report run = %v, stages = %v", run, stages)
	}
	// Only runs the host knows can be reported, and only from the directory its own records name.
	body, isErr = f.call(toolReport, map[string]any{"run_id": "../../etc"})
	if !isErr {
		t.Errorf("a path-like run_id must be refused: %v", body)
	}
}

func writeFinishedRun(t *testing.T, runDir string) {
	t.Helper()
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	var data []byte
	for i, step := range [][2]state.StageStatus{{state.StatusPending, state.StatusRunning}, {state.StatusRunning, state.StatusDone}} {
		line, _ := json.Marshal(state.Transition{Seq: uint64(i + 1), Time: at.Add(time.Duration(i+1) * time.Second), StageID: "build", From: step[0], To: step[1], Event: "test"})
		data = append(append(data, line...), '\n')
	}
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStartThroughTheGatewayCreatesOneLaunch(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	project := t.TempDir()
	f.launcher.cfg.Config.Projects = []Project{{ID: "app", Path: project}}
	f.launcher.cfg.Config.Pipelines = []Pipeline{{ID: "dev", Source: "/p"}}
	f.launcher.cfg.Binary = os.Args[0]
	f.launcher.cfg.Env = []string{fakeEnv + "=1", "SERVE_FAKE_REGISTRY=" + f.regDir, "PATH=" + os.Getenv("PATH")}
	f.launcher.cfg.PollInterval = 20 * time.Millisecond
	t.Cleanup(func() {
		receipts, _ := f.host.Receipts()
		for _, r := range receipts {
			if r.PID > 0 {
				_ = syscall.Kill(r.PID, syscall.SIGKILL)
			}
		}
		f.launcher.reapers.Wait()
	})
	args := map[string]any{"request_id": "r1", "project": "app", "pipeline": "dev", "mode": "core", "task": "do the thing"}

	first, isErr := f.call(toolRunStart, args)
	if isErr || first["state"] != "starting" || first["launch_id"] != LaunchID("r1") {
		t.Fatalf("start = %v", first)
	}
	again, isErr := f.call(toolRunStart, args)
	if isErr || again["launch_id"] != first["launch_id"] {
		t.Errorf("repeat = %v", again)
	}
	args["mode"] = "dashboard"
	if body, isErr := f.call(toolRunStart, args); !isErr || body["code"] != "request_conflict" {
		t.Errorf("same request_id, other parameters: %v", body)
	}
	args["request_id"], args["project"] = "r2", "nope"
	if body, isErr := f.call(toolRunStart, args); !isErr || body["code"] != "invalid" {
		t.Errorf("unknown project: %v", body)
	}
	eventually(t, "the launch to become ready", func() bool {
		r, _ := f.host.Receipt(LaunchID("r1"))
		return r.State == StateReady
	})
}

// A client that gives up on its request after the host accepted it must not take the work with it. The host is
// held right after the receipt is written, so the cancellation reaches a request that is still in flight.
func TestCancelledStartRequestDoesNotKillTheChild(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	project := t.TempDir()
	out := filepath.Join(t.TempDir(), "out.jsonl")
	f.launcher.cfg.Config.Projects = []Project{{ID: "app", Path: project}}
	f.launcher.cfg.Config.Pipelines = []Pipeline{{ID: "dev", Source: "/p"}}
	f.launcher.cfg.Binary = os.Args[0]
	f.launcher.cfg.Env = []string{fakeEnv + "=1", "SERVE_FAKE_REGISTRY=" + f.regDir, "SERVE_FAKE_OUT=" + out, "PATH=" + os.Getenv("PATH")}
	f.launcher.cfg.PollInterval = 20 * time.Millisecond
	entered, release := make(chan Receipt, 1), make(chan struct{})
	var releaseOnce sync.Once
	releaseHost := func() { releaseOnce.Do(func() { close(release) }) }
	f.launcher.accepted = func(r Receipt) {
		entered <- r
		<-release
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Registered last, so it runs first, on every exit path: the held request is let go and the gateway close
	// waits for the start it is still handling (and refuses a later one), so its child exists and has a reaper
	// before the children are enumerated.
	t.Cleanup(func() {
		cancel()
		releaseHost()
		f.gw.Close()
		killLaunched(f.host)
		f.launcher.reapers.Wait()
	})

	done := make(chan error, 1)
	go func() {
		_, err := f.client.CallTool(ctx, &sdkmcp.CallToolParams{Name: toolRunStart, Arguments: map[string]any{
			"request_id": "r1", "project": "app", "pipeline": "dev", "mode": "core"}})
		done <- err
	}()
	var accepted Receipt
	select {
	case accepted = <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the host never accepted the request")
	}
	if onDisk, err := f.host.Receipt(accepted.LaunchID); err != nil || onDisk.State != StateAccepted || onDisk.PID != 0 {
		t.Fatalf("receipt while the request is held = %+v, %v", onDisk, err)
	}
	select {
	case err := <-done:
		t.Fatalf("the start call returned while the host still held it: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the cancelled call returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the client call did not end on cancel")
	}

	releaseHost()
	ready := waitReceiptState(t, f.host, accepted.LaunchID, StateReady)
	if n := len(readReports(t, out)); n != 1 || ready.PID == 0 {
		t.Errorf("children started = %d (receipt %+v), want exactly one", n, ready)
	}
}

func waitReceiptState(t *testing.T, h *Host, launchID, want string) Receipt {
	t.Helper()
	var last Receipt
	eventually(t, "receipt state "+want, func() bool {
		last, _ = h.Receipt(launchID)
		return last.State == want
	})
	return last
}

// Closing the gateway must not wait for a forwarded wait that is parked on a run until its own timeout.
func TestGatewayCloseDoesNotWaitForParkedForwardedWaits(t *testing.T) {
	f := newHostFixture(t, AllHostGroups())
	child := newFakeChild(t, testInstance, "run-1")
	f.managed(child, "req")
	status, _ := f.call("afm_run_status", map[string]any{"run_id": "run-1"})
	cursor, _ := status["cursor"].(string)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := make(chan struct{})
	go func() {
		defer close(waiting)
		_, _ = f.client.CallTool(ctx, &sdkmcp.CallToolParams{Name: "afm_run_wait",
			Arguments: map[string]any{"run_id": "run-1", "cursor": cursor, "timeout_seconds": 120}})
	}()
	eventually(t, "the wait to reach the run", func() bool { return child.count("tool:afm_run_wait") == 1 })

	closed := make(chan struct{})
	began := time.Now()
	go func() { f.gw.Close(); close(closed); t.Logf("close took %s", time.Since(began)) }()
	select {
	case <-closed:
	case <-time.After(closeGrace + 3*time.Second):
		t.Fatal("Gateway.Close is parked on a forwarded wait")
	}
	cancel()
	<-waiting
}

// A new session is accepted only for the instance the caller resolved: another process may resume the same run
// on the same port.
func TestSessionIsRefusedForAnotherInstanceOfTheSameRun(t *testing.T) {
	child := newFakeChild(t, otherInstance, "run-1")
	sessions := &Sessions{}
	t.Cleanup(sessions.Close)
	view := RunView{RunID: "run-1", InstanceID: testInstance, Endpoint: child.srv.URL}
	ctx := context.Background()
	if _, _, err := sessions.Status(ctx, view); !errors.Is(err, errIdentityChanged) {
		t.Fatalf("status through a session to another instance: %v", err)
	}
	sessions.mu.Lock()
	cached := len(sessions.sessions)
	sessions.mu.Unlock()
	if cached != 0 {
		t.Errorf("a refused session stayed cached (%d)", cached)
	}
	view.InstanceID = otherInstance
	if _, _, err := sessions.Status(ctx, view); err != nil {
		t.Errorf("the right instance: %v", err)
	}
}
