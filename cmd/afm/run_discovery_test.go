package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/docker"
	"github.com/akopichin/afm/pkg/runregistry"
)

func newTestRegistrar(t *testing.T) (*runRegistrar, string) {
	t.Helper()
	dir := t.TempDir()
	rec := runregistry.Record{
		InstanceID: runregistry.NewInstanceID(),
		LaunchID:   "launch-1",
		PID:        os.Getpid(),
		Mode:       "core",
		Project:    runregistry.Project{Root: "/project"},
	}
	reg, err := newRunRegistrar(dir, rec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.Close)
	return reg, dir
}

func readRecord(t *testing.T, dir, instanceID string) runregistry.Record {
	t.Helper()
	records, err := runregistry.List(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range records {
		if rec.InstanceID == instanceID {
			return rec
		}
	}
	t.Fatalf("no record %s in %s", instanceID, dir)
	return runregistry.Record{}
}

func TestRunRegistrar_EarlyRecordHasNoEndpoint(t *testing.T) {
	reg, dir := newTestRegistrar(t)
	rec := readRecord(t, dir, reg.InstanceID())
	if rec.PID != os.Getpid() || rec.LaunchID != "launch-1" || rec.Mode != "core" {
		t.Errorf("early record = %+v", rec)
	}
	if rec.APIURL != "" || rec.RunID != "" || rec.Ready {
		t.Errorf("early record already claims an endpoint, run or readiness: %+v", rec)
	}
	if rec.StartedAt.IsZero() || rec.UpdatedAt.IsZero() {
		t.Errorf("timestamps missing: %+v", rec)
	}
}

func TestRunRegistrar_UpdatesFollowTheRun(t *testing.T) {
	reg, dir := newTestRegistrar(t)
	reg.SetProject("flow-name", "/wt")
	reg.SetRun("run-1")
	reg.SetEndpoint("http://127.0.0.1:1234")
	reg.SetReady(true)

	rec := readRecord(t, dir, reg.InstanceID())
	if rec.RunID != "run-1" || rec.APIURL != "http://127.0.0.1:1234" || !rec.Ready {
		t.Errorf("record = %+v", rec)
	}
	if rec.Project.RunName != "flow-name" || rec.Project.Worktree != "/wt" || rec.Project.Root != "/project" {
		t.Errorf("project = %+v", rec.Project)
	}

	// A repeat cycle: the new run replaces the old one, which moves into the history, and is not ready yet.
	reg.SetRun("run-2")
	rec = readRecord(t, dir, reg.InstanceID())
	if rec.RunID != "run-2" || rec.Ready || !slices.Equal(rec.PreviousRunIDs, []string{"run-1"}) {
		t.Errorf("after the next cycle: %+v", rec)
	}
	// The same run id again is not another cycle.
	reg.SetRun("run-2")
	if rec = readRecord(t, dir, reg.InstanceID()); len(rec.PreviousRunIDs) != 1 {
		t.Errorf("history = %v", rec.PreviousRunIDs)
	}
}

func TestRunRegistrar_CloseRemovesOnlyOwnRecord(t *testing.T) {
	reg, dir := newTestRegistrar(t)
	other := runregistry.Record{InstanceID: runregistry.NewInstanceID(), PID: 1}
	if err := runregistry.Publish(dir, other); err != nil {
		t.Fatal(err)
	}
	reg.Close()
	records, err := runregistry.List(dir)
	if err != nil || len(records) != 1 || records[0].InstanceID != other.InstanceID {
		t.Fatalf("records after Close = %+v, %v; want only the foreign one", records, err)
	}
	// A late update after Close must not bring the record back.
	reg.SetRun("late")
	reg.Touch()
	if records, _ = runregistry.List(dir); len(records) != 1 {
		t.Errorf("a closed registrar republished: %+v", records)
	}
}

func TestRunRegistrar_HeartbeatMovesUpdatedAt(t *testing.T) {
	reg, dir := newTestRegistrar(t)
	first := readRecord(t, dir, reg.InstanceID()).UpdatedAt
	reg.Every(5*time.Millisecond, reg.Touch)
	waitFor(t, "a heartbeat", func() bool { return readRecord(t, dir, reg.InstanceID()).UpdatedAt.After(first) })
}

func TestRunRegistrar_NilIsInert(t *testing.T) {
	var reg *runRegistrar
	reg.SetProject("a", "b")
	reg.SetRun("r")
	reg.SetEndpoint("http://x")
	reg.SetReady(true)
	reg.Touch()
	reg.Every(time.Millisecond, func() {})
	reg.Close()
}

func TestWatchContainerRuntime(t *testing.T) {
	reg, dir := newTestRegistrar(t)
	var answered atomic.Bool
	runID, instanceID := "run-in-container", reg.InstanceID()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"protocol_version":1,"mode":"core","run_id":"` + runID + `","ready":true,"instance_id":"` + instanceID + `"}`))
	}))
	defer srv.Close()

	poll := containerRuntimePoller(reg, srv.URL, &answered)
	poll()
	rec := readRecord(t, dir, instanceID)
	if rec.RunID != runID || !rec.Ready || !answered.Load() {
		t.Errorf("record = %+v answered=%v", rec, answered.Load())
	}
}

// Another process that answers on the published port is not this run.
func TestWatchContainerRuntime_IgnoresForeignInstance(t *testing.T) {
	reg, dir := newTestRegistrar(t)
	var answered atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"protocol_version":1,"mode":"core","run_id":"other","ready":true,"instance_id":"` + runregistry.NewInstanceID() + `"}`))
	}))
	defer srv.Close()

	containerRuntimePoller(reg, srv.URL, &answered)()
	if rec := readRecord(t, dir, reg.InstanceID()); rec.RunID != "" || rec.Ready || answered.Load() {
		t.Errorf("a foreign instance leaked into the record: %+v", rec)
	}
}

func TestReexecWithRetry(t *testing.T) {
	prevDelay := dockerRetryDelay
	dockerRetryDelay = 0
	t.Cleanup(func() { dockerRetryDelay = prevDelay })
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // test fixture: an executable stub
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())

	tests := []struct {
		name      string
		codes     []int
		started   func(calls int) bool
		wantCalls int
		wantCode  int
	}{
		{name: "125 twice then success", codes: []int{125, 125, 0}, wantCalls: 3, wantCode: 0},
		{name: "gives up after three attempts", codes: []int{125, 125, 125, 0}, wantCalls: 3, wantCode: 125},
		{name: "other codes are not retried", codes: []int{1, 0}, wantCalls: 1, wantCode: 1},
		{name: "no retry once the container answered", codes: []int{125, 0}, started: func(int) bool { return true }, wantCalls: 1, wantCode: 125},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			docker.SetExecFunc(func(string, []string, []string) error {
				code := tt.codes[calls]
				calls++
				return &docker.SubprocessExitError{Code: code}
			})
			t.Cleanup(docker.ResetExecFunc)
			started := func() bool { return tt.started != nil && tt.started(calls) }

			err := reexecWithRetry(docker.ReExecConfig{Image: "img", ProjectDir: t.TempDir()}, started)
			var exitErr *docker.SubprocessExitError
			if !errors.As(err, &exitErr) || exitErr.Code != tt.wantCode {
				t.Fatalf("err = %v, want exit code %d", err, tt.wantCode)
			}
			if calls != tt.wantCalls {
				t.Errorf("docker run attempts = %d, want %d", calls, tt.wantCalls)
			}
		})
	}
}

// useHome gives the child a HOME the test can read and returns its run registry directory.
func (fx *modeFixture) useHome() string {
	fx.t.Helper()
	home := fx.t.TempDir()
	fx.env = append(fx.env, "HOME="+home)
	return runregistry.Dir(home)
}

// waitRecord waits for the only record in the registry that satisfies ok.
func waitRecord(t *testing.T, dir, what string, ok func(runregistry.Record) bool) runregistry.Record {
	t.Helper()
	var found runregistry.Record
	waitFor(t, what, func() bool {
		records, _ := runregistry.List(dir)
		for _, rec := range records {
			if ok(rec) {
				found = rec
				return true
			}
		}
		return false
	})
	return found
}

func TestRunRegistry_NativeRunPublishesAndRemovesItsRecord(t *testing.T) {
	fx := newModeFixture(t, modeCore, "auto.yaml", "")
	dir := fx.useHome()
	instanceID := runregistry.NewInstanceID()
	child := fx.start("--instance-id", instanceID, "--launch-id", "launch-7")
	runID := fx.waitReady(child)

	rec := waitRecord(t, dir, "a ready record", func(r runregistry.Record) bool { return r.Ready })
	if rec.InstanceID != instanceID || rec.LaunchID != "launch-7" || rec.RunID != runID {
		t.Errorf("record = %+v, want instance %s, launch launch-7, run %s", rec, instanceID, runID)
	}
	if rec.APIURL != fmt.Sprintf("http://127.0.0.1:%d", fx.port) || rec.Mode != modeCore || !rec.MCP || rec.Dashboard || rec.Docker {
		t.Errorf("record endpoint/flags = %+v", rec)
	}
	if rec.Project.Root == "" || rec.RunsDir == "" || rec.PID != child.cmd.Process.Pid || rec.ProcessStart == "" {
		t.Errorf("record identity = %+v", rec)
	}
	info, err := os.Stat(filepath.Join(dir, instanceID+".json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("record file = %v, %v; want mode 600", info, err)
	}

	code, body, _ := fx.call(http.MethodGet, "/api/runtime", "", nil)
	if code != http.StatusOK || !strings.Contains(body, `"instance_id":"`+instanceID+`"`) {
		t.Errorf("/api/runtime = %d %s, want the instance id", code, body)
	}

	child.interrupt(t)
	if records, _ := runregistry.List(dir); len(records) != 0 {
		t.Errorf("records left after a clean exit: %+v", records)
	}
}

// A repeating process keeps one record and lists the earlier runs of its cycles.
func TestRunRegistry_RepeatKeepsHistory(t *testing.T) {
	fx := newModeFixture(t, modeCore, "repeat.yaml", "")
	dir := fx.useHome()
	child := fx.start()
	child.waitOutput(t, "afm: starting cycle 2")

	rec := waitRecord(t, dir, "a record with history", func(r runregistry.Record) bool { return len(r.PreviousRunIDs) == 1 })
	if rec.RunID == "" || rec.PreviousRunIDs[0] == rec.RunID {
		t.Errorf("record = %+v, want the second run with the first in the history", rec)
	}
}

// --listen-any-port serves the API on the socket opened up front; the record carries that very port and the run
// keeps its human control (MCP and the API stay mounted).
func TestRunRegistry_ListenAnyPort(t *testing.T) {
	fx := newModeFixture(t, modeCore, "auto.yaml", "")
	dir := fx.useHome()
	args := []string{"run", fx.flowPath, "--dir", fx.root, "--mode", "core", "--listen-any-port"}
	child := startAfmChild(t, fx.root, fx.env, args...)
	interruptOnCleanup(t, child)

	rec := waitRecord(t, dir, "a ready record", func(r runregistry.Record) bool { return r.Ready && r.APIURL != "" })
	fx.port = mustPort(t, rec.APIURL)
	if fx.port == 0 {
		t.Fatalf("record endpoint %q has no port", rec.APIURL)
	}
	fx.waitReady(child)
	if names := mcpToolNames(t, fx.base()+"/mcp"); len(names) == 0 {
		t.Error("no MCP tools on the --listen-any-port API")
	}
}

func mustPort(t *testing.T, apiURL string) int {
	t.Helper()
	u, err := url.Parse(apiURL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return 0
	}
	return port
}

func TestRunRegistry_RefusedFlagsCreateNothing(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "listen-any-port with port", args: []string{"--listen-any-port", "--port", "8080"}, want: "--listen-any-port cannot be combined with --port"},
		{name: "invalid instance id", args: []string{"--instance-id", "nope"}, want: "--instance-id must be 32 lowercase hex characters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newCycleFixture(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("AFM_EXIT_ON_FAIL", "")
			t.Setenv("AFM_DIR", "")
			t.Setenv("AFM_WORKTREES", "0")
			t.Setenv("AFM_USE_DOCKER", "0")
			t.Setenv("AFM_BROWSER", "0")
			fx.write("flow.yaml", fx.flowYAML(flowSpec{}))

			var code int
			_, stderr := captureOutput(t, func() {
				code = runMain(append([]string{"run", fx.flowPath, "--dir", fx.root}, tt.args...))
			})
			if code == 0 || !strings.Contains(stderr, tt.want) {
				t.Fatalf("code = %d, stderr = %q, want a refusal with %q", code, stderr, tt.want)
			}
			if _, err := os.Stat(filepath.Join(fx.root, ".afm", "runs")); !os.IsNotExist(err) {
				t.Errorf("runs directory exists after refused flags (stat err = %v)", err)
			}
			if records, _ := runregistry.List(runregistry.Dir(home)); len(records) != 0 {
				t.Errorf("a refused start published %+v", records)
			}
		})
	}
}

// Docker: the host-side afm registers the published endpoint, the container gets the pinned port and the
// instance id, and the container itself does not register.
func TestRunRegistry_DockerHostRegistersPublishedEndpoint(t *testing.T) {
	fx := &coreFixture{cycleFixture: newExitOnFailFixture(t)}
	t.Setenv("AFM_IN_DOCKER", "")
	t.Cleanup(config.SwapContainerMarkerPresent(func() bool { return false }))
	t.Setenv("AFM_USE_DOCKER", "1")
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	home := t.TempDir()
	t.Setenv("HOME", home)
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // test fixture: an executable stub
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	fx.write("flow.yaml", fx.flowYAML(flowSpec{}))
	fx.writeProjectConfig(t, "server:\n  open_browser: false\n")

	instanceID := runregistry.NewInstanceID()
	var argv []string
	var seen []runregistry.Record
	docker.SetExecFunc(func(_ string, got []string, _ []string) error {
		argv = got
		seen, _ = runregistry.List(runregistry.Dir(home))
		return nil
	})
	t.Cleanup(docker.ResetExecFunc)
	args := []string{"run", fx.flowPath, "--dir", fx.root, "--mode", "core", "--listen-any-port", "--instance-id", instanceID, "--task", "secret task text"}
	prevArgs := os.Args
	os.Args = append([]string{"afm"}, args...)
	t.Cleanup(func() { os.Args = prevArgs })

	var code int
	captureOutput(t, func() { code = runMain(args) })
	if code != 0 {
		t.Fatalf("runMain = %d, want 0", code)
	}
	if len(seen) != 1 || seen[0].InstanceID != instanceID || !seen[0].Docker || !strings.HasPrefix(seen[0].APIURL, "http://127.0.0.1:") {
		t.Fatalf("records while the container ran = %+v, want one host-side docker record", seen)
	}
	port := mustPort(t, seen[0].APIURL)
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		fmt.Sprintf("-p 127.0.0.1:%d:%d", port, port),
		fmt.Sprintf("--port=%d", port),
		"--listen-any-port=false",
		"--instance-id=" + instanceID,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("docker argv lacks %q: %v", want, argv)
		}
	}
	if strings.Contains(joined, "secret task text") || !strings.Contains(joined, "--task=@") {
		t.Errorf("docker argv must carry the task as a file, not as text: %v", argv)
	}
	if records, _ := runregistry.List(runregistry.Dir(home)); len(records) != 0 {
		t.Errorf("records left after the host exited: %+v", records)
	}
}

// Inside the container nothing registers: the host publishes the container's endpoint.
func TestRunRegistry_ContainerDoesNotRegister(t *testing.T) {
	fx := &coreFixture{cycleFixture: newCycleFixture(t)}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AFM_IN_DOCKER", "1")
	t.Cleanup(config.SwapContainerMarkerPresent(func() bool { return true }))
	t.Setenv("AFM_WORKTREES", "0")
	t.Setenv("AFM_BROWSER", "0")
	fx.write("flow.yaml", fx.flowYAML(flowSpec{}))
	fx.writeProjectConfig(t, "server:\n  open_browser: false\n")
	port, err := pickFreeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	// The dashboard hold after a finished run would wait for a tab that never comes.
	prevDrain := drainDashboard
	drainDashboard = func(context.Context, func() int) {}
	t.Cleanup(func() { drainDashboard = prevDrain })

	var code int
	captureOutput(t, func() {
		code = runMain([]string{"run", fx.flowPath, "--dir", fx.root, "--port", strconv.Itoa(port)})
	})
	if code != 0 {
		t.Fatalf("runMain = %d, want 0", code)
	}
	if records, _ := runregistry.List(runregistry.Dir(home)); len(records) != 0 {
		t.Errorf("the container registered itself: %+v", records)
	}
}

// The run registry record must exist while the pipeline is being prepared, so a process that hangs or dies there
// is still discoverable. Preparation is held at its entry; moving openRegistrar below it fails this.
func TestRunDiscovery_RegistersBeforePipelinePreparation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AFM_IN_DOCKER", "") // a container re-exec does not register
	t.Setenv("AFM_EXIT_ON_FAIL", "")
	t.Setenv("AFM_DIR", "")
	t.Setenv("AFM_WORKTREES", "0")
	t.Setenv("AFM_USE_DOCKER", "0")
	t.Setenv("AFM_BROWSER", "0")
	prevRootDir := rootDir
	entered, release := make(chan struct{}), make(chan struct{})
	errHeld := errors.New("preparation ended by the test")
	prepare := func(ctx context.Context, _ pipelineFlags, _ config.Config, _ string, _, _, _ bool) (*pipelinePlan, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, errHeld
	}
	root := newRootCmd()
	if run, _, err := root.Find([]string{"run"}); err == nil {
		root.RemoveCommand(run)
	}
	root.AddCommand(newRunCmdWith(runDeps{preparePipeline: prepare}))
	root.SetArgs([]string{"run", "--pipeline", t.TempDir(), "--dir", t.TempDir()})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	// Joined before the environment and rootDir are restored, on every path out of the test.
	finish := sync.OnceValue(func() error {
		close(release)
		defer func() { cancel(); rootDir = prevRootDir }()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			cancel()
			return <-done
		}
	})
	defer func() { _ = finish() }()

	select {
	case <-entered:
	case err := <-done:
		done <- err
		t.Fatalf("the run ended before its pipeline was prepared: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the run never reached pipeline preparation")
	}
	records, err := runregistry.List(runregistry.Dir(home))
	if err != nil || len(records) != 1 {
		t.Errorf("records while the pipeline is prepared = %+v, %v; want one", records, err)
	}
	if err := finish(); !errors.Is(err, errHeld) {
		t.Errorf("run ended with %v, want the preparation's error", err)
	}
	if records, _ := runregistry.List(runregistry.Dir(home)); len(records) != 0 {
		t.Errorf("the record outlived the run: %+v", records)
	}
}
