package state

import (
	"errors"
	"testing"
)

// Fence проверяется под блокировкой store прямо перед коммитом: false проигрывает CAS и ничего не пишет в журнал.
func TestExitOnFailTransitionFence(t *testing.T) {
	store, err := Open(t.TempDir(), []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	var seen *RunState
	rejecting := &Transition{StageID: "a", From: StatusPending, To: StatusFailed, Event: "fail",
		Fence: func(rs *RunState) bool { seen = rs; return false }}
	if err := store.Apply(rejecting); !errors.Is(err, ErrConcurrentChange) {
		t.Fatalf("Apply with a rejecting fence = %v, want ErrConcurrentChange", err)
	}
	if seen == nil || store.Get("a") != StatusPending || store.Snapshot().LastSeq != 0 {
		t.Fatalf("a rejected fence must leave the stage pending and the journal empty (status %v)", store.Get("a"))
	}

	accepting := &Transition{StageID: "a", From: StatusPending, To: StatusFailed, Event: "fail",
		Fence: func(*RunState) bool { return true }}
	if err := store.Apply(accepting); err != nil || store.Get("a") != StatusFailed {
		t.Fatalf("Apply with an accepting fence = %v, status %v; want a committed failure", err, store.Get("a"))
	}
}
