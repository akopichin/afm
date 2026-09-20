package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// G2 (5-е код-ревью): a concurrent Pause()/Revise() landing AFTER the
// outcome-independent F1 status check (retry.go ~:150) but BEFORE the
// verify-driven EvFail (~:216) or the needs_changes incomplete-retry
// transition must NOT be overridden by a stale verify outcome — EvFail's
// rule.From == nil accepts any non-terminal status (including paused/
// revising), so without a local guard the stale transition would silently
// win. o.verifyOutcomeGuardHook (test-only seam) fires exactly at the point
// verifyOutcomeStillOwned re-checks the status, letting these tests inject
// the race deterministically instead of relying on real goroutine timing.

// TestRunWithRetry_VerifyExecError_LateRevise_DoesNotOverrideRevising —
// "a late verify exec-error" scenario from the finding: reaches the FINAL
// EvFail branch (VerifyExecError is never an IncompleteWorkError). The
// guard hook fires a concurrent Revise (Running -> Revising) right before
// the stale EvFail would commit.
func TestRunWithRetry_VerifyExecError_LateRevise_DoesNotOverrideRevising(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.maxRetries = 15

	execErr := &VerifyExecError{Reason: "verify timed out", Step: 2}

	o.verifyOutcomeGuardHook = func(stageID string) {
		if _, ok := o.Trigger(stageID, bus.EvRevise, bus.GuardCtx{}, "concurrent revise"); !ok {
			t.Fatal("test setup: concurrent EvRevise CAS failed")
		}
	}

	var attempts int
	var onUserInterruptedCalled bool
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(retryContext string) error { attempts++; return nil },
		func() error { return execErr },
		func() { onUserInterruptedCalled = true },
	)

	if attempts != 1 {
		t.Fatalf("author must not be re-run in this call, got %d attempts", attempts)
	}
	if !onUserInterruptedCalled {
		t.Error("expected onUserInterrupted to be called for the concurrent revise, same as the F1 switch")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusRevising {
		t.Fatalf("status = %v, want revising — the stale verify exec-error must not override the concurrent revise", got)
	}

	data, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "verify timed out") {
		t.Errorf("EvFail must never have been committed; events.jsonl should not carry the stale verify diagnosis: %s", data)
	}
}

// TestRunWithRetry_VerifyExecError_LatePause_DoesNotOverridePaused — same
// race, Pause() instead of Revise(): the stale EvFail must not resurrect a
// paused stage back into failed.
func TestRunWithRetry_VerifyExecError_LatePause_DoesNotOverridePaused(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.maxRetries = 15

	execErr := &VerifyExecError{Reason: "verify timed out", Step: 2}

	o.verifyOutcomeGuardHook = func(stageID string) {
		if _, ok := o.Trigger(stageID, bus.EvPause, bus.GuardCtx{}, "concurrent pause"); !ok {
			t.Fatal("test setup: concurrent EvPause CAS failed")
		}
	}

	var onUserInterruptedCalled bool
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(retryContext string) error { return nil },
		func() error { return execErr },
		func() { onUserInterruptedCalled = true },
	)

	if onUserInterruptedCalled {
		t.Error("Pause() already recorded the durable transition — onUserInterrupted must not fire")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusPaused {
		t.Fatalf("status = %v, want paused — the stale verify exec-error must not override the concurrent pause", got)
	}

	data, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "verify timed out") {
		t.Errorf("EvFail must never have been committed; events.jsonl should not carry the stale verify diagnosis: %s", data)
	}
}

// TestRunWithRetry_NeedsChanges_LateRevise_DoesNotOverrideRevising — the
// "needs_changes incomplete-retry transition" call site (attempt 0, the
// `continue` branch, never reaches EvFail at all): the race lands right
// before it would set incompleteReason/continue the loop.
func TestRunWithRetry_NeedsChanges_LateRevise_DoesNotOverrideRevising(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.maxRetries = 15

	rejected := &VerifyRejectedError{
		IncompleteWorkError: &stagefiles.IncompleteWorkError{Reason: "verify needs changes (step 1): missing tests"},
		ReportID:            "ver123",
		Step:                1,
	}

	o.verifyOutcomeGuardHook = func(stageID string) {
		if _, ok := o.Trigger(stageID, bus.EvRevise, bus.GuardCtx{}, "concurrent revise"); !ok {
			t.Fatal("test setup: concurrent EvRevise CAS failed")
		}
	}

	var attempts int
	var onUserInterruptedCalled bool
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(retryContext string) error { attempts++; return nil },
		func() error { return rejected },
		func() { onUserInterruptedCalled = true },
	)

	if attempts != 1 {
		t.Fatalf("the free incomplete-retry must not run — the stage no longer belongs to this call, got %d attempts", attempts)
	}
	if !onUserInterruptedCalled {
		t.Error("expected onUserInterrupted to be called for the concurrent revise")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusRevising {
		t.Fatalf("status = %v, want revising — the stale needs_changes outcome must not override the concurrent revise", got)
	}
}

// TestRunWithRetry_NeedsChanges_LatePause_DoesNotOverridePaused — same as
// above with Pause() instead of Revise().
func TestRunWithRetry_NeedsChanges_LatePause_DoesNotOverridePaused(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.maxRetries = 15

	rejected := &VerifyRejectedError{
		IncompleteWorkError: &stagefiles.IncompleteWorkError{Reason: "verify needs changes (step 1): missing tests"},
		ReportID:            "ver123",
		Step:                1,
	}

	o.verifyOutcomeGuardHook = func(stageID string) {
		if _, ok := o.Trigger(stageID, bus.EvPause, bus.GuardCtx{}, "concurrent pause"); !ok {
			t.Fatal("test setup: concurrent EvPause CAS failed")
		}
	}

	var attempts int
	var onUserInterruptedCalled bool
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(retryContext string) error { attempts++; return nil },
		func() error { return rejected },
		func() { onUserInterruptedCalled = true },
	)

	if attempts != 1 {
		t.Fatalf("the free incomplete-retry must not run — the stage no longer belongs to this call, got %d attempts", attempts)
	}
	if onUserInterruptedCalled {
		t.Error("Pause() already recorded the durable transition — onUserInterrupted must not fire")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusPaused {
		t.Fatalf("status = %v, want paused — the stale needs_changes outcome must not override the concurrent pause", got)
	}
}

// TestRunWithRetry_SecondNeedsChanges_LateRevise_DoesNotOverrideRevising —
// "a second needs_changes" from the finding: attempt 0 completes the free
// incomplete-retry normally (no race yet — the guard hook only starts
// racing from the second call on), attempt 1's needs_changes falls through
// to the FINAL EvFail branch (attempt != 0), where the race lands.
func TestRunWithRetry_SecondNeedsChanges_LateRevise_DoesNotOverrideRevising(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.maxRetries = 15

	rejected := &VerifyRejectedError{
		IncompleteWorkError: &stagefiles.IncompleteWorkError{Reason: "verify needs changes (step 1): missing tests"},
		ReportID:            "ver123",
		Step:                1,
	}

	var guardCalls int
	o.verifyOutcomeGuardHook = func(stageID string) {
		guardCalls++
		if guardCalls < 2 {
			return // attempt 0's incomplete-retry: no race yet, behaves normally
		}
		if _, ok := o.Trigger(stageID, bus.EvRevise, bus.GuardCtx{}, "concurrent revise"); !ok {
			t.Fatal("test setup: concurrent EvRevise CAS failed")
		}
	}

	var attempts int
	var onUserInterruptedCalled bool
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(retryContext string) error { attempts++; return nil },
		func() error { return rejected },
		func() { onUserInterruptedCalled = true },
	)

	if attempts != 2 {
		t.Fatalf("expected exactly 2 attempts (one free incomplete-retry, then the race on the second needs_changes), got %d", attempts)
	}
	if !onUserInterruptedCalled {
		t.Error("expected onUserInterrupted to be called for the concurrent revise")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusRevising {
		t.Fatalf("status = %v, want revising — the stale second needs_changes outcome must not override the concurrent revise", got)
	}
}
