package state

import (
	"fmt"
	"testing"
)

func TestTaskHoldEpoch_LiveAndReplay(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	created := taskCreatedControl("create", "t1", "a", nil)
	created.Payload = mustMarshal(t, TaskCreatedPayload{ProfileID: "reviewer"})
	if err := store.AppendControl(created); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		kind string
		want uint64
	}{
		{"task_hold_set", 1},
		{"task_hold_cleared", 1},
		{"task_hold_set", 2},
	} {
		_, err := store.ApplyRunOperation(Operation{
			ID: fmt.Sprintf("%s:%d", step.kind, step.want), Kind: "test_hold",
			Effects: []Effect{{Kind: step.kind, Target: Target{TaskID: "t1"}, Payload: mustMarshal(t, TaskHoldPayload{Owner: "manual"})}},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := store.Snapshot().Assistants.Tasks["t1"].HoldEpoch; got != step.want {
			t.Errorf("%s: live epoch=%d want %d", step.kind, got, step.want)
		}
		store.Close()
		store, err = Open(dir, []string{"a"})
		if err != nil {
			t.Fatal(err)
		}
		if got := store.Snapshot().Assistants.Tasks["t1"].HoldEpoch; got != step.want {
			t.Errorf("%s: replay epoch=%d want %d", step.kind, got, step.want)
		}
	}
}
