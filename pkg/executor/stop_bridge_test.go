package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// stopHookProbe is a StopHookClient that records how the bridge used it.
type stopHookProbe struct {
	mu       sync.Mutex
	requests []StopRequest
	minted   int
	revoked  int
	decision StopDecision
}

func (p *stopHookProbe) client() *StopHookClient {
	return &StopHookClient{Mint: func() (func(StopRequest) StopDecision, func()) {
		p.mu.Lock()
		p.minted++
		p.mu.Unlock()
		decide := func(req StopRequest) StopDecision {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.requests = append(p.requests, req)
			return p.decision
		}
		revoke := func() {
			p.mu.Lock()
			p.revoked++
			p.mu.Unlock()
		}
		return decide, revoke
	}}
}

func (p *stopHookProbe) seen() []StopRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]StopRequest(nil), p.requests...)
}

func (p *stopHookProbe) revokedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.revoked
}

// openStopBridge starts the bridge of one invocation and returns its descriptor path and descriptor.
func openStopBridge(t *testing.T, p *stopHookProbe) (path string, d StopHookDescriptor, cleanup func()) {
	t.Helper()
	e := New(Config{Command: "claude", StopHook: p.client()})
	hook, err := e.setupStopHook()
	if err != nil {
		t.Fatal(err)
	}
	env, cleanup := hook.env, hook.cleanup
	path = envValue(env, StopHookDescriptorEnv)
	if path == "" {
		cleanup()
		t.Fatalf("%s must be set, env = %v", StopHookDescriptorEnv, env)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &d); err != nil {
		cleanup()
		t.Fatal(err)
	}
	return path, d, cleanup
}

// postStop sends a raw request to the bridge, as a hook client that does not follow the contract might.
func postStop(t *testing.T, d StopHookDescriptor, token string, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, d.URL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestStopBridge_DescriptorIsPrivateAndHoldsTheToken(t *testing.T) {
	p := &stopHookProbe{}
	path, d, cleanup := openStopBridge(t, p)
	defer cleanup()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("descriptor mode = %v, want 0600", info.Mode().Perm())
	}
	if d.Token == "" || d.Invocation == "" || !strings.HasPrefix(d.URL, "http://127.0.0.1:") {
		t.Fatalf("descriptor = %+v, want a loopback url, a token and an invocation", d)
	}
}

func TestStopBridge_TokenIsNeverInTheEnvironment(t *testing.T) {
	p := &stopHookProbe{}
	e := New(Config{Command: "claude", StopHook: p.client()})
	hook, err := e.setupStopHook()
	if err != nil {
		t.Fatal(err)
	}
	env := hook.env
	defer hook.cleanup()
	data, err := os.ReadFile(envValue(env, StopHookDescriptorEnv))
	if err != nil {
		t.Fatal(err)
	}
	var d StopHookDescriptor
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}
	for _, kv := range env {
		if strings.Contains(kv, d.Token) || strings.Contains(kv, d.Invocation) {
			t.Fatalf("env entry %q carries a secret; only the descriptor path may", kv)
		}
	}
}

func TestStopBridge_DecisionTravelsToTheHookCommandOutput(t *testing.T) {
	tests := []struct {
		name     string
		decision StopDecision
		want     string
	}{
		{"block", StopDecision{Block: true, Reason: "write the summary"}, `{"decision":"block","reason":"write the summary"}`},
		{"allow", StopDecision{}, `{}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &stopHookProbe{decision: tc.decision}
			path, _, cleanup := openStopBridge(t, p)
			defer cleanup()

			out, err := RunStopHook(context.Background(), path, strings.NewReader(`{"hook_event_name":"Stop","stop_hook_active":true}`))
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(out)) != tc.want {
				t.Fatalf("hook output = %s, want %s", out, tc.want)
			}
			got := p.seen()
			if len(got) != 1 || !got[0].HookActive || got[0].RequestID == "" {
				t.Fatalf("decide saw %+v, want one request with the hook-active flag and an id", got)
			}
		})
	}
}

func TestStopBridge_RejectsWhatIsNotItsOwnInvocation(t *testing.T) {
	p := &stopHookProbe{decision: StopDecision{Block: true, Reason: "x"}}
	_, d, cleanup := openStopBridge(t, p)
	defer cleanup()
	valid := `{"invocation":"` + d.Invocation + `","request_id":"r1"}`

	tests := []struct {
		name  string
		token string
		body  string
		want  int
	}{
		{"no token", "", valid, http.StatusUnauthorized},
		{"wrong token", "not-" + d.Token, valid, http.StatusUnauthorized},
		{"other invocation", d.Token, `{"invocation":"another","request_id":"r1"}`, http.StatusForbidden},
		{"no request id", d.Token, `{"invocation":"` + d.Invocation + `"}`, http.StatusBadRequest},
		{"not json", d.Token, `{`, http.StatusBadRequest},
		{"oversized body", d.Token, `{"request_id":"` + strings.Repeat("x", 1<<16) + `"}`, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := postStop(t, d, tc.token, tc.body).StatusCode; got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}
	if got := p.seen(); len(got) != 0 {
		t.Fatalf("decide ran for rejected requests: %+v", got)
	}
}

func TestStopBridge_OnlyPostIsServed(t *testing.T) {
	p := &stopHookProbe{}
	_, d, cleanup := openStopBridge(t, p)
	defer cleanup()
	req, err := http.NewRequest(http.MethodGet, d.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+d.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", resp.StatusCode)
	}
}

// A hook command that retries a lost response sends the same request id: it gets the same answer and the
// decision (which may spend a durable budget) is made once.
func TestStopBridge_RepeatedRequestIDIsAnsweredFromTheReceipt(t *testing.T) {
	p := &stopHookProbe{decision: StopDecision{Block: true, Reason: "once"}}
	_, d, cleanup := openStopBridge(t, p)
	defer cleanup()
	body := `{"invocation":"` + d.Invocation + `","request_id":"same"}`

	var answers []string
	for range 2 {
		resp := postStop(t, d, d.Token, body)
		raw, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d err %v", resp.StatusCode, err)
		}
		answers = append(answers, string(raw))
	}
	if answers[0] != answers[1] || !strings.Contains(answers[0], `"block":true`) {
		t.Fatalf("answers = %q, want the same blocking answer twice", answers)
	}
	if got := p.seen(); len(got) != 1 {
		t.Fatalf("decide ran %d times for one request id, want 1", len(got))
	}
}

func TestStopBridge_DistinctRequestsAreEachDecided(t *testing.T) {
	p := &stopHookProbe{}
	_, d, cleanup := openStopBridge(t, p)
	defer cleanup()
	for _, id := range []string{"a", "b"} {
		postStop(t, d, d.Token, `{"invocation":"`+d.Invocation+`","request_id":"`+id+`"}`)
	}
	if got := p.seen(); len(got) != 2 {
		t.Fatalf("decide ran %d times, want one per distinct request", len(got))
	}
}

// The bridge lives exactly as long as the call: listener, descriptor and capability are all gone after cleanup.
func TestStopBridge_CleanupClosesListenerRevokesAndRemovesTheDescriptor(t *testing.T) {
	p := &stopHookProbe{}
	path, d, cleanup := openStopBridge(t, p)
	if p.revokedCount() != 0 {
		t.Fatal("revoked before cleanup")
	}

	cleanup()
	cleanup() // idempotent: deferred paths may run it twice

	if p.revokedCount() != 1 {
		t.Fatalf("revoked %d times, want exactly once", p.revokedCount())
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("descriptor dir survives cleanup: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, d.URL, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	if resp, err := client.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatal("the listener still answers after cleanup")
	}
}

func TestSetupStopHook_AMintWithoutDecideIsAnError(t *testing.T) {
	var revoked bool
	e := New(Config{Command: "claude", StopHook: &StopHookClient{
		Mint: func() (func(StopRequest) StopDecision, func()) { return nil, func() { revoked = true } },
	}})
	if _, err := e.setupStopHook(); err == nil {
		t.Fatal("a Mint without a decision callback must not open a bridge")
	}
	if !revoked {
		t.Fatal("the unusable capability must still be revoked")
	}
}

// The hook command never blocks a stop by accident: whatever goes wrong it answers "allow" (an empty object,
// exit 0) and reports why on the error.
func TestRunStopHook_FailuresAnswerAllowAndReportTheCause(t *testing.T) {
	p := &stopHookProbe{decision: StopDecision{Block: true, Reason: "x"}}
	path, _, cleanup := openStopBridge(t, p)
	cleanup() // the bridge is already gone: the descriptor points nowhere

	tests := []struct {
		name  string
		path  string
		stdin string
	}{
		{"no descriptor in the environment", "", `{}`},
		{"descriptor file missing", filepath.Join(t.TempDir(), "absent.json"), `{}`},
		{"bridge closed", path, `{}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, err := RunStopHook(ctx, tc.path, strings.NewReader(tc.stdin))
			if err == nil {
				t.Fatal("want the cause of the failure")
			}
			if strings.TrimSpace(string(out)) != "{}" {
				t.Fatalf("output = %q, want {}", out)
			}
		})
	}
}

func TestRunStopHook_UnreadableInputAnswersAllow(t *testing.T) {
	p := &stopHookProbe{decision: StopDecision{Block: true, Reason: "x"}}
	path, _, cleanup := openStopBridge(t, p)
	defer cleanup()

	out, err := RunStopHook(context.Background(), path, strings.NewReader(`not json`))
	if err == nil || strings.TrimSpace(string(out)) != "{}" {
		t.Fatalf("out = %q err = %v, want {} and an error", out, err)
	}
	if got := p.seen(); len(got) != 0 {
		t.Fatalf("an unreadable hook event reached the decision: %+v", got)
	}
}

func TestRunStopHook_OversizedInputIsBounded(t *testing.T) {
	p := &stopHookProbe{decision: StopDecision{Block: true, Reason: "x"}}
	path, _, cleanup := openStopBridge(t, p)
	defer cleanup()

	huge := `{"stop_hook_active":false,"pad":"` + strings.Repeat("x", maxHookInputBytes) + `"}`
	out, err := RunStopHook(context.Background(), path, strings.NewReader(huge))
	if err == nil || strings.TrimSpace(string(out)) != "{}" {
		t.Fatalf("out = %q err = %v, want {} and an error", out, err)
	}
}

func TestFilterInheritedEnv_StripsStopHookTransport(t *testing.T) {
	ambient := []string{StopHookDescriptorEnv + "=/tmp/x", "PATH=/bin"}
	for _, cfg := range []Config{{VerifyMode: true}, {}} {
		e := New(cfg)
		for _, kv := range e.filterInheritedEnv(ambient) {
			if IsStopHookTransportEnv(kv) {
				t.Fatalf("a child env carries %q (cfg=%+v)", kv, cfg)
			}
		}
	}
	for _, kv := range New(Config{}).jsonQueryEnv(ambient) {
		if IsStopHookTransportEnv(kv) {
			t.Fatalf("a JSON query env carries %q", kv)
		}
	}
}

// agentScript starts an agent that reports its stop-hook environment to envFile and waits for releaseFile.
func stopHookAgentScript(t *testing.T, dir string) (script, envFile, releaseFile string) {
	t.Helper()
	envFile = filepath.Join(dir, "env.txt")
	releaseFile = filepath.Join(dir, "release")
	body := `printf '%s' "$` + StopHookDescriptorEnv + `" > ` + envFile + `.tmp && mv ` + envFile + `.tmp ` + envFile + "\n" +
		"while [ ! -e " + releaseFile + " ]; do sleep 0.05; done\n" +
		`echo '{"type":"result","subtype":"success","is_error":false,"result":"ok"}'` + "\n"
	return writeScript(t, dir, "agent.sh", body), envFile, releaseFile
}

func waitForFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			return data
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not appear", path)
	return nil
}

// An author run with a StopHook capability hands its agent a live bridge, and the bridge is closed when the call
// returns — however the agent got there.
func TestRun_StopHookBridgeLivesForTheCall(t *testing.T) {
	dir := t.TempDir()
	script, envFile, releaseFile := stopHookAgentScript(t, dir)
	p := &stopHookProbe{decision: StopDecision{Block: true, Reason: "write it"}}
	e := New(Config{Command: script, IdleTimeout: 30 * time.Second, StopHook: p.client()})

	done := make(chan error, 1)
	go func() {
		done <- e.RunAgent(context.Background(), "autonomous", "s1", "prompt", filepath.Join(dir, "run.log"))
	}()

	descriptor := string(waitForFile(t, envFile))
	out, err := RunStopHook(context.Background(), descriptor, strings.NewReader(`{"stop_hook_active":false}`))
	if err != nil || !strings.Contains(string(out), `"decision":"block"`) {
		t.Fatalf("out = %q err = %v, want a blocking answer from the live bridge", out, err)
	}
	if err := os.WriteFile(releaseFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	if _, err := os.Stat(descriptor); !os.IsNotExist(err) {
		t.Fatalf("descriptor survives the call: %v", err)
	}
	if p.revokedCount() != 1 {
		t.Fatalf("revoked %d times, want once", p.revokedCount())
	}
	if out, err := RunStopHook(context.Background(), descriptor, strings.NewReader(`{}`)); err == nil || strings.TrimSpace(string(out)) != "{}" {
		t.Fatalf("a hook of the finished call got out = %q err = %v, want {} and an error", out, err)
	}
}

// Isolated runs never see the bridge, even if their config carries the capability.
func TestRun_IsolatedRunsGetNoStopHook(t *testing.T) {
	for name, cfg := range map[string]Config{
		"verify": {VerifyMode: true},
		"helper": {runMode: ModeHelper},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			script := writeScript(t, dir, "agent.sh", `printf '%s' "$`+StopHookDescriptorEnv+`" > `+filepath.Join(dir, "env.txt")+"\n"+
				`echo '{"type":"result","subtype":"success","is_error":false,"result":"ok"}'`+"\n")
			p := &stopHookProbe{}
			cfg.Command, cfg.IdleTimeout, cfg.StopHook = script, 30*time.Second, p.client()
			e := New(cfg)

			var buf bytes.Buffer
			if err := e.run(context.Background(), "prompt", "x", &buf, func(string) {}); err != nil {
				t.Fatalf("run: %v", err)
			}
			if data, _ := os.ReadFile(filepath.Join(dir, "env.txt")); len(data) != 0 {
				t.Fatalf("an isolated run got the descriptor %q", data)
			}
			if p.minted != 0 {
				t.Fatalf("minted %d capabilities for an isolated run", p.minted)
			}
		})
	}
}

// A bridge that cannot be opened must not fail the agent: the hook is an extra, AFM's own recovery remains.
func TestRun_UnavailableBridgeDoesNotFailTheAgent(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "agent.sh", `printf '%s' "$`+StopHookDescriptorEnv+`" > `+filepath.Join(dir, "env.txt")+"\n"+
		`echo '{"type":"result","subtype":"success","is_error":false,"result":"ok"}'`+"\n")
	e := New(Config{Command: script, IdleTimeout: 30 * time.Second, StopHook: &StopHookClient{
		Mint: func() (func(StopRequest) StopDecision, func()) { return nil, nil },
	}})

	var stderr bytes.Buffer
	if err := e.run(context.Background(), "prompt", "x", &stderr, func(string) {}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "env.txt")); len(data) != 0 {
		t.Fatalf("the agent got a descriptor %q although no bridge exists", data)
	}
	if !strings.Contains(stderr.String(), "stop hook unavailable") {
		t.Fatalf("stderr = %q, want the reason the hook is unavailable", stderr.String())
	}
}
