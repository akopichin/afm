package state

import (
	"slices"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
)

// TestTaskTurn_InventoryProjectsLiveAndOnReplay: a running record with a turn number starts that turn and
// records the messages handed to it; a later status change naming the same turn ends it and forgets its
// input; a status change without a turn (a cut-short turn: Pause, a crash) keeps the input for the retry.
// The journal alone gives the same projection after a restart.
func TestTaskTurn_InventoryProjectsLiveAndOnReplay(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	created := taskCreatedControl("op-1", "t1", "a", nil)
	created.Payload = mustMarshal(t, TaskCreatedPayload{ProfileID: "reviewer"})
	appendStatus := func(p TaskStatusChangedPayload) {
		t.Helper()
		c := &Control{Kind: "task_status_changed", SchemaVersion: ControlSchemaVersion, Target: Target{TaskID: "t1"}, Payload: mustMarshal(t, p)}
		if err := store.AppendControl(c); err != nil {
			t.Fatalf("AppendControl(%+v): %v", p, err)
		}
	}
	check := func(rs RunState, wantTurn uint64, wantInbox []agentcomm.MessageID) {
		t.Helper()
		task := rs.Assistants.Tasks["t1"]
		if task.Turn != wantTurn || !slices.Equal(task.TurnInbox, wantInbox) {
			t.Fatalf("turn = %d, inbox = %v; want %d, %v", task.Turn, task.TurnInbox, wantTurn, wantInbox)
		}
	}
	if err := store.AppendControl(created); err != nil {
		t.Fatal(err)
	}
	appendStatus(TaskStatusChangedPayload{Status: "running"}) // the queued→running start carries no turn
	check(store.Snapshot(), 0, nil)

	appendStatus(TaskStatusChangedPayload{Status: "running", Turn: 1, TurnInbox: []string{"m1"}})
	check(store.Snapshot(), 1, []agentcomm.MessageID{"m1"})
	appendStatus(TaskStatusChangedPayload{Status: "queued", Reason: "re-queued: paused while running"}) // cut short
	check(store.Snapshot(), 1, []agentcomm.MessageID{"m1"})

	appendStatus(TaskStatusChangedPayload{Status: "running"})
	appendStatus(TaskStatusChangedPayload{Status: "running", Turn: 2, TurnInbox: []string{"m1", "m2"}})
	appendStatus(TaskStatusChangedPayload{Status: "queued", Turn: 2}) // the turn's result committed
	check(store.Snapshot(), 2, nil)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	replayed, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	check(replayed, 2, nil)
}

// TestTaskTurn_ReadonlyEnforcementProjectsLiveAndOnReplay: a turn-start record names the read-only guarantee the
// turn runs under; the task keeps the latest one, a status change without a turn leaves it, and replay agrees.
func TestTaskTurn_ReadonlyEnforcementProjectsLiveAndOnReplay(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	created := taskCreatedControl("op-1", "t1", "a", nil)
	created.Payload = mustMarshal(t, TaskCreatedPayload{ProfileID: "reviewer"})
	if err := store.AppendControl(created); err != nil {
		t.Fatal(err)
	}
	for _, p := range []TaskStatusChangedPayload{
		{Status: "running"},
		{Status: "running", Turn: 1, ReadonlyEnforcement: "os"},
		{Status: "queued", Turn: 1},
		{Status: "running"},
		{Status: "running", Turn: 2, ReadonlyEnforcement: "none"},
		{Status: "completed", Turn: 2},
	} {
		c := &Control{Kind: "task_status_changed", SchemaVersion: ControlSchemaVersion, Target: Target{TaskID: "t1"}, Payload: mustMarshal(t, p)}
		if err := store.AppendControl(c); err != nil {
			t.Fatalf("AppendControl(%+v): %v", p, err)
		}
	}
	if got := store.Snapshot().Assistants.Tasks["t1"].TurnReadonlyEnforcement; got != "none" {
		t.Fatalf("live turn enforcement = %q, want the last turn's none", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	replayed, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	if got := replayed.Assistants.Tasks["t1"].TurnReadonlyEnforcement; got != "none" {
		t.Fatalf("replayed turn enforcement = %q, want none", got)
	}
}
