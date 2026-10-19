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

// Each stage remembers the seq of its latest transition — a park's identity while it stays parked — and a
// transition fenced with that seq commits only if nothing has moved the stage since.
func TestStageSeq_IdentifiesTheLatestTransition(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, []string{"s"})
	if err != nil {
		t.Fatal(err)
	}
	applyTr(t, s, StatusPending, StatusRunning, "start_run")
	applyTr(t, s, StatusRunning, StatusAwaitingPeer, "await_peer")
	park1 := s.Snapshot().Stages["s"].Seq
	if park1 == 0 {
		t.Fatal("the park has no seq")
	}
	applyTr(t, s, StatusAwaitingPeer, StatusRunning, "peer_replied")
	applyTr(t, s, StatusRunning, StatusAwaitingPeer, "await_peer")
	park2 := s.Snapshot().Stages["s"].Seq

	err = s.Apply(&Transition{StageID: "s", From: StatusAwaitingPeer, To: StatusRunning, Event: "peer_question", ExpectStageSeq: &park1})
	if !errors.Is(err, ErrConcurrentChange) {
		t.Fatalf("resume fenced to an earlier park: err = %v, want ErrConcurrentChange", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.Stages["s"].Seq; got != park2 {
		t.Fatalf("replayed seq = %d, want %d", got, park2)
	}
}

// A paused park keeps its episode, and Continue back into awaiting_peer restores the same waits, so the
// continuation is decided on them again.
func TestStagePeerWaits_SurvivePauseAndContinue(t *testing.T) {
	s, err := Open(t.TempDir(), []string{"s"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	applyTr(t, s, StatusPending, StatusRunning, "start_run")
	if err := s.Apply(&Transition{StageID: "s", From: StatusRunning, To: StatusAwaitingPeer, Event: "await_peer", PeerWaits: []agentcomm.WaitID{"w1"}}); err != nil {
		t.Fatal(err)
	}
	applyTr(t, s, StatusAwaitingPeer, StatusPaused, "pause")
	if got := s.Snapshot().Stages["s"].PeerWaits; len(got) != 1 {
		t.Fatalf("paused park's waits = %v, want w1", got)
	}
	applyTr(t, s, StatusPaused, StatusAwaitingPeer, "continue")
	if got := s.Snapshot().Stages["s"].PeerWaits; len(got) != 1 || got[0] != "w1" {
		t.Fatalf("continued park's waits = %v, want w1", got)
	}
	applyTr(t, s, StatusAwaitingPeer, StatusRunning, "peer_replied")
	if got := s.Snapshot().Stages["s"].PeerWaits; got != nil {
		t.Fatalf("waits after the resume = %v, want none", got)
	}
}
