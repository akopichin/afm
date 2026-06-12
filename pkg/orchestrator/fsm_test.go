package orchestrator

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
			_ = store.Apply(state.Transition{StageID: "a", From: state.StatusPending, To: tc.from, Event: "test_setup"})

			to, ok, err := fsm.Apply("a", tc.event, GuardCtx{}, "")
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
	_ = store.Apply(state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusDone, Event: "test_setup"})

	_, ok, err := fsm.Apply("a", EvStartPlanning, GuardCtx{}, "")
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
	_ = store.Apply(state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusRetrying, Event: "test_setup"})

	to, ok, err := fsm.Apply("a", EvStartPlanning, GuardCtx{}, "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok || to != state.StatusPlanning {
		t.Errorf("retrying->planning: got (%v, %v), want (planning, true)", to, ok)
	}
}

func TestFSM_Apply_AskUser(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"})

	to, ok, err := fsm.Apply("a", EvAskUser, GuardCtx{Phase: "implementation"}, "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok || to != state.StatusAwaitingUserInput {
		t.Errorf("running->awaiting_user_input: got (%v, %v), want (awaiting_user_input, true)", to, ok)
	}
}

func TestFSM_PhaseDispatch_UserAnswered(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusAwaitingUserInput, Event: "test_setup"})

	to, ok, err := fsm.Apply("a", EvUserAnswered, GuardCtx{Phase: "planning"}, "")
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
	_ = store.Apply(state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusRetrying, Event: "test_setup"})

	to, ok, err := fsm.Apply("a", EvResumeAfterRetry, GuardCtx{Phase: "implementation"}, "")
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
			_, _, _ = fsm.Apply("a", ev, GuardCtx{Phase: "implementation"}, "")
			if IsTerminal(store.Get("a")) {
				return
			}
		}
		t.Errorf("did not reach terminal in %d steps; last status: %q", maxSteps, store.Get("a"))
	})
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
