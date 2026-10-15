package state

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
)

// mustMarshal is a small test helper: marshal v to json.RawMessage, failing the test on error.
func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func taskCreatedControl(opID, taskID, stageID string, parent *string) *Control {
	return &Control{
		Kind:          "task_created",
		SchemaVersion: ControlSchemaVersion,
		OperationID:   opID,
		Actor:         Actor{Kind: "user"},
		Target:        Target{TaskID: taskID, StageID: stageID},
		Payload:       json.RawMessage(`{}`), // overwritten below when a parent/profile is needed
	}
}

func TestReplay_TaskCreatedThenStatusChanges(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	created := taskCreatedControl("op-1", "t1", "a", nil)
	created.Payload = mustMarshal(t, TaskCreatedPayload{ProfileID: "reviewer", Title: "Review"})
	if err := store.AppendControl(created); err != nil {
		t.Fatalf("AppendControl(created): %v", err)
	}

	running := &Control{Kind: "task_status_changed", SchemaVersion: ControlSchemaVersion,
		Target: Target{TaskID: "t1"}, Payload: mustMarshal(t, TaskStatusChangedPayload{Status: "running"})}
	if err := store.AppendControl(running); err != nil {
		t.Fatalf("AppendControl(running): %v", err)
	}

	done := &Control{Kind: "task_status_changed", SchemaVersion: ControlSchemaVersion,
		Target: Target{TaskID: "t1"}, Payload: mustMarshal(t, TaskStatusChangedPayload{Status: "completed", Reason: "ok"})}
	if err := store.AppendControl(done); err != nil {
		t.Fatalf("AppendControl(done): %v", err)
	}
	store.Close()

	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	task, ok := rs.Assistants.Tasks["t1"]
	if !ok {
		t.Fatal("task t1 not found after replay")
	}
	if task.Status != assistants.StatusCompleted {
		t.Errorf("Status = %q, want completed", task.Status)
	}
	if task.StatusReason != "ok" {
		t.Errorf("StatusReason = %q, want ok", task.StatusReason)
	}
	if task.StartedAt.IsZero() {
		t.Error("StartedAt is zero, want set from the running transition")
	}
	if task.FinishedAt.IsZero() {
		t.Error("FinishedAt is zero, want set from the terminal transition")
	}
}

func TestReplay_ComputesDepthFromParent(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	root := taskCreatedControl("op-1", "t1", "a", nil)
	root.Payload = mustMarshal(t, TaskCreatedPayload{ProfileID: "p"})
	if err := store.AppendControl(root); err != nil {
		t.Fatalf("AppendControl(root): %v", err)
	}

	parentID := "t1"
	child := taskCreatedControl("op-2", "t2", "a", &parentID)
	// The payload claims a bogus depth-adjacent hint via a completely different (wrong) parent — but since
	// there is no "depth" field in the payload at all, there is nothing for an untrusted model to lie about;
	// this test instead confirms depth is derived, not copied from anywhere in payload.
	child.Payload = mustMarshal(t, TaskCreatedPayload{ProfileID: "p", ParentTaskID: &parentID})
	if err := store.AppendControl(child); err != nil {
		t.Fatalf("AppendControl(child): %v", err)
	}

	grandparentID := "t2"
	grandchild := taskCreatedControl("op-3", "t3", "a", &grandparentID)
	grandchild.Payload = mustMarshal(t, TaskCreatedPayload{ProfileID: "p", ParentTaskID: &grandparentID})
	if err := store.AppendControl(grandchild); err != nil {
		t.Fatalf("AppendControl(grandchild): %v", err)
	}
	store.Close()

	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	if d := rs.Assistants.Tasks["t1"].Depth; d != 1 {
		t.Errorf("t1 depth = %d, want 1", d)
	}
	if d := rs.Assistants.Tasks["t2"].Depth; d != 2 {
		t.Errorf("t2 depth = %d, want 2", d)
	}
	if d := rs.Assistants.Tasks["t3"].Depth; d != 3 {
		t.Errorf("t3 depth = %d, want 3", d)
	}
}

func TestReplay_RejectsCycle(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	selfID := "t1"
	c := taskCreatedControl("op-1", "t1", "a", &selfID)
	c.Payload = mustMarshal(t, TaskCreatedPayload{ProfileID: "p", ParentTaskID: &selfID})
	err = store.AppendControl(c)
	if err == nil {
		t.Fatal("AppendControl with self-referencing parent: want error, got nil")
	}
	if !errors.Is(err, assistants.ErrCycle) {
		t.Fatalf("err = %v, want wrapping assistants.ErrCycle", err)
	}
}

func TestReplay_RejectsMissingParent(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	missing := "does-not-exist"
	c := taskCreatedControl("op-1", "t1", "a", &missing)
	c.Payload = mustMarshal(t, TaskCreatedPayload{ProfileID: "p", ParentTaskID: &missing})
	err = store.AppendControl(c)
	if err == nil {
		t.Fatal("AppendControl with a missing parent: want error, got nil")
	}
	if !errors.Is(err, assistants.ErrMissingParent) {
		t.Fatalf("err = %v, want wrapping assistants.ErrMissingParent", err)
	}
}

// TestReplay_HoldsRestored proves a task's own pause and a separate pause of its ancestor restore as two
// INDEPENDENT holds with distinct owners: clearing one must not clear the other.
func TestReplay_HoldsRestored(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	parentID := "parent"
	root := taskCreatedControl("op-1", "parent", "a", nil)
	root.Payload = mustMarshal(t, TaskCreatedPayload{ProfileID: "p"})
	_ = store.AppendControl(root)
	child := taskCreatedControl("op-2", "child", "a", &parentID)
	child.Payload = mustMarshal(t, TaskCreatedPayload{ProfileID: "p", ParentTaskID: &parentID})
	_ = store.AppendControl(child)

	// Pause the child directly (owner "manual") AND the parent directly (owner "manual") — two separate
	// tasks, each with its own independent hold.
	holdChild := &Control{Kind: "task_hold_set", SchemaVersion: ControlSchemaVersion,
		Target: Target{TaskID: "child"}, Payload: mustMarshal(t, TaskHoldPayload{Owner: "manual", Reason: "user paused child"})}
	if err := store.AppendControl(holdChild); err != nil {
		t.Fatalf("AppendControl(holdChild): %v", err)
	}
	holdParent := &Control{Kind: "task_hold_set", SchemaVersion: ControlSchemaVersion,
		Target: Target{TaskID: "parent"}, Payload: mustMarshal(t, TaskHoldPayload{Owner: "manual", Reason: "user paused parent"})}
	if err := store.AppendControl(holdParent); err != nil {
		t.Fatalf("AppendControl(holdParent): %v", err)
	}
	store.Close()

	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	if !rs.Assistants.Tasks["child"].Paused() {
		t.Error("child.Paused() = false, want true (direct hold)")
	}
	if !rs.Assistants.Tasks["parent"].Paused() {
		t.Error("parent.Paused() = false, want true (direct hold)")
	}
	held, holds := rs.Assistants.EffectivelyHeld("child")
	if !held || len(holds) != 2 {
		t.Fatalf("EffectivelyHeld(child) = (%v, %+v), want held with 2 holds (own + ancestor)", held, holds)
	}

	// Clear only the child's own hold — the parent's hold (and thus child's ancestor-derived
	// EffectivelyHeld) must remain, proving the two holds are independent.
	store2, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	clearChild := &Control{Kind: "task_hold_cleared", SchemaVersion: ControlSchemaVersion,
		Target: Target{TaskID: "child"}, Payload: mustMarshal(t, TaskHoldPayload{Owner: "manual"})}
	if err := store2.AppendControl(clearChild); err != nil {
		t.Fatalf("AppendControl(clearChild): %v", err)
	}
	store2.Close()

	rs2, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState after clear: %v", err)
	}
	if rs2.Assistants.Tasks["child"].Paused() {
		t.Error("child.Paused() = true after clearing its own hold, want false")
	}
	heldAfter, holdsAfter := rs2.Assistants.EffectivelyHeld("child")
	if !heldAfter || len(holdsAfter) != 1 {
		t.Fatalf("EffectivelyHeld(child) after clearing its own hold = (%v, %+v), want still held via the untouched parent hold", heldAfter, holdsAfter)
	}
	if !rs2.Assistants.Tasks["parent"].Paused() {
		t.Error("parent.Paused() = false, want true (its own hold must be untouched by clearing the child's)")
	}
}

// TestReplay_ContinuationRestored: a task in waiting_children with a saved continuation restores as waiting
// — pkg/state has no notion of a concurrency slot at all (those are an orchestrator-level runtime resource,
// always empty right after a fresh process start), so "without holding a slot" reduces to: the restored
// status is waiting_children, not running.
func TestReplay_ContinuationRestored(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c := taskCreatedControl("op-1", "t1", "a", nil)
	c.Payload = mustMarshal(t, TaskCreatedPayload{ProfileID: "p"})
	_ = store.AppendControl(c)

	waiting := &Control{Kind: "task_status_changed", SchemaVersion: ControlSchemaVersion,
		Target: Target{TaskID: "t1"}, Payload: mustMarshal(t, TaskStatusChangedPayload{Status: "waiting_children"})}
	if err := store.AppendControl(waiting); err != nil {
		t.Fatalf("AppendControl(waiting): %v", err)
	}
	cont := &Control{Kind: "task_continuation_saved", SchemaVersion: ControlSchemaVersion,
		Target: Target{TaskID: "t1"}, Payload: mustMarshal(t, assistants.Continuation{Trigger: assistants.TriggerOnCompletion, Note: "resume when children finish"})}
	if err := store.AppendControl(cont); err != nil {
		t.Fatalf("AppendControl(continuation): %v", err)
	}
	store.Close()

	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	task := rs.Assistants.Tasks["t1"]
	if task.Status != assistants.StatusWaitingChildren {
		t.Fatalf("Status = %q, want waiting_children", task.Status)
	}
	if task.Continuation == nil || task.Continuation.Note != "resume when children finish" {
		t.Fatalf("Continuation = %+v, want restored note", task.Continuation)
	}
}

// TestReplay_IdempotencyIndexRestored: the operation_id index is restored for EVERY record in the log, not
// just the tail — write records spread across the log (not clustered at the end) and confirm the very first
// one is still recognized after a restart.
func TestReplay_IdempotencyIndexRestored(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	first := taskCreatedControl("op-first", "t1", "a", nil)
	first.Payload = mustMarshal(t, TaskCreatedPayload{ProfileID: "p"})
	if err := store.AppendControl(first); err != nil {
		t.Fatalf("AppendControl(first): %v", err)
	}
	firstSeq := first.Seq

	// Several unrelated records in between (spread across the log, not clustered at the end).
	for i := 0; i < 5; i++ {
		status := &Control{Kind: "task_status_changed", SchemaVersion: ControlSchemaVersion,
			Target: Target{TaskID: "t1"}, Payload: mustMarshal(t, TaskStatusChangedPayload{Status: "queued"})}
		if err := store.AppendControl(status); err != nil {
			t.Fatalf("AppendControl(status %d): %v", i, err)
		}
	}

	last := taskCreatedControl("op-last", "t2", "a", nil)
	last.Payload = mustMarshal(t, TaskCreatedPayload{ProfileID: "p"})
	if err := store.AppendControl(last); err != nil {
		t.Fatalf("AppendControl(last): %v", err)
	}
	store.Close()

	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	if seq, ok := rs.Assistants.OperationIndex["op-first"]; !ok || seq != firstSeq {
		t.Fatalf("OperationIndex[op-first] = (%d, %v), want (%d, true) — the FIRST record's key must survive restart same as the last's", seq, ok, firstSeq)
	}
	if _, ok := rs.Assistants.OperationIndex["op-last"]; !ok {
		t.Fatal("OperationIndex[op-last] missing")
	}
}
