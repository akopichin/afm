package state

import (
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
)

func applyTr(t *testing.T, s *Store, from, to StageStatus, event string) {
	t.Helper()
	if err := s.Apply(&Transition{StageID: "s", From: from, To: to, Event: event}); err != nil {
		t.Fatalf("Apply %s→%s (%s): %v", from, to, event, err)
	}
}

// The stage's execution epoch changes only when someone outside the running process takes the stage over or
// hands it to a new process (pause, continue, revise) — never on the process's own transitions — and replay
// derives the same value.
func TestStageEpoch_CountsTakeoversAndReplays(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, []string{"s"})
	if err != nil {
		t.Fatal(err)
	}
	applyTr(t, s, StatusPending, StatusRunning, "start_run")
	applyTr(t, s, StatusRunning, StatusAwaitingUserInput, "ask_user")
	applyTr(t, s, StatusAwaitingUserInput, StatusRunning, "user_answered")
	if got := s.Snapshot().Stages["s"].Epoch; got != 0 {
		t.Fatalf("epoch after the process's own transitions = %d, want 0", got)
	}
	applyTr(t, s, StatusRunning, StatusPaused, "pause")
	applyTr(t, s, StatusPaused, StatusRunning, "continue")
	applyTr(t, s, StatusRunning, StatusRevising, "revise")
	if got := s.Snapshot().Stages["s"].Epoch; got != 3 {
		t.Fatalf("epoch after pause, continue, revise = %d, want 3", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.Stages["s"].Epoch; got != 3 {
		t.Fatalf("replayed epoch = %d, want 3", got)
	}
}

// A transition fenced with an expected epoch commits only while the stage is still in that epoch.
func TestStageEpoch_FencedTransition(t *testing.T) {
	s, err := Open(t.TempDir(), []string{"s"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	applyTr(t, s, StatusPending, StatusRunning, "start_run")
	stale := s.Snapshot().Stages["s"].Epoch
	applyTr(t, s, StatusRunning, StatusPaused, "pause")
	applyTr(t, s, StatusPaused, StatusRunning, "continue")

	err = s.Apply(&Transition{StageID: "s", From: StatusRunning, To: StatusAwaitingPeer, Event: "await_peer", ExpectEpoch: &stale})
	if !errors.Is(err, ErrConcurrentChange) {
		t.Fatalf("fenced transition from a stale epoch: err = %v, want ErrConcurrentChange", err)
	}
	current := s.Snapshot().Stages["s"].Epoch
	if err := s.Apply(&Transition{StageID: "s", From: StatusRunning, To: StatusAwaitingPeer, Event: "await_peer", ExpectEpoch: &current}); err != nil {
		t.Fatalf("fenced transition in the current epoch: %v", err)
	}
}

// A park records the waits its continuation depends on; the next transition ends that episode. Replay
// restores both.
func TestStagePeerWaits_RecordedOnParkAndReplayed(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, []string{"s"})
	if err != nil {
		t.Fatal(err)
	}
	applyTr(t, s, StatusPending, StatusRunning, "start_run")
	waits := []agentcomm.WaitID{"w1", "w2"}
	if err := s.Apply(&Transition{StageID: "s", From: StatusRunning, To: StatusAwaitingPeer, Event: "await_peer", PeerWaits: waits}); err != nil {
		t.Fatal(err)
	}
	if got := s.Snapshot().Stages["s"].PeerWaits; len(got) != 2 || got[0] != "w1" || got[1] != "w2" {
		t.Fatalf("PeerWaits = %v, want %v", got, waits)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.Stages["s"].PeerWaits; len(got) != 2 {
		t.Fatalf("replayed PeerWaits = %v, want %v", got, waits)
	}

	s2, err := Open(dir, []string{"s"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s2.Close() })
	applyTr(t, s2, StatusAwaitingPeer, StatusRunning, "peer_replied")
	if got := s2.Snapshot().Stages["s"].PeerWaits; got != nil {
		t.Fatalf("PeerWaits after the resume = %v, want none", got)
	}
}
