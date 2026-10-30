package bus

import (
	"fmt"
	"testing"

	"github.com/akopichin/afm/pkg/state"
)

func forceStatus(t *testing.T, store *state.Store, id string, to state.StageStatus) {
	t.Helper()
	if err := store.Apply(&state.Transition{StageID: id, From: store.Get(id), To: to, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
}

func TestEvSummaryRecover_AllowedOnlyFromOwnerStatuses(t *testing.T) {
	allowed := map[state.StageStatus]bool{
		state.StatusRunning: true, state.StatusRetrying: true, state.StatusAwaitingUserInput: true,
	}
	for _, from := range state.AllStatuses() {
		fsm, store := newTestFSM(t, []string{"a"})
		forceStatus(t, store, "a", from)

		_, to, seq, ok, err := fsm.Apply("a", EvSummaryRecover, GuardCtx{SummaryRecovery: &state.SummaryRecovery{Used: 1, Reason: "missing"}}, "")
		if err != nil {
			t.Fatalf("%s: %v", from, err)
		}
		if ok != allowed[from] {
			t.Errorf("from %s: applied=%v, want %v", from, ok, allowed[from])
		}
		if ok && ((to != state.StatusRunning || store.Get("a") != state.StatusRunning) || seq == 0) {
			t.Errorf("from %s: not a transition to running (to %s, status %s, seq %d)", from, to, store.Get("a"), seq)
		}
		store.Close()
	}
}

func TestEvSummaryRecover_ReserveReturnsNewEpochAndPayload(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	forceStatus(t, store, "a", state.StatusRunning)
	epoch := store.Stage("a").OwnerEpoch

	_, _, seq, ok, err := fsm.Apply("a", EvSummaryRecover, GuardCtx{OwnerEpoch: &epoch, SummaryRecovery: &state.SummaryRecovery{Used: 1, Reason: "empty"}}, "")
	if err != nil || !ok {
		t.Fatalf("reserve: ok=%v err=%v", ok, err)
	}
	got := store.Stage("a")
	if got.OwnerEpoch != seq || got.SummaryRecoveryUsed != 1 || got.SummaryRecoveryReason != "empty" {
		t.Fatalf("state = %+v, want epoch=%d used=1 reason=empty", got, seq)
	}

	// Тот же (теперь устаревший) epoch больше не резервирует и не тратит бюджет.
	_, _, _, ok, err = fsm.Apply("a", EvSummaryRecover, GuardCtx{OwnerEpoch: &epoch, SummaryRecovery: &state.SummaryRecovery{Used: 2, Reason: "empty"}}, "")
	if err != nil || ok {
		t.Fatalf("stale reserve: ok=%v err=%v, want a silent CAS miss", ok, err)
	}
	if used := store.Stage("a").SummaryRecoveryUsed; used != 1 {
		t.Fatalf("used = %d, want 1 after a lost CAS", used)
	}
}

func TestFencedFail_StaleOwnerIsCASMissNotStorageError(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	forceStatus(t, store, "a", state.StatusRunning)
	stale := store.Stage("a").OwnerEpoch
	forceStatus(t, store, "a", state.StatusPaused)
	forceStatus(t, store, "a", state.StatusRunning)

	_, _, _, ok, err := fsm.Apply("a", EvFail, GuardCtx{OwnerEpoch: &stale}, "summary exhausted")
	if err != nil || ok {
		t.Fatalf("stale fenced fail: ok=%v err=%v, want a silent no-op", ok, err)
	}
	if got := store.Get("a"); got != state.StatusRunning {
		t.Fatalf("status = %s, stale author must not fail the stage", got)
	}
	// Без guard старый контракт EvFail не меняется.
	if _, _, _, ok, _ := fsm.Apply("a", EvFail, GuardCtx{}, "x"); !ok {
		t.Fatal("unfenced EvFail must keep working")
	}
}

func TestOwnerEpoch_SameOwnerEventsKeepEpoch(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	forceStatus(t, store, "a", state.StatusRunning)
	epoch := store.Stage("a").OwnerEpoch

	steps := []struct {
		ev    FSMEvent
		phase string
	}{{EvAskUser, phasePlanning}, {EvUserAnswered, ""}, {EvScheduleRetry, ""}, {EvResumeAfterRetry, ""}}
	for _, step := range steps {
		if _, _, _, ok, err := fsm.Apply("a", step.ev, GuardCtx{Phase: step.phase}, ""); err != nil || !ok {
			t.Fatalf("%s: ok=%v err=%v", step.ev, ok, err)
		}
		if got := store.Stage("a").OwnerEpoch; got != epoch {
			t.Fatalf("%s changed the owner epoch: %d → %d", step.ev, epoch, got)
		}
	}
	for _, ev := range []FSMEvent{EvPause, EvContinue} {
		ctx := GuardCtx{PausedFrom: state.StatusRunning}
		if _, _, _, ok, err := fsm.Apply("a", ev, ctx, ""); err != nil || !ok {
			t.Fatalf("%s: ok=%v err=%v", ev, ok, err)
		}
	}
	if got := store.Stage("a").OwnerEpoch; got == epoch {
		t.Fatal("Pause→Continue must change the owner epoch")
	}
}

func TestSummaryBudgetReset_OnReviseAndManualRetry(t *testing.T) {
	for _, tc := range []struct {
		name string
		from state.StageStatus
		ev   FSMEvent
	}{
		{"revise", state.StatusRunning, EvRevise},
		{"manual retry", state.StatusFailed, EvManualRetry},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsm, store := newTestFSM(t, []string{"a"})
			defer store.Close()
			forceStatus(t, store, "a", state.StatusRunning)
			if _, _, _, ok, _ := fsm.Apply("a", EvSummaryRecover, GuardCtx{SummaryRecovery: &state.SummaryRecovery{Used: 2, Reason: "missing"}}, ""); !ok {
				t.Fatal("reserve failed")
			}
			if tc.from != state.StatusRunning {
				forceStatus(t, store, "a", tc.from)
			}
			if _, _, _, ok, err := fsm.Apply("a", tc.ev, GuardCtx{}, ""); err != nil || !ok {
				t.Fatalf("%s: ok=%v err=%v", tc.ev, ok, err)
			}
			if got := store.Stage("a"); got.SummaryRecoveryUsed != 0 || got.SummaryRecoveryReason != "" {
				t.Fatalf("budget not reset by %s: %+v", tc.ev, got)
			}
		})
	}
}

func TestOwnerEpoch_AnswerRestartChangesOwner(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart=%t", restart), func(t *testing.T) {
			fsm, store := newTestFSM(t, []string{"a"})
			defer store.Close()
			forceStatus(t, store, "a", state.StatusRunning)
			epoch := store.Stage("a").OwnerEpoch
			if _, _, _, ok, err := fsm.Apply("a", EvAskUser, GuardCtx{Phase: "autonomous_execution"}, ""); err != nil || !ok {
				t.Fatalf("ask: %v, %v", ok, err)
			}
			if _, _, _, ok, err := fsm.Apply("a", EvUserAnswered, GuardCtx{Phase: "autonomous_execution", NewOwner: restart}, ""); err != nil || !ok {
				t.Fatalf("answer: %v, %v", ok, err)
			}
			st := store.Stage("a")
			if restart {
				if st.OwnerEpoch == epoch || st.PrevOwnerEpoch != epoch {
					t.Fatalf("restart retained old owner: %+v", st)
				}
			} else if st.OwnerEpoch != epoch {
				t.Fatal("answer to live turn changed owner")
			}
		})
	}
}
