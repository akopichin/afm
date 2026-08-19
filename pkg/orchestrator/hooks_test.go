package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

func TestRunScriptWithRetry_SucceedsOnThirdAttempt(t *testing.T) {
	attempts := 0
	err := runScriptWithRetry(context.Background(), func() error {
		attempts++
		if attempts < 3 {
			return errors.New("boom")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestRunScriptWithRetry_ExhaustsAfterFourAttempts(t *testing.T) {
	attempts := 0
	err := runScriptWithRetry(context.Background(), func() error {
		attempts++
		return errors.New("always fails")
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if attempts != hookMaxRetries+1 {
		t.Errorf("attempts = %d, want %d", attempts, hookMaxRetries+1)
	}
}

func TestRunScriptWithRetry_RespectsBackoffTiming(t *testing.T) {
	attempts := 0
	start := time.Now()
	_ = runScriptWithRetry(context.Background(), func() error {
		attempts++
		return errors.New("fail")
	})
	elapsed := time.Since(start)
	// 1s + 2s + 3s = 6s minimum between the 4 attempts.
	if elapsed < 6*time.Second {
		t.Errorf("elapsed = %v, want >= 6s (backoff not respected)", elapsed)
	}
}

func TestHookPending_WriteReadClear(t *testing.T) {
	dir := t.TempDir()
	p := hookPending{Hook: "before", Script: "echo hi", Timeout: 30 * time.Second}
	if err := writeHookPending(dir, p); err != nil {
		t.Fatalf("writeHookPending: %v", err)
	}
	got, ok := readHookPending(dir)
	if !ok {
		t.Fatal("readHookPending: not found")
	}
	if got != p {
		t.Errorf("readHookPending = %+v, want %+v", got, p)
	}
	clearHookPending(dir)
	if _, ok := readHookPending(dir); ok {
		t.Error("expected pending to be cleared")
	}
}

func TestWaitForHookDecision_ResolveDelivers(t *testing.T) {
	o := &Orchestrator{}
	done := make(chan hookDecision, 1)
	go func() {
		d, ok := o.waitForHookDecision(context.Background(), "s1")
		if !ok {
			t.Error("expected ok=true")
		}
		done <- d
	}()

	// Give the goroutine a moment to register the waiter.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if o.resolveHook("s1", hookDecisionSkip) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case d := <-done:
		if d != hookDecisionSkip {
			t.Errorf("decision = %v, want skip", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for decision to be delivered")
	}
}

func TestWaitForHookDecision_CtxCancelReturnsFalse(t *testing.T) {
	o := &Orchestrator{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, ok := o.waitForHookDecision(ctx, "s1")
	if ok {
		t.Error("expected ok=false when ctx is already cancelled")
	}
}

func TestResolveHook_NoWaiterReturnsFalse(t *testing.T) {
	o := &Orchestrator{}
	if o.resolveHook("nonexistent", hookDecisionRetry) {
		t.Error("expected false when no stage is waiting")
	}
}

var _ = filepath.Join // silence unused import if not otherwise used

func TestExecScript_RunsInRootDirAndPublishesOutput(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}

	ui := bus.NewUIBus()
	subID, events := ui.Subscribe(10)
	defer ui.Unsubscribe(subID)

	o := &Orchestrator{opts: Options{RootDir: rootDir, RunDir: runDir}, ui: ui}

	s := flow.Stage{ID: "s1"}
	logFile := filepath.Join(stageDir, "before.log")
	err := o.execScript(context.Background(), s, "before", "pwd; echo output-line", 5*time.Second, logFile)
	if err != nil {
		t.Fatalf("execScript: %v", err)
	}

	data, _ := os.ReadFile(logFile)
	if !strings.Contains(string(data), rootDir) {
		t.Errorf("script should have run in rootDir, log: %q", string(data))
	}

	select {
	case ev := <-events:
		if ev.Type != bus.EventScriptOutput {
			t.Errorf("event type = %v, want bus.EventScriptOutput", ev.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected at least one bus.EventScriptOutput to be published")
	}
}

// TestExecScript_PersistsOutputToNotices verifies script/hook output survives
// a client reconnecting after the script already finished: EventScriptOutput
// must be durably recorded (via appendNotice, the same mechanism
// EventAgentCompleted/EventContextWarning already use) in <runDir>/notices.jsonl,
// not just published live to the ephemeral UI bus — otherwise a dashboard that
// connects after a fast script/hook completes never sees its output in the
// event feed (only the Log panel, which reads the log file directly).
func TestExecScript_PersistsOutputToNotices(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}

	ui := bus.NewUIBus()
	o := &Orchestrator{opts: Options{RootDir: rootDir, RunDir: runDir}, ui: ui}

	s := flow.Stage{ID: "s1"}
	logFile := filepath.Join(stageDir, "before.log")
	err := o.execScript(context.Background(), s, "before", "echo first-line; echo second-line", 5*time.Second, logFile)
	if err != nil {
		t.Fatalf("execScript: %v", err)
	}

	noticesData, err := os.ReadFile(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		t.Fatalf("read notices.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(noticesData)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 notice lines (one per output line), got %d: %q", len(lines), string(noticesData))
	}

	type scriptOutputData struct {
		Hook string `json:"hook"`
		Line string `json:"line"`
	}
	var entry struct {
		Type    string           `json:"type"`
		StageID string           `json:"stage_id"`
		Data    scriptOutputData `json:"data"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("unmarshal notice: %v", err)
	}
	if entry.Type != string(bus.EventScriptOutput) {
		t.Errorf("notice type = %q, want %q", entry.Type, bus.EventScriptOutput)
	}
	if entry.StageID != "s1" {
		t.Errorf("notice stage_id = %q, want s1", entry.StageID)
	}
	if entry.Data.Hook != "before" || entry.Data.Line != "first-line" {
		t.Errorf("notice data = %+v, want hook=before line=first-line", entry.Data)
	}
}

func setupHookOrch(t *testing.T, stageID string) (*Orchestrator, string) {
	t.Helper()
	rootDir := t.TempDir()
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{stageID})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: stageID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	ui := bus.NewUIBus()
	critical := bus.NewCriticalBus(16)
	stages := []flow.Stage{{ID: stageID}}
	o := &Orchestrator{
		opts:     Options{RootDir: rootDir, RunDir: runDir, Store: store, Stages: stages},
		ui:       ui,
		critical: critical,
		fsm:      bus.NewFSM(store),
		graph:    graph.NewGraph(stages),
	}
	return o, runDir
}

func TestRunBeforeHook_SucceedsFirstTry(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	s := flow.Stage{ID: "s1", ScriptBefore: "echo ok"}
	if !o.runBeforeHook(context.Background(), s) {
		t.Fatal("expected true (proceed) on success")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusRunning {
		t.Errorf("status = %v, want running (unchanged)", got)
	}
}

func TestRunBeforeHook_BlocksThenRetrySucceeds(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(stageDir, "attempt.marker")
	// Script fails until the marker file exists; the test creates it after
	// the stage blocks, then issues a Retry decision.
	s := flow.Stage{ID: "s1", ScriptBefore: "test -f " + marker}

	done := make(chan bool, 1)
	go func() {
		done <- o.runBeforeHook(context.Background(), s)
	}()

	// Wait for the stage to block in hook_failed (4 failed attempts: 1s+2s+3s backoff).
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if o.opts.Store.Get("s1") == state.StatusHookFailed {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if o.opts.Store.Get("s1") != state.StatusHookFailed {
		t.Fatal("stage did not reach hook_failed")
	}
	if _, ok := readHookPending(stageDir); !ok {
		t.Error("expected hook_pending.json to exist")
	}

	if err := os.WriteFile(marker, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if !o.resolveHook("s1", hookDecisionRetry) {
		t.Fatal("resolveHook returned false")
	}

	select {
	case ok := <-done:
		if !ok {
			t.Error("expected true (proceed) after successful retry")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for runBeforeHook to return")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusRunning {
		t.Errorf("status = %v, want running after resolution", got)
	}
	if _, ok := readHookPending(stageDir); ok {
		t.Error("expected hook_pending.json to be cleared")
	}
}

func TestRunBeforeHook_BlocksThenSkip(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	s := flow.Stage{ID: "s1", ScriptBefore: "exit 1"}

	done := make(chan bool, 1)
	go func() {
		done <- o.runBeforeHook(context.Background(), s)
	}()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if o.opts.Store.Get("s1") == state.StatusHookFailed {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !o.resolveHook("s1", hookDecisionSkip) {
		t.Fatal("resolveHook returned false")
	}

	select {
	case ok := <-done:
		if !ok {
			t.Error("expected true (proceed) after skip")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusRunning {
		t.Errorf("status = %v, want running after skip", got)
	}
}

// TestRunBeforeHook_PublishesEventHookFailedOnUIBus regression-tests a bug
// found during an architectural review of the event pipeline: the exhausted-
// retries EventHookFailed for script_before was published to o.critical, a
// bus with exactly one consumer (Run()'s own event loop), whose handleEvent
// switch doesn't even handle this event type — so it was silently discarded,
// and the dashboard never saw the error text for a failed script_before
// hook, live or on reconnect (the FSM status itself still updated fine via
// the Trigger call, so the stage didn't hang — only the diagnostic message
// was lost). Fixed by publishing to o.ui instead, matching the other 3
// sibling call sites (before-resolve, after-fail, after-resolve).
func TestRunBeforeHook_PublishesEventHookFailedOnUIBus(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	s := flow.Stage{ID: "s1", ScriptBefore: "exit 1"}

	subID, ch := o.ui.Subscribe(16)
	defer o.ui.Unsubscribe(subID)

	done := make(chan bool, 1)
	go func() {
		done <- o.runBeforeHook(context.Background(), s)
	}()

	found := false
	deadline := time.After(15 * time.Second)
	for !found {
		select {
		case ev := <-ch:
			if ev.Type != bus.EventHookFailed {
				continue
			}
			data, ok := ev.Data.(map[string]string)
			if !ok || data["error"] == "" {
				t.Fatalf("EventHookFailed missing error text: %+v", ev.Data)
			}
			found = true
		case <-deadline:
			t.Fatal("EventHookFailed never reached a UI bus subscriber")
		}
	}

	if !o.resolveHook("s1", hookDecisionSkip) {
		t.Fatal("resolveHook returned false")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for runBeforeHook to return")
	}
}

func TestRunAfterHook_SucceedsFirstTry_NoEvents(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	if err := o.opts.Store.Apply(&state.Transition{StageID: "s1", From: state.StatusRunning, To: state.StatusDone, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	s := flow.Stage{ID: "s1", ScriptAfter: "echo ok"}
	o.runAfterHook(context.Background(), s)
	if got := o.opts.Store.Get("s1"); got != state.StatusDone {
		t.Errorf("status = %v, want done (unchanged)", got)
	}
	if _, ok := readHookPending(filepath.Join(runDir, "s1")); ok {
		t.Error("no pending hook expected on success")
	}
}

func TestRunAfterHook_FailsThenSkip_StageStaysDone(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	if err := o.opts.Store.Apply(&state.Transition{StageID: "s1", From: state.StatusRunning, To: state.StatusDone, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	s := flow.Stage{ID: "s1", ScriptAfter: "exit 1"}

	done := make(chan struct{})
	go func() {
		o.runAfterHook(context.Background(), s)
		close(done)
	}()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := readHookPending(stageDir); ok {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, ok := readHookPending(stageDir); !ok {
		t.Fatal("expected hook_pending.json for the failed after-hook")
	}
	// Status must NOT have moved to hook_failed for the after-hook.
	if got := o.opts.Store.Get("s1"); got != state.StatusDone {
		t.Errorf("status = %v, want done (after-hook must never block/revert)", got)
	}

	if !o.resolveHook("s1", hookDecisionSkip) {
		t.Fatal("resolveHook returned false")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusDone {
		t.Errorf("status = %v, want done after skip", got)
	}
}

// TestWithBeforeHook_SkipsMainFnIfPausedDuringHook is a regression test for a
// bug found live: script_before runs as a bare shell script with no
// InterruptCh (only the main agent registers one, inside runWithRetry) — so
// Pause() can succeed (durable EvPause transition) while the hook is still
// executing in the background. Without this check, withBeforeHook would
// unconditionally call mainFn once the hook finished, spawning a real agent
// on a stage the user just paused — and a second agent again if they'd
// already clicked Continue in the meantime (Continue's resumeStageAtStatus
// spawns independently). withBeforeHook must check status after the hook
// resolves and skip mainFn if the stage went to paused in the meantime.
func TestWithBeforeHook_SkipsMainFnIfPausedDuringHook(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	s := flow.Stage{ID: "s1", ScriptBefore: "echo ok"}

	called := false
	wrapped := o.withBeforeHook(func(context.Context, flow.Stage) {
		called = true
	})

	// Simulate Pause() landing while the hook was still running in the
	// background: by the time runBeforeHook returns (success), the stage is
	// already paused.
	if _, ok := o.Trigger("s1", bus.EvPause, bus.GuardCtx{}, "test"); !ok {
		t.Fatal("EvPause should be allowed from running")
	}

	wrapped(context.Background(), s)

	if called {
		t.Error("mainFn must not run — the stage was paused while the before-hook was executing")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusPaused {
		t.Errorf("status = %v, want paused (unchanged by withBeforeHook)", got)
	}
}

func TestWithBeforeHook_RunsMainFnWhenNotPaused(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	s := flow.Stage{ID: "s1", ScriptBefore: "echo ok"}

	called := false
	wrapped := o.withBeforeHook(func(context.Context, flow.Stage) {
		called = true
	})

	wrapped(context.Background(), s)

	if !called {
		t.Error("mainFn should run normally when the stage was not paused during the hook")
	}
}
