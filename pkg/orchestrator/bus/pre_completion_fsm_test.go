package bus

import (
	"testing"

	"github.com/akopichin/afm/pkg/state"
)

func TestFSM_PreCompletionRoundEntersFromAuthorTurnOnly(t *testing.T) {
	enter := map[state.StageStatus]bool{state.StatusRunning: true, state.StatusRetrying: true, state.StatusAwaitingUserInput: true}
	for _, from := range state.AllStatuses() {
		fsm, store := newTestFSM(t, []string{"a"})
		_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: from, Event: "test_setup"})
		_, to, _, ok, err := fsm.Apply("a", EvAwaitPreCompletion, GuardCtx{PreCompletion: &state.PreCompletionStep{Phase: "implementation"}}, "")
		store.Close()
		if err != nil {
			t.Fatalf("%s: Apply: %v", from, err)
		}
		if ok != enter[from] {
			t.Errorf("await_pre_completion from %s: ok = %v, want %v", from, ok, enter[from])
		}
		if ok && to != state.StatusAwaitingPreCompletion {
			t.Errorf("await_pre_completion from %s: to = %s", from, to)
		}
	}
}

func TestFSM_AwaitingPreCompletionLeavesOnlyByDecisionPauseReviseFail(t *testing.T) {
	allowed := map[FSMEvent]state.StageStatus{
		EvPreCompletionDecided: state.StatusRunning,
		EvPause:                state.StatusPaused,
		EvRevise:               state.StatusRevising,
		EvFail:                 state.StatusFailed,
	}
	events := []FSMEvent{EvComplete, EvVerifyFail, EvAskUser, EvAwaitAssistants, EvAwaitPeer, EvAwaitQuota, EvScheduleRetry,
		EvPreCompletionDecided, EvPause, EvRevise, EvFail}
	for _, ev := range events {
		fsm, store := newTestFSM(t, []string{"a"})
		_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusAwaitingPreCompletion, Event: "test_setup"})
		_, to, _, ok, err := fsm.Apply("a", ev, GuardCtx{PreCompletion: &state.PreCompletionStep{Decision: state.PreCompletionAccepted}}, "")
		store.Close()
		if err != nil {
			t.Fatalf("%s: Apply: %v", ev, err)
		}
		want, isAllowed := allowed[ev]
		if ok != isAllowed || (ok && to != want) {
			t.Errorf("%s from awaiting_pre_completion: (to=%s, ok=%v), want (to=%s, ok=%v)", ev, to, ok, want, isAllowed)
		}
	}
}

func TestFSM_PreCompletionRecordEventsStayInRunning(t *testing.T) {
	for _, ev := range []FSMEvent{EvPreCompletionAcceptEmpty, EvPreCompletionConsumed} {
		for _, from := range []state.StageStatus{state.StatusRunning, state.StatusRetrying, state.StatusAwaitingPreCompletion, state.StatusPaused, state.StatusAwaitingUserInput} {
			fsm, store := newTestFSM(t, []string{"a"})
			_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: from, Event: "test_setup"})
			_, to, _, ok, err := fsm.Apply("a", ev, GuardCtx{PreCompletion: &state.PreCompletionStep{}}, "")
			store.Close()
			if err != nil {
				t.Fatalf("%s from %s: Apply: %v", ev, from, err)
			}
			wantOK := from == state.StatusRunning
			if ev == EvPreCompletionAcceptEmpty && (from == state.StatusAwaitingUserInput || from == state.StatusRetrying) {
				wantOK = true
			}
			if ok != wantOK || (ok && to != from) {
				t.Errorf("%s from %s: (to=%s, ok=%v), want ok=%v in the same status", ev, from, to, ok, wantOK)
			}
		}
	}
}

func TestFSM_PreCompletionStepIsJournaled(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"})
	step := &state.PreCompletionStep{Phase: "implementation", Attempt: 2, VerifyFailures: 1}
	if _, _, _, ok, err := fsm.Apply("a", EvAwaitPreCompletion, GuardCtx{PreCompletion: step}, ""); err != nil || !ok {
		t.Fatalf("await: ok=%v err=%v", ok, err)
	}
	got := store.Snapshot().Stages["a"].PreCompletion
	if got == nil || got.Round != 1 || got.Phase != "implementation" || got.Attempt != 2 || got.VerifyFailures != 1 || !got.Open() {
		t.Fatalf("PreCompletion = %+v, want an open round 1 carrying the counters", got)
	}
}
