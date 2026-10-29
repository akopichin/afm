package bus

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
	"pgregory.net/rapid"
)

func newTestFSM(t *testing.T, stages []string) (*FSM, *state.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := state.Open(filepath.Join(dir, "run"), stages)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	return NewFSM(store), store
}

func TestFSM_Apply_LegalTransitions(t *testing.T) {
	cases := []struct {
		name   string
		from   state.StageStatus
		event  FSMEvent
		wantTo state.StageStatus
		wantOK bool
	}{
		{"pending->planning", state.StatusPending, EvStartPlanning, state.StatusPlanning, true},
		{"planning->awaiting", state.StatusPlanning, EvPlanReady, state.StatusAwaitingApproval, true},
		{"awaiting->ready", state.StatusAwaitingApproval, EvApprove, state.StatusReady, true},
		{"awaiting->revising", state.StatusAwaitingApproval, EvRevise, state.StatusRevising, true},
		{"revising->planning", state.StatusRevising, EvStartPlanning, state.StatusPlanning, true},
		{"ready->running", state.StatusReady, EvStartRun, state.StatusRunning, true},
		{"running->done", state.StatusRunning, EvComplete, state.StatusDone, true},
		{"running->failed", state.StatusRunning, EvFail, state.StatusFailed, true},
		{"failed->pending(manual)", state.StatusFailed, EvManualRetry, state.StatusPending, true},
		{"pending->failed(blocked)", state.StatusPending, EvBlockedByDep, state.StatusFailed, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsm, store := newTestFSM(t, []string{"a"})
			defer store.Close()
			_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: tc.from, Event: "test_setup"})

			_, to, _, ok, err := fsm.Apply("a", tc.event, GuardCtx{}, "")
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if ok != tc.wantOK {
				t.Errorf("applied = %v, want %v", ok, tc.wantOK)
			}
			if to != tc.wantTo {
				t.Errorf("to = %q, want %q", to, tc.wantTo)
			}
		})
	}
}

func TestFSM_Apply_IllegalReturnsApplyFalse(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusDone, Event: "test_setup"})

	_, _, _, ok, err := fsm.Apply("a", EvStartPlanning, GuardCtx{}, "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if ok {
		t.Error("illegal transition: ok = true, want false")
	}
}

func TestFSM_Apply_RetryingStartPlanning(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusRetrying, Event: "test_setup"})

	_, to, _, ok, err := fsm.Apply("a", EvStartPlanning, GuardCtx{}, "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok || to != state.StatusPlanning {
		t.Errorf("retrying->planning: got (%v, %v), want (planning, true)", to, ok)
	}
}

func TestFSM_RetryFromAwaitingUserInput(t *testing.T) {
	t.Run("EvScheduleRetry: awaiting_user_input->retrying", func(t *testing.T) {
		fsm, store := newTestFSM(t, []string{"a"})
		defer store.Close()
		_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusAwaitingUserInput, Event: "test_setup"})

		_, to, _, ok, err := fsm.Apply("a", EvScheduleRetry, GuardCtx{Phase: string(flow.PhaseImplementation)}, "")
		if err != nil {
			t.Fatal(err)
		}
		if !ok || to != state.StatusRetrying {
			t.Fatalf("EvScheduleRetry from awaiting_user_input: got (%v, %v), want (retrying, true)", to, ok)
		}
	})

	t.Run("EvResumeAfterRetry: awaiting_user_input->running", func(t *testing.T) {
		fsm, store := newTestFSM(t, []string{"a"})
		defer store.Close()
		_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusAwaitingUserInput, Event: "test_setup"})

		_, to, _, ok, err := fsm.Apply("a", EvResumeAfterRetry, GuardCtx{Phase: string(flow.PhaseImplementation)}, "")
		if err != nil {
			t.Fatal(err)
		}
		if !ok || to != state.StatusRunning {
			t.Fatalf("EvResumeAfterRetry from awaiting_user_input: got (%v, %v), want (running, true)", to, ok)
		}
	})
}

func TestFSM_Apply_ReviseFromRunning(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"})

	_, to, _, ok, err := fsm.Apply("a", EvRevise, GuardCtx{}, "feedback text")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok || to != state.StatusRevising {
		t.Errorf("running->revising: got (%v, %v), want (revising, true)", to, ok)
	}
}

// ExpectFrom narrows a transition to the status the caller decided on: a Revise meant for a running agent does
// not move a stage that a concurrent Pause has already paused (EvRevise itself accepts paused).
func TestFSM_Apply_ExpectFromRefusesAnotherStatus(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusPaused, Event: "test_setup"})

	_, to, _, ok, err := fsm.Apply("a", EvRevise, GuardCtx{ExpectFrom: state.StatusRunning}, "question")
	if err != nil || ok || to != state.StatusPaused {
		t.Fatalf("got (%v, %v, %v), want the paused stage untouched", to, ok, err)
	}
	if got := store.Get("a"); got != state.StatusPaused {
		t.Fatalf("status = %v, want paused", got)
	}
}

func TestEvRevise_FromPaused(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusPaused, Event: "test_setup"})

	_, to, _, ok, err := fsm.Apply("a", EvRevise, GuardCtx{}, "review inject")
	if err != nil || !ok || to != state.StatusRevising {
		t.Fatalf("EvRevise from paused: to=%v ok=%v err=%v", to, ok, err)
	}
}

func TestFSM_Apply_AskUser(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"})

	_, to, _, ok, err := fsm.Apply("a", EvAskUser, GuardCtx{Phase: "implementation"}, "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok || to != state.StatusAwaitingUserInput {
		t.Errorf("running->awaiting_user_input: got (%v, %v), want (awaiting_user_input, true)", to, ok)
	}
}

func TestFSM_Apply_AskUser_FromRetry(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusRetrying, Event: "test_setup"})

	_, to, _, ok, err := fsm.Apply("a", EvAskUser, GuardCtx{Phase: "implementation"}, "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok || to != state.StatusAwaitingUserInput {
		t.Errorf("retrying->awaiting_user_input: got (%v, %v), want (awaiting_user_input, true)", to, ok)
	}
}

// A question left open in revising belongs to the turn the Revise interrupted: handing the stage to the user would take
// it from the Revise's feedback run.
func TestFSM_Apply_AskUser_NotFromRevising(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusRevising, Event: "test_setup"})

	_, _, _, ok, err := fsm.Apply("a", EvAskUser, GuardCtx{Phase: "implementation"}, "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if ok {
		t.Error("revising->awaiting_user_input was applied")
	}
}

func TestFSM_PhaseDispatch_UserAnswered(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusAwaitingUserInput, Event: "test_setup"})

	_, to, _, ok, err := fsm.Apply("a", EvUserAnswered, GuardCtx{Phase: "planning"}, "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok || to != state.StatusPlanning {
		t.Errorf("planning phase: got (%v, %v), want (planning, true)", to, ok)
	}
}

func TestFSM_PhaseDispatch_ResumeAfterRetry(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusRetrying, Event: "test_setup"})

	_, to, _, ok, err := fsm.Apply("a", EvResumeAfterRetry, GuardCtx{Phase: "implementation"}, "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok || to != state.StatusRunning {
		t.Errorf("impl phase: got (%v, %v), want (running, true)", to, ok)
	}
}

// Ensure GuardCtx can hold a flow.Stage without compile error.
var _ flow.Stage

func TestFSM_Property_LivenessTerminates(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		fsm, store := newTestFSMRapid(t, []string{"a"})
		defer store.Close()

		events := []FSMEvent{
			EvStartPlanning, EvPlanReady, EvApprove, EvRevise,
			EvStartRun, EvComplete, EvFail, EvUserAnswered,
			EvScheduleRetry, EvResumeAfterRetry, EvManualRetry, EvBlockedByDep,
		}

		const maxSteps = 200
		for i := 0; i < maxSteps; i++ {
			ev := rapid.SampledFrom(events).Draw(t, "event")
			_, _, _, _, _ = fsm.Apply("a", ev, GuardCtx{Phase: "implementation"}, "")
			if IsTerminal(store.Get("a")) {
				return
			}
		}
		t.Errorf("did not reach terminal in %d steps; last status: %q", maxSteps, store.Get("a"))
	})
}

func TestFSM_HookFailedTransitions(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"s1"})
	defer store.Close()

	// running -> hook_failed
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatalf("setup transition: %v", err)
	}
	_, to, _, ok, err := fsm.Apply("s1", EvHookFailed, GuardCtx{}, "before hook failed")
	if err != nil || !ok || to != state.StatusHookFailed {
		t.Fatalf("EvHookFailed from running: to=%v ok=%v err=%v", to, ok, err)
	}

	// hook_failed -> running (resolved)
	_, to, _, ok, err = fsm.Apply("s1", EvHookResolved, GuardCtx{}, "user retried")
	if err != nil || !ok || to != state.StatusRunning {
		t.Fatalf("EvHookResolved from hook_failed: to=%v ok=%v err=%v", to, ok, err)
	}

	// EvHookFailed from done should be rejected (not in the From list)
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusRunning, To: state.StatusDone, Event: "test_setup"}); err != nil {
		t.Fatalf("setup transition to done: %v", err)
	}
	_, _, _, ok, err = fsm.Apply("s1", EvHookFailed, GuardCtx{}, "after hook failed")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("EvHookFailed should not apply from done (after-hook failures don't use the FSM)")
	}
}

// setStatus forces stage "a" into status s via a raw store transition (bypassing FSM rules), so the
// invariant tests below can start from a terminal status.
func setStatus(t *testing.T, store *state.Store, s state.StageStatus) {
	t.Helper()
	if err := store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: s, Event: "test_setup"}); err != nil {
		t.Fatalf("setup to %s: %v", s, err)
	}
}

// TestFSM_DoneIsAbsoluteSink encodes the real terminal contract for done: NO event in the rule table can
// move a stage out of done. Guards against a future edit that adds StatusDone to any rule's From set —
// that would reintroduce the "a Done stage becomes not-Done" regression this fix targets.
func TestFSM_DoneIsAbsoluteSink(t *testing.T) {
	for ev := range NewFSM(nil).rules {
		t.Run(string(ev), func(t *testing.T) {
			fsm, store := newTestFSM(t, []string{"a"})
			defer store.Close()
			setStatus(t, store, state.StatusDone)

			from, to, _, ok, err := fsm.Apply("a", ev, GuardCtx{Phase: "implementation"}, "")
			if err != nil {
				t.Fatalf("Apply(%s) from done: %v", ev, err)
			}
			if ok {
				t.Errorf("event %s applied from done (to=%v): done must be an absolute terminal sink", ev, to)
			}
			if from != state.StatusDone || store.Get("a") != state.StatusDone {
				t.Errorf("event %s mutated a done stage: from=%v live=%v", ev, from, store.Get("a"))
			}
		})
	}
}

// TestFSM_FailedTerminalExceptManualRetry encodes the asymmetry: failed is terminal for every event EXCEPT
// EvManualRetry, whose failed -> pending contract is the one intended way out. Guards against anyone adding
// StatusFailed to another rule's From, or dropping the manual-retry escape hatch.
func TestFSM_FailedTerminalExceptManualRetry(t *testing.T) {
	for ev := range NewFSM(nil).rules {
		t.Run(string(ev), func(t *testing.T) {
			fsm, store := newTestFSM(t, []string{"a"})
			defer store.Close()
			setStatus(t, store, state.StatusFailed)

			_, to, _, ok, err := fsm.Apply("a", ev, GuardCtx{Phase: "implementation"}, "")
			if err != nil {
				t.Fatalf("Apply(%s) from failed: %v", ev, err)
			}
			if ev == EvManualRetry {
				if !ok || to != state.StatusPending {
					t.Errorf("EvManualRetry from failed: got (%v, %v), want (pending, true)", to, ok)
				}
				return
			}
			if ok {
				t.Errorf("event %s applied from failed (to=%v): failed is terminal except EvManualRetry", ev, to)
			}
		})
	}
}

// TestRuleAllowsFrom_TerminalGuardsNilFrom locks the EvFail From:nil guard: ruleAllowsFrom with an empty
// allow-set must still refuse both terminal statuses, so EvFail (and any future From:nil event) can never
// fire from done/failed.
func TestRuleAllowsFrom_TerminalGuardsNilFrom(t *testing.T) {
	if ruleAllowsFrom(nil, state.StatusDone) {
		t.Error("ruleAllowsFrom(nil, done) = true, want false (EvFail must not fire from done)")
	}
	if ruleAllowsFrom(nil, state.StatusFailed) {
		t.Error("ruleAllowsFrom(nil, failed) = true, want false (EvFail must not fire from failed)")
	}
}

func newTestFSMRapid(t *rapid.T, stages []string) (*FSM, *state.Store) {
	dir, err := os.MkdirTemp("", "fsm-rapid-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	store, err := state.Open(filepath.Join(dir, "run"), stages)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	return NewFSM(store), store
}

func TestFSM_Apply_Pause(t *testing.T) {
	for _, from := range []state.StageStatus{state.StatusRunning, state.StatusPlanning, state.StatusRevising, state.StatusRetrying} {
		fsm, store := newTestFSM(t, []string{"a"})
		_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: from, Event: "test_setup"})

		_, to, _, ok, err := fsm.Apply("a", EvPause, GuardCtx{}, "manual pause")
		store.Close()
		if err != nil {
			t.Fatalf("%s: Apply: %v", from, err)
		}
		if !ok || to != state.StatusPaused {
			t.Errorf("%s->paused: got (%v, %v), want (paused, true)", from, to, ok)
		}
	}
}

func TestFSM_Apply_Pause_IllegalFromAwaitingApproval(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusAwaitingApproval, Event: "test_setup"})

	_, _, _, ok, err := fsm.Apply("a", EvPause, GuardCtx{}, "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if ok {
		t.Error("pause from awaiting_approval: ok = true, want false")
	}
}

func TestFSM_Apply_Continue_ResumesToPausedFrom(t *testing.T) {
	for _, pausedFrom := range []state.StageStatus{state.StatusRunning, state.StatusPlanning, state.StatusRevising, state.StatusRetrying, state.StatusPending} {
		fsm, store := newTestFSM(t, []string{"a"})
		_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusPaused, Event: "test_setup"})

		_, to, _, ok, err := fsm.Apply("a", EvContinue, GuardCtx{PausedFrom: pausedFrom}, "")
		store.Close()
		if err != nil {
			t.Fatalf("%s: Apply: %v", pausedFrom, err)
		}
		if !ok || to != pausedFrom {
			t.Errorf("continue with PausedFrom=%s: got (%v, %v), want (%s, true)", pausedFrom, to, ok, pausedFrom)
		}
	}
}

func TestFSM_Apply_Continue_IllegalFromRunning(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"})

	_, _, _, ok, err := fsm.Apply("a", EvContinue, GuardCtx{PausedFrom: state.StatusRunning}, "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if ok {
		t.Error("continue from running (not paused): ok = true, want false")
	}
}

func TestFSM_Apply_VerifyFail_LegalFromActiveStatuses(t *testing.T) {
	for _, from := range []state.StageStatus{
		state.StatusRunning, state.StatusAwaitingApproval, state.StatusRetrying, state.StatusAwaitingUserInput,
	} {
		fsm, store := newTestFSM(t, []string{"a"})
		_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: from, Event: "test_setup"})

		_, to, _, ok, err := fsm.Apply("a", EvVerifyFail, GuardCtx{}, "verify diagnosis")
		store.Close()
		if err != nil {
			t.Fatalf("%s: Apply: %v", from, err)
		}
		if !ok || to != state.StatusFailed {
			t.Errorf("%s->failed: got (%v, %v), want (failed, true)", from, to, ok)
		}
	}
}

func TestFSM_EnterAwaitingAssistantsOnlyFromAllowedStates(t *testing.T) {
	for _, from := range []state.StageStatus{
		state.StatusRunning, state.StatusRetrying, state.StatusAwaitingUserInput,
	} {
		fsm, store := newTestFSM(t, []string{"a"})
		_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: from, Event: "test_setup"})

		_, to, _, ok, err := fsm.Apply("a", EvAwaitAssistants, GuardCtx{}, "required task open")
		store.Close()
		if err != nil {
			t.Fatalf("%s: Apply: %v", from, err)
		}
		if !ok || to != state.StatusAwaitingAssistants {
			t.Errorf("%s->awaiting_assistants: got (%v, %v), want (awaiting_assistants, true)", from, to, ok)
		}
	}

	// Planning/AwaitingApproval belong to planning-only completion, which the barrier never touches
	// (TestPlanningOnlyStageUnaffected) — must stay illegal.
	for _, from := range []state.StageStatus{
		state.StatusPending, state.StatusPlanning, state.StatusAwaitingApproval, state.StatusReady,
		state.StatusRevising, state.StatusPaused, state.StatusDone, state.StatusFailed, state.StatusHookFailed,
	} {
		fsm, store := newTestFSM(t, []string{"a"})
		_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: from, Event: "test_setup"})

		_, to, _, ok, err := fsm.Apply("a", EvAwaitAssistants, GuardCtx{}, "required task open")
		store.Close()
		if err != nil {
			t.Fatalf("%s: Apply: %v", from, err)
		}
		if ok {
			t.Errorf("await_assistants from %s: ok = true, want false", from)
		}
		if to != from {
			t.Errorf("await_assistants from %s: to = %v, want unchanged %v", from, to, from)
		}
	}
}

// TestFSM_CasRejectsStaleTransition mirrors EvVerifyFail's own race-closing test (fsm.go's doc comment): From
// is an explicit allowlist, not nil — a concurrent Pause/Revise that already moved the stage away from one of
// EvAwaitAssistants's legal source statuses must win, and the rejected Apply must report the CURRENT status
// unchanged, never a stale "to".
func TestFSM_CasRejectsStaleTransition(t *testing.T) {
	for _, from := range []state.StageStatus{state.StatusPaused, state.StatusRevising} {
		fsm, store := newTestFSM(t, []string{"a"})
		_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: from, Event: "test_setup"})

		_, to, _, ok, err := fsm.Apply("a", EvAwaitAssistants, GuardCtx{}, "stale: already paused/revising")
		store.Close()
		if err != nil {
			t.Fatalf("%s: Apply: %v", from, err)
		}
		if ok {
			t.Errorf("await_assistants from %s: ok = true, want false (a concurrent pause/revise must win)", from)
		}
		if to != from {
			t.Errorf("await_assistants from %s: to = %v, want unchanged %v", from, to, from)
		}
	}
}

func TestFSM_Apply_VerifyFail_IllegalFromPausedOrRevising(t *testing.T) {
	for _, from := range []state.StageStatus{state.StatusPaused, state.StatusRevising} {
		fsm, store := newTestFSM(t, []string{"a"})
		_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: from, Event: "test_setup"})

		_, to, _, ok, err := fsm.Apply("a", EvVerifyFail, GuardCtx{}, "stale verify diagnosis")
		store.Close()
		if err != nil {
			t.Fatalf("%s: Apply: %v", from, err)
		}
		if ok {
			t.Errorf("verify_fail from %s: ok = true, want false (a concurrent pause/revise must win)", from)
		}
		if to != from {
			t.Errorf("verify_fail from %s: to = %v, want unchanged %v (rejected transition must not report a new status)", from, to, from)
		}
	}
}

// TestFSM_Apply_AfterStoreClosed_IsBenignNotFatal — Этап 10 (T10.5): a transition reaching FSM.Apply AFTER
// the store has already been closed (the run is winding down) must be classified EXACTLY like a benign
// ErrConcurrentChange CAS-miss (ok=false, err=nil) — never wrapped as a fatal *StorageError. Before this
// stage, Store.Apply had no eventsLog==nil guard of its own (unlike ApplyRunOperation/AddStageToGraph), so
// this exact scenario produced a confusing "write events.jsonl: invalid argument" that WAS wrapped as fatal.
func TestFSM_Apply_AfterStoreClosed_IsBenignNotFatal(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, _, _, ok, err := fsm.Apply("a", EvReady, GuardCtx{}, "late scheduler wake after shutdown")
	if err != nil {
		t.Fatalf("Apply after store close: err = %v, want nil (benign, like ErrConcurrentChange)", err)
	}
	if ok {
		t.Fatal("Apply after store close: ok = true, want false")
	}
	var se *StorageError
	if errors.As(err, &se) {
		t.Fatalf("Apply after store close must NOT be a fatal StorageError: %v", err)
	}
}

// parkInQuota parks stage "a" (already running) in awaiting_quota via the FSM and returns the park's seq.
func parkInQuota(t *testing.T, fsm *FSM, quota *state.QuotaWait) uint64 {
	t.Helper()
	_, to, seq, ok, err := fsm.Apply("a", EvAwaitQuota, GuardCtx{Quota: quota}, "usage limit")
	if err != nil || !ok || to != state.StatusAwaitingQuota {
		t.Fatalf("park: got (%v, %v, %v), want (awaiting_quota, true, nil)", to, ok, err)
	}
	return seq
}

func TestFSM_AwaitQuota_ReachableFromTheRetryPhases(t *testing.T) {
	retryFrom := []state.StageStatus{state.StatusPlanning, state.StatusRunning, state.StatusAwaitingUserInput, state.StatusAwaitingPeer}
	// EvAwaitQuota covers everything EvScheduleRetry does, plus retrying itself (a usage limit hit by a retry attempt).
	from := append([]state.StageStatus{state.StatusRetrying}, retryFrom...)
	for _, status := range from {
		fsm, store := newTestFSM(t, []string{"a"})
		setStatus(t, store, status)
		_, to, _, ok, err := fsm.Apply("a", EvAwaitQuota, GuardCtx{Quota: &state.QuotaWait{Provider: "claude"}}, "")
		store.Close()
		if err != nil || !ok || to != state.StatusAwaitingQuota {
			t.Errorf("%s -> awaiting_quota: got (%v, %v, %v)", status, to, ok, err)
		}
	}
	for _, status := range []state.StageStatus{state.StatusPending, state.StatusReady, state.StatusPaused, state.StatusDone, state.StatusFailed, state.StatusAwaitingQuota, state.StatusAwaitingAssistants} {
		fsm, store := newTestFSM(t, []string{"a"})
		setStatus(t, store, status)
		_, _, _, ok, err := fsm.Apply("a", EvAwaitQuota, GuardCtx{Quota: &state.QuotaWait{Provider: "claude"}}, "")
		store.Close()
		if err != nil || ok {
			t.Errorf("%s -> awaiting_quota: ok=%v err=%v, want refused", status, ok, err)
		}
	}
}

func TestFSM_AwaitQuota_RecordsQuotaInTheJournal(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	setStatus(t, store, state.StatusRunning)
	q := &state.QuotaWait{Provider: "codex", Source: state.QuotaSourceText, Waits: 1}
	parkInQuota(t, fsm, q)
	got := store.Snapshot().Stages["a"].Quota
	if got == nil || got.Provider != "codex" || got.Waits != 1 {
		t.Fatalf("stage quota = %+v, want the park's metadata", got)
	}
	hist, err := store.History()
	if err != nil {
		t.Fatal(err)
	}
	last := hist[len(hist)-1]
	if last.Event != "await_quota" || last.Quota == nil || last.Quota.Provider != "codex" {
		t.Fatalf("journal tail = %+v, want await_quota with quota", last)
	}
}

func TestFSM_QuotaReset_DispatchesToThePhase(t *testing.T) {
	for _, tc := range []struct {
		phase string
		want  state.StageStatus
	}{
		{phasePlanning, state.StatusPlanning},
		{string(flow.PhaseImplementation), state.StatusRunning},
		{"", state.StatusRunning},
	} {
		fsm, store := newTestFSM(t, []string{"a"})
		setStatus(t, store, state.StatusRunning)
		parkInQuota(t, fsm, &state.QuotaWait{Provider: "claude"})
		_, to, _, ok, err := fsm.Apply("a", EvQuotaReset, GuardCtx{Phase: tc.phase}, "")
		store.Close()
		if err != nil || !ok || to != tc.want {
			t.Errorf("phase %q: quota_reset = (%v, %v, %v), want (%s, true, nil)", tc.phase, to, ok, err, tc.want)
		}
	}
}

func TestFSM_QuotaReset_RefusedOutsideTheWait(t *testing.T) {
	for _, status := range []state.StageStatus{state.StatusRunning, state.StatusPaused, state.StatusRetrying, state.StatusAwaitingPeer} {
		fsm, store := newTestFSM(t, []string{"a"})
		setStatus(t, store, status)
		_, _, _, ok, err := fsm.Apply("a", EvQuotaReset, GuardCtx{}, "")
		store.Close()
		if err != nil || ok {
			t.Errorf("quota_reset from %s: ok=%v err=%v, want refused", status, ok, err)
		}
	}
}

func TestFSM_Quota_PauseAndFailFromTheWait(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	setStatus(t, store, state.StatusRunning)
	parkInQuota(t, fsm, &state.QuotaWait{Provider: "claude", Waits: 2})
	_, to, _, ok, err := fsm.Apply("a", EvPause, GuardCtx{}, "manual")
	if err != nil || !ok || to != state.StatusPaused {
		t.Fatalf("pause from the wait = (%v, %v, %v)", to, ok, err)
	}
	if got := store.PausedFrom("a"); got != state.StatusAwaitingQuota {
		t.Fatalf("PausedFrom = %q, want awaiting_quota", got)
	}
	// Continue returns to the park; the caller decides the manual probe via GuardCtx.Quota.
	probe := &state.QuotaWait{Provider: "claude", ManualProbe: true}
	_, to, _, ok, err = fsm.Apply("a", EvContinue, GuardCtx{PausedFrom: state.StatusAwaitingQuota, Quota: probe}, "")
	if err != nil || !ok || to != state.StatusAwaitingQuota {
		t.Fatalf("continue = (%v, %v, %v), want back to awaiting_quota", to, ok, err)
	}
	if q := store.Snapshot().Stages["a"].Quota; q == nil || !q.ManualProbe {
		t.Fatalf("quota after continue = %+v, want the manual probe", q)
	}
	_, to, _, ok, err = fsm.Apply("a", EvFail, GuardCtx{}, "cancelled")
	store.Close()
	if err != nil || !ok || to != state.StatusFailed {
		t.Fatalf("fail (cancel) from the wait = (%v, %v, %v)", to, ok, err)
	}
}

// Revise and a direct Continue stay refused while the stage waits: Pause first.
func TestFSM_Quota_ReviseAndContinueRefusedInTheWait(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	setStatus(t, store, state.StatusRunning)
	parkInQuota(t, fsm, &state.QuotaWait{Provider: "claude"})
	for _, ev := range []FSMEvent{EvRevise, EvContinue, EvComplete, EvVerifyFail, EvAwaitAssistants} {
		_, _, _, ok, err := fsm.Apply("a", ev, GuardCtx{PausedFrom: state.StatusAwaitingQuota}, "")
		if err != nil || ok {
			t.Errorf("%s in awaiting_quota: ok=%v err=%v, want refused", ev, ok, err)
		}
	}
}

func TestFSM_Quota_GuardsAreCheckedInStoreApply(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	setStatus(t, store, state.StatusRunning)
	staleEpoch := store.Snapshot().Stages["a"].Epoch
	firstPark := parkInQuota(t, fsm, &state.QuotaWait{Provider: "claude"})
	for _, ev := range []FSMEvent{EvPause, EvContinue} {
		if _, _, _, ok, err := fsm.Apply("a", ev, GuardCtx{PausedFrom: state.StatusAwaitingQuota}, ""); err != nil || !ok {
			t.Fatalf("%s: ok=%v err=%v", ev, ok, err)
		}
	}
	// The timer of the first park cannot move the stage that was paused and continued into a new park.
	if _, _, _, ok, err := fsm.Apply("a", EvQuotaReset, GuardCtx{ExpectStageSeq: &firstPark}, ""); err != nil || ok {
		t.Fatalf("timer of an earlier park: ok=%v err=%v, want refused", ok, err)
	}
	currentPark := store.Snapshot().Stages["a"].Seq
	if _, _, _, ok, err := fsm.Apply("a", EvQuotaReset, GuardCtx{ExpectStageSeq: &currentPark}, ""); err != nil || !ok {
		t.Fatalf("timer of the current park: ok=%v err=%v, want applied", ok, err)
	}
	// A stale author (older epoch) cannot park the stage again.
	if _, _, _, ok, err := fsm.Apply("a", EvAwaitQuota, GuardCtx{ExpectEpoch: &staleEpoch, Quota: &state.QuotaWait{Provider: "claude"}}, ""); err != nil || ok {
		t.Fatalf("stale author park: ok=%v err=%v, want refused", ok, err)
	}
}
