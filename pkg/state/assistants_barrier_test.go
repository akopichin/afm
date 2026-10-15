package state

import (
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
)

// barrierFixture builds a minimal AssistantsState for RequiredBlocker tests: callers add tasks via addTask and
// wire ByRootStage/ByParent by hand — simpler and more explicit for these unit tests than driving the whole
// control-record projection pipeline.
type barrierFixture struct {
	as AssistantsState
}

func newBarrierFixture() *barrierFixture {
	return &barrierFixture{as: newAssistantsState()}
}

func (f *barrierFixture) addTask(t assistants.Task) {
	f.as.Tasks[t.ID] = &TaskState{Task: t}
	if t.ParentTaskID == nil {
		f.as.ByRootStage[t.RootStageID] = append(f.as.ByRootStage[t.RootStageID], t.ID)
	} else {
		f.as.ByParent[*t.ParentTaskID] = append(f.as.ByParent[*t.ParentTaskID], t.ID)
	}
}

func (f *barrierFixture) setVerdict(id assistants.TaskID, v assistants.Verdict) {
	f.as.Tasks[id].LastVerdict = v
}

func TestTaskGate_NoPassWithRunningRequiredChild(t *testing.T) {
	f := newBarrierFixture()
	f.addTask(assistants.Task{ID: "a", RootStageID: "s1", Mode: assistants.ModeRequired, Status: assistants.StatusRunning})

	blocker, ok := f.as.RequiredBlocker("s1")
	if !ok || blocker.ID != "a" {
		t.Fatalf("RequiredBlocker(s1) = (%v, %v), want (a, true) — a running required task must block completion", blocker, ok)
	}
}

func TestTaskGate_NoPassWithFailedOrStaleChildResult(t *testing.T) {
	t.Run("failed required child blocks", func(t *testing.T) {
		f := newBarrierFixture()
		f.addTask(assistants.Task{ID: "a", RootStageID: "s1", Mode: assistants.ModeRequired, Status: assistants.StatusFailed})

		blocker, ok := f.as.RequiredBlocker("s1")
		if !ok || blocker.ID != "a" {
			t.Fatalf("RequiredBlocker(s1) = (%v, %v), want (a, true) — a failed required task must never satisfy the gate", blocker, ok)
		}
	})

	t.Run("a stale (old root generation) pass does not satisfy the current attempt", func(t *testing.T) {
		f := newBarrierFixture()
		// A belongs to generation 0 (the attempt before a Revise/Retry bumped the root generation) and already
		// passed — but it must not be visible to the CURRENT generation's gate at all.
		f.addTask(assistants.Task{ID: "a", RootStageID: "s1", Mode: assistants.ModeRequired, Status: assistants.StatusCompleted, RootGeneration: 0})
		f.setVerdict("a", assistants.VerdictPass)
		// B is attached fresh, under the CURRENT generation (1), and has not resolved yet.
		f.as.RootGenerations["s1"] = 1
		f.addTask(assistants.Task{ID: "b", RootStageID: "s1", Mode: assistants.ModeRequired, Status: assistants.StatusRunning, RootGeneration: 1})

		blocker, ok := f.as.RequiredBlocker("s1")
		if !ok || blocker.ID != "b" {
			t.Fatalf("RequiredBlocker(s1) = (%v, %v), want (b, true) — stage A's stale pass from generation 0 must not satisfy generation 1's gate", blocker, ok)
		}
	})
}

func TestChain_LoweredMaxDepthDoesNotMarkHeldBranchDone(t *testing.T) {
	// A required task that can never be scheduled because the policy's max_depth was lowered below its own
	// Depth (AssistantsState.PolicyBlocked) sits forever in a non-terminal status — RequiredBlocker must keep
	// reporting it as open, never silently treat "can't run" as "done".
	f := newBarrierFixture()
	f.addTask(assistants.Task{ID: "a", RootStageID: "s1", Mode: assistants.ModeRequired, Status: assistants.StatusWaitingParent, Depth: 5})
	f.as.Policy = PolicyState{MaxDepth: 2, Revision: "rev-1"}

	if !f.as.PolicyBlocked("a") {
		t.Fatal("fixture invariant: task a should be policy-blocked (depth 5 > max_depth 2)")
	}

	blocker, ok := f.as.RequiredBlocker("s1")
	if !ok || blocker.ID != "a" {
		t.Fatalf("RequiredBlocker(s1) = (%v, %v), want (a, true) — a policy-blocked required task must stay open, never marked done", blocker, ok)
	}
}

func TestRequiredBlocker_NoRequiredTasksGateSatisfied(t *testing.T) {
	f := newBarrierFixture()
	f.addTask(assistants.Task{ID: "a", RootStageID: "s1", Mode: assistants.ModeAdvisory, Status: assistants.StatusRunning})

	if _, ok := f.as.RequiredBlocker("s1"); ok {
		t.Fatal("RequiredBlocker(s1) ok=true, want false — only advisory tasks are attached, nothing blocks")
	}
}

func TestRequiredBlocker_PassedRequiredTaskSatisfiesGate(t *testing.T) {
	f := newBarrierFixture()
	f.addTask(assistants.Task{ID: "a", RootStageID: "s1", Mode: assistants.ModeRequired, Status: assistants.StatusCompleted})
	f.setVerdict("a", assistants.VerdictPass)

	if _, ok := f.as.RequiredBlocker("s1"); ok {
		t.Fatal("RequiredBlocker(s1) ok=true, want false — the only required task already passed")
	}
}

// --- T7.7: isIdle classification of awaiting_assistants ---

func TestIdle_WorkingAssistantIsNotUserIdle(t *testing.T) {
	f := newBarrierFixture()
	f.addTask(assistants.Task{ID: "a", RootStageID: "s1", Mode: assistants.ModeRequired, Status: assistants.StatusRunning})
	rs := &RunState{
		Stages:     map[string]StageState{"s1": {Status: StatusAwaitingAssistants}},
		Assistants: f.as,
	}
	if isIdle(rs) {
		t.Error("want idle=false while the blocking required task is actively running — that is machine work")
	}
}

func TestIdle_NeedsChangesDecisionIsUserIdle(t *testing.T) {
	f := newBarrierFixture()
	f.addTask(assistants.Task{ID: "a", RootStageID: "s1", Mode: assistants.ModeRequired, Status: assistants.StatusCompleted})
	f.setVerdict("a", assistants.VerdictNeedsChanges)
	rs := &RunState{
		Stages:     map[string]StageState{"s1": {Status: StatusAwaitingAssistants}},
		Assistants: f.as,
	}
	if !isIdle(rs) {
		t.Error("want idle=true — a needs_changes verdict on the blocking required task requires a human decision")
	}
}

func TestIdle_WaitingChildrenNotDoubleCounted(t *testing.T) {
	// A (required, waiting_children) delegated to B (required, still running). RequiredBlocker stops at the
	// shallowest open required node (A) rather than descending further — whatever the real status, idle
	// classification must consult exactly ONE blocker, not accrue extra weight per tree level.
	f := newBarrierFixture()
	f.addTask(assistants.Task{ID: "a", RootStageID: "s1", Mode: assistants.ModeRequired, Status: assistants.StatusWaitingChildren})
	aID := assistants.TaskID("a")
	f.addTask(assistants.Task{ID: "b", RootStageID: "s1", ParentTaskID: &aID, Mode: assistants.ModeRequired, Status: assistants.StatusRunning})

	blocker, ok := f.as.RequiredBlocker("s1")
	if !ok || blocker.ID != "a" {
		t.Fatalf("RequiredBlocker(s1) = (%v, %v), want (a, true)", blocker, ok)
	}
	rs := &RunState{
		Stages:     map[string]StageState{"s1": {Status: StatusAwaitingAssistants}},
		Assistants: f.as,
	}
	if isIdle(rs) {
		t.Error("want idle=false — waiting_children is machine work (delegation in progress), not a human decision point")
	}
}

func TestIdle_LiveAndReplayAgree(t *testing.T) {
	// Drives the SAME scenario (stage in awaiting_assistants, blocked on a required task that resolves
	// needs_changes) through live Store.Apply/AppendControl, then reopens the store (forcing a full
	// parseEventLog replay) and asserts the idle accumulator is bit-for-bit identical — direct protection
	// against accountIdleAndBackoff's live/replay call sites drifting apart (AGENTS.md).
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := store.Apply(&Transition{StageID: "a", From: StatusPending, To: StatusRunning, Event: "start"}); err != nil {
		t.Fatalf("Apply(running): %v", err)
	}

	created := taskCreatedControl("op-1", "t1", "a", nil)
	created.Payload = mustMarshal(t, TaskCreatedPayload{ProfileID: "reviewer", Mode: "required"})
	if err := store.AppendControl(created); err != nil {
		t.Fatalf("AppendControl(created): %v", err)
	}

	// The stage enters awaiting_assistants while t1 is still running — machine work, not idle yet.
	if err := store.Apply(&Transition{StageID: "a", From: StatusRunning, To: StatusAwaitingAssistants, Event: "await_assistants"}); err != nil {
		t.Fatalf("Apply(awaiting_assistants): %v", err)
	}
	snap := store.Snapshot()
	if snap.IdleSince() != nil {
		t.Fatal("IdleSince() != nil while the blocking task is still running, want nil")
	}

	time.Sleep(10 * time.Millisecond)

	// t1 resolves needs_changes — now a human decision is pending; idle should open from here.
	resultDone := &Control{Kind: "task_status_changed", SchemaVersion: ControlSchemaVersion,
		Target: Target{TaskID: "t1"}, Payload: mustMarshal(t, TaskStatusChangedPayload{Status: "completed", Reason: "turn finished"})}
	if err := store.AppendControl(resultDone); err != nil {
		t.Fatalf("AppendControl(resultDone): %v", err)
	}
	taskResult := &Control{Kind: "task_result", SchemaVersion: ControlSchemaVersion,
		Target: Target{TaskID: "t1"}, Payload: mustMarshal(t, assistants.Result{Verdict: assistants.VerdictNeedsChanges, Summary: "fix X"})}
	if err := store.AppendControl(taskResult); err != nil {
		t.Fatalf("AppendControl(taskResult): %v", err)
	}

	snap = store.Snapshot()
	if snap.IdleSince() == nil {
		t.Fatal("IdleSince() == nil after needs_changes verdict, want non-nil")
	}

	time.Sleep(10 * time.Millisecond)
	// One more unrelated transition forces accountIdleAndBackoff to close the idle interval it had open.
	if err := store.Apply(&Transition{StageID: "a", From: StatusAwaitingAssistants, To: StatusFailed, Event: "test"}); err != nil {
		t.Fatalf("Apply(failed): %v", err)
	}

	before := store.Snapshot()
	if before.IdleAccumulatedMs <= 0 {
		t.Fatalf("IdleAccumulatedMs before close = %d, want > 0", before.IdleAccumulatedMs)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer reopened.Close()

	after := reopened.Snapshot()
	if after.IdleAccumulatedMs != before.IdleAccumulatedMs {
		t.Errorf("IdleAccumulatedMs after replay = %d, want %d (live and replay must agree)", after.IdleAccumulatedMs, before.IdleAccumulatedMs)
	}
}

func TestBackoff_StageSemanticsUnchanged(t *testing.T) {
	// A stage's own retrying/backoff accounting must not be affected by an unrelated stage sitting in
	// awaiting_assistants with a working (non-decision) blocker.
	f := newBarrierFixture()
	f.addTask(assistants.Task{ID: "a", RootStageID: "s2", Mode: assistants.ModeRequired, Status: assistants.StatusRunning})
	rs := &RunState{
		Stages: map[string]StageState{
			"s1": {Status: StatusRetrying},
			"s2": {Status: StatusAwaitingAssistants},
		},
		Assistants: f.as,
	}
	if isIdle(rs) {
		t.Error("want idle=false — retrying alone is not idle, and s2's assistant is still working")
	}
}
