package bus

import (
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

			to, _, ok, err := fsm.Apply("a", tc.event, GuardCtx{}, "")
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

	_, _, ok, err := fsm.Apply("a", EvStartPlanning, GuardCtx{}, "")
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

	to, _, ok, err := fsm.Apply("a", EvStartPlanning, GuardCtx{}, "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok || to != state.StatusPlanning {
		t.Errorf("retrying->planning: got (%v, %v), want (planning, true)", to, ok)
	}
}

// TestFSM_RetryFromAwaitingUserInput закрывает Finding #5: поллер вопросов мог
// перевести стадию Running→AwaitingUserInput из-за брошенного вопроса, пока агент
// ещё выполнялся; затем агент возвращал ретраибельную ошибку (529). Раньше
// EvScheduleRetry (From={Planning,Running}) и EvResumeAfterRetry (From={Retrying})
// молча отбрасывались из AwaitingUserInput, и всё время бэкоффа стадия показывала
// неверный awaiting_user_input вместо retrying (и полагалась на fallthrough
// повторного запуска агента + AwaitingUserInput-приём в EvComplete — хрупко). Оба
// события теперь принимают AwaitingUserInput как исходный статус.
func TestFSM_RetryFromAwaitingUserInput(t *testing.T) {
	t.Run("EvScheduleRetry: awaiting_user_input->retrying", func(t *testing.T) {
		fsm, store := newTestFSM(t, []string{"a"})
		defer store.Close()
		_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusAwaitingUserInput, Event: "test_setup"})

		to, _, ok, err := fsm.Apply("a", EvScheduleRetry, GuardCtx{Phase: string(flow.PhaseImplementation)}, "")
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

		to, _, ok, err := fsm.Apply("a", EvResumeAfterRetry, GuardCtx{Phase: string(flow.PhaseImplementation)}, "")
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

	to, _, ok, err := fsm.Apply("a", EvRevise, GuardCtx{}, "feedback text")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok || to != state.StatusRevising {
		t.Errorf("running->revising: got (%v, %v), want (revising, true)", to, ok)
	}
}

func TestFSM_Apply_AskUser(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"})

	to, _, ok, err := fsm.Apply("a", EvAskUser, GuardCtx{Phase: "implementation"}, "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok || to != state.StatusAwaitingUserInput {
		t.Errorf("running->awaiting_user_input: got (%v, %v), want (awaiting_user_input, true)", to, ok)
	}
}

// TestFSM_Apply_AskUser_FromRetryAndRevising locks in the fix for the question
// poller scanning retrying/revising stages: EvAskUser must transition them to
// awaiting_user_input, otherwise a question asked mid-retry/mid-revision never
// reaches the awaiting state and the answer is lost.
func TestFSM_Apply_AskUser_FromRetryAndRevising(t *testing.T) {
	for _, from := range []state.StageStatus{state.StatusRetrying, state.StatusRevising} {
		fsm, store := newTestFSM(t, []string{"a"})
		_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: from, Event: "test_setup"})

		to, _, ok, err := fsm.Apply("a", EvAskUser, GuardCtx{Phase: "implementation"}, "")
		store.Close()
		if err != nil {
			t.Fatalf("%s: Apply: %v", from, err)
		}
		if !ok || to != state.StatusAwaitingUserInput {
			t.Errorf("%s->awaiting_user_input: got (%v, %v), want (awaiting_user_input, true)", from, to, ok)
		}
	}
}

func TestFSM_PhaseDispatch_UserAnswered(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusAwaitingUserInput, Event: "test_setup"})

	to, _, ok, err := fsm.Apply("a", EvUserAnswered, GuardCtx{Phase: "planning"}, "")
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

	to, _, ok, err := fsm.Apply("a", EvResumeAfterRetry, GuardCtx{Phase: "implementation"}, "")
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
			_, _, _, _ = fsm.Apply("a", ev, GuardCtx{Phase: "implementation"}, "")
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
	to, _, ok, err := fsm.Apply("s1", EvHookFailed, GuardCtx{}, "before hook failed")
	if err != nil || !ok || to != state.StatusHookFailed {
		t.Fatalf("EvHookFailed from running: to=%v ok=%v err=%v", to, ok, err)
	}

	// hook_failed -> running (resolved)
	to, _, ok, err = fsm.Apply("s1", EvHookResolved, GuardCtx{}, "user retried")
	if err != nil || !ok || to != state.StatusRunning {
		t.Fatalf("EvHookResolved from hook_failed: to=%v ok=%v err=%v", to, ok, err)
	}

	// EvHookFailed from done should be rejected (not in the From list)
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusRunning, To: state.StatusDone, Event: "test_setup"}); err != nil {
		t.Fatalf("setup transition to done: %v", err)
	}
	_, _, ok, err = fsm.Apply("s1", EvHookFailed, GuardCtx{}, "after hook failed")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("EvHookFailed should not apply from done (after-hook failures don't use the FSM)")
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

		to, _, ok, err := fsm.Apply("a", EvPause, GuardCtx{}, "manual pause")
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

	_, _, ok, err := fsm.Apply("a", EvPause, GuardCtx{}, "")
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

		to, _, ok, err := fsm.Apply("a", EvContinue, GuardCtx{PausedFrom: pausedFrom}, "")
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

	_, _, ok, err := fsm.Apply("a", EvContinue, GuardCtx{PausedFrom: state.StatusRunning}, "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if ok {
		t.Error("continue from running (not paused): ok = true, want false")
	}
}
