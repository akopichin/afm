package bus

import (
	"testing"

	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 5, T5.1: the awaiting_peer status and its FSM edges — the peer twin of
// awaiting_user_input/EvAskUser/EvUserAnswered.

func TestFSM_Apply_AwaitPeer_FromRunningAndRetrying(t *testing.T) {
	for _, from := range []state.StageStatus{state.StatusRunning, state.StatusRetrying, state.StatusPlanning, state.StatusRevising} {
		fsm, store := newTestFSM(t, []string{"a"})
		_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: from, Event: "test_setup"})

		_, to, _, ok, err := fsm.Apply("a", EvAwaitPeer, GuardCtx{Phase: "implementation"}, "")
		store.Close()
		if err != nil {
			t.Fatalf("%s: Apply: %v", from, err)
		}
		if !ok || to != state.StatusAwaitingPeer {
			t.Errorf("%s->awaiting_peer: got (%v, %v), want (awaiting_peer, true)", from, to, ok)
		}
	}
}

func TestFSM_PhaseDispatch_PeerReplied(t *testing.T) {
	for _, tc := range []struct {
		phase  string
		wantTo state.StageStatus
	}{
		{"planning", state.StatusPlanning},
		{"implementation", state.StatusRunning},
	} {
		fsm, store := newTestFSM(t, []string{"a"})
		_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusAwaitingPeer, Event: "test_setup"})

		_, to, _, ok, err := fsm.Apply("a", EvPeerReplied, GuardCtx{Phase: tc.phase}, "")
		store.Close()
		if err != nil {
			t.Fatalf("%s: Apply: %v", tc.phase, err)
		}
		if !ok || to != tc.wantTo {
			t.Errorf("%s phase: got (%v, %v), want (%v, true)", tc.phase, to, ok, tc.wantTo)
		}
	}
}

func TestFSM_AwaitingPeer_IsPausable(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusAwaitingPeer, Event: "test_setup"})

	_, to, _, ok, err := fsm.Apply("a", EvPause, GuardCtx{}, "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok || to != state.StatusPaused {
		t.Errorf("awaiting_peer->paused: got (%v, %v), want (paused, true)", to, ok)
	}
}

func TestFSM_AwaitingPeer_IsNotTerminal(t *testing.T) {
	if IsTerminal(state.StatusAwaitingPeer) {
		t.Fatal("awaiting_peer must not be terminal")
	}
}

func TestFSM_AwaitingPeer_CanComplete(t *testing.T) {
	// Defensive inclusion mirroring awaiting_user_input: the question/wait poller may have raced a
	// legitimate completion signal.
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusAwaitingPeer, Event: "test_setup"})

	_, to, _, ok, err := fsm.Apply("a", EvComplete, GuardCtx{}, "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok || to != state.StatusDone {
		t.Errorf("awaiting_peer->done: got (%v, %v), want (done, true)", to, ok)
	}
}
