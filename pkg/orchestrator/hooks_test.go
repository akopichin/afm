package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
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

	ui := NewUIBus()
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
		if ev.Type != EventScriptOutput {
			t.Errorf("event type = %v, want EventScriptOutput", ev.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected at least one EventScriptOutput to be published")
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
	ui := NewUIBus()
	critical := NewCriticalBus(16)
	o := &Orchestrator{
		opts:     Options{RootDir: rootDir, RunDir: runDir, Store: store},
		ui:       ui,
		critical: critical,
		fsm:      NewFSM(store),
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
