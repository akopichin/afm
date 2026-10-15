package state

import (
	"encoding/json"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
)

// This file covers the pkg/state half of T2.7 (restoring the task tree, continuations and policy on
// recovery, in a replay-safe way) — the cmd/afm half (wiring the production call ORDER: store+flock before
// orchestrator.New) is covered separately in cmd/afm/run_collaboration_test.go, since pkg/state has no
// notion of "orchestrator" to order against.

func statusChangedCtl(taskID, status string) *Control {
	payload, _ := json.Marshal(TaskStatusChangedPayload{Status: status})
	return &Control{Kind: "task_status_changed", SchemaVersion: ControlSchemaVersion, Target: Target{TaskID: taskID}, Payload: payload}
}

func TestRecovery_CompletedChildrenQueueContinuationOnce(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	mustRecordPolicy(t, store)

	parentOp := Operation{ID: "op-parent", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "parent", "a")}}
	if _, err := store.ApplyRunOperation(parentOp, nil); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	parentID := "parent"
	for _, id := range []string{"c1", "c2"} {
		op := Operation{ID: "op-" + id, Kind: "create_children", Effects: []Effect{taskCreatedEffect(
			`{"profile_id":"p","parent_task_id":"`+parentID+`"}`, id, "a")}}
		if _, err := store.ApplyRunOperation(op, nil); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	if err := store.AppendControl(statusChangedCtl("parent", "waiting_children")); err != nil {
		t.Fatalf("mark parent waiting_children: %v", err)
	}
	cont := &Control{Kind: "task_continuation_saved", SchemaVersion: ControlSchemaVersion, Target: Target{TaskID: "parent"},
		Payload: mustMarshal(t, assistants.Continuation{Trigger: assistants.TriggerOnCompletion})}
	if err := store.AppendControl(cont); err != nil {
		t.Fatalf("save continuation: %v", err)
	}

	// Children not yet terminal — parent must not show up as ready yet.
	if ready := store.Snapshot().Assistants.ReadyContinuations(); len(ready) != 0 {
		t.Fatalf("ReadyContinuations before children finish = %v, want empty", ready)
	}

	if err := store.AppendControl(statusChangedCtl("c1", "completed")); err != nil {
		t.Fatalf("complete c1: %v", err)
	}
	if err := store.AppendControl(statusChangedCtl("c2", "completed")); err != nil {
		t.Fatalf("complete c2: %v", err)
	}

	ready := store.Snapshot().Assistants.ReadyContinuations()
	if len(ready) != 1 || ready[0] != "parent" {
		t.Fatalf("ReadyContinuations = %v, want exactly [parent]", ready)
	}
	// Calling it again (simulating a retried/duplicated recovery attempt) must still report it EXACTLY once —
	// there is no separate "already queued" bit to desync.
	ready2 := store.Snapshot().Assistants.ReadyContinuations()
	if len(ready2) != 1 || ready2[0] != "parent" {
		t.Fatalf("second ReadyContinuations call = %v, want exactly [parent] again (idempotent, not double-queued)", ready2)
	}
	store.Close()

	// And after a full restart, replay alone reaches the same conclusion.
	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	readyAfterRestart := rs.Assistants.ReadyContinuations()
	if len(readyAfterRestart) != 1 || readyAfterRestart[0] != "parent" {
		t.Fatalf("ReadyContinuations after restart = %v, want exactly [parent]", readyAfterRestart)
	}
}

func TestRecovery_WaitingChildrenDoesNotHoldSlot(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	mustRecordPolicy(t, store)

	op := Operation{ID: "op-1", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "t1", "a")}}
	if _, err := store.ApplyRunOperation(op, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.AppendControl(statusChangedCtl("t1", "waiting_children")); err != nil {
		t.Fatalf("mark waiting_children: %v", err)
	}
	cont := &Control{Kind: "task_continuation_saved", SchemaVersion: ControlSchemaVersion, Target: Target{TaskID: "t1"},
		Payload: mustMarshal(t, assistants.Continuation{Trigger: assistants.TriggerOnCompletion})}
	if err := store.AppendControl(cont); err != nil {
		t.Fatalf("save continuation: %v", err)
	}
	store.Close()

	// pkg/state has no concept of a concurrency slot at all (that is an orchestrator-level runtime resource,
	// always empty right after a fresh process start — see pkg/orchestrator/concurrency). The only thing
	// recovery can observe here is that the restored status is waiting_children, NOT running — a status of
	// running is what would make a real orchestrator consumer (re)acquire a slot for it.
	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	task, ok := rs.Assistants.Tasks["t1"]
	if !ok {
		t.Fatal("task t1 missing after restart")
	}
	if task.Status != assistants.StatusWaitingChildren {
		t.Fatalf("Status after restart = %q, want waiting_children (not running — must not look like it needs a slot)", task.Status)
	}
}

// TestRecovery_OldRunWithoutControlRecords: a run predating the collaboration feature (zero control
// records, legacy Transitions only) must load via the fully compatible legacy path, AND the current
// (possibly very different) flow.yaml policy must not be silently treated as though it had been in force
// since that old run started — EnsurePolicyRecorded always writes a FRESH record for a run with no recorded
// policy, it never backfills history.
func TestRecovery_OldRunWithoutControlRecords(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Only legacy stage transitions — exactly what a pre-collaboration run's journal looks like.
	if err := store.Apply(&Transition{StageID: "a", From: StatusPending, To: StatusDone, Event: "complete"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	store.Close()

	store2, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("reopen old run: %v", err)
	}
	defer store2.Close()
	if got := store2.Get("a"); got != StatusDone {
		t.Fatalf("Get(a) = %q, want done (legacy replay must still work)", got)
	}
	if rev := store2.Snapshot().Assistants.Policy.Revision; rev != "" {
		t.Fatalf("Policy.Revision = %q before any EnsurePolicyRecorded call, want empty (nothing to inherit from an old run)", rev)
	}

	changed, err := store2.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 2, Source: assistants.DepthSourceFlow, Revision: "current-flow-rev"}, Actor{Kind: "system"})
	if err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}
	if !changed {
		t.Fatal("EnsurePolicyRecorded on an old run with no prior policy: want changed=true (a fresh record, not a silent no-op)")
	}
	if got := store2.Snapshot().Assistants.Policy.Revision; got != "current-flow-rev" {
		t.Fatalf("Policy.Revision = %q, want current-flow-rev", got)
	}
}
