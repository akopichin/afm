package assistants

import (
	"errors"
	"testing"
)

func TestComputeDepth_FirstLevelIsOne(t *testing.T) {
	got := ComputeDepth(nil)
	if got != 1 {
		t.Fatalf("ComputeDepth(nil) = %d, want 1", got)
	}
}

func TestComputeDepth_ChildIsParentPlusOne(t *testing.T) {
	parent := &Task{Depth: 2}
	got := ComputeDepth(parent)
	if got != 3 {
		t.Fatalf("ComputeDepth(parent depth=2) = %d, want 3", got)
	}
}

func TestValidateTree_RejectsCycle(t *testing.T) {
	aID, bID := TaskID("a"), TaskID("b")
	a := Task{ID: aID, RunID: "run-1", RootStageID: "stage-1", ParentTaskID: &bID}
	b := Task{ID: bID, RunID: "run-1", RootStageID: "stage-1", ParentTaskID: &aID}

	err := ValidateTree([]Task{a, b}, "run-1")
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("ValidateTree(A->B->A) err = %v, want ErrCycle", err)
	}
}

func TestValidateTree_RejectsMissingParent(t *testing.T) {
	missing := TaskID("ghost")
	a := Task{ID: "a", RunID: "run-1", RootStageID: "stage-1", ParentTaskID: &missing}

	err := ValidateTree([]Task{a}, "run-1")
	if !errors.Is(err, ErrMissingParent) {
		t.Fatalf("ValidateTree(dangling parent) err = %v, want ErrMissingParent", err)
	}
}

func TestValidateTree_RejectsCrossStageParent(t *testing.T) {
	parentID := TaskID("parent")
	parent := Task{ID: parentID, RunID: "run-1", RootStageID: "stage-1"}
	child := Task{ID: "child", RunID: "run-1", RootStageID: "stage-2", ParentTaskID: &parentID}

	err := ValidateTree([]Task{parent, child}, "run-1")
	if !errors.Is(err, ErrCrossStageParent) {
		t.Fatalf("ValidateTree(cross-stage parent) err = %v, want ErrCrossStageParent", err)
	}
}

func TestValidateTree_RejectsCrossRunParent(t *testing.T) {
	parentID := TaskID("parent")
	parent := Task{ID: parentID, RunID: "run-other", RootStageID: "stage-1"}
	child := Task{ID: "child", RunID: "run-1", RootStageID: "stage-1", ParentTaskID: &parentID}

	err := ValidateTree([]Task{parent, child}, "run-1")
	if !errors.Is(err, ErrCrossRunParent) {
		t.Fatalf("ValidateTree(cross-run parent) err = %v, want ErrCrossRunParent", err)
	}
}

// TestValidateParent_RunScopedTaskWithNoParentIsValid (Agent Mesh, T6.1): a run-scoped task has no
// RootStageID and, per the plan (T6.2/T6.2b never give a run-scoped role a parent — AttachAndSend's
// run-scope path is a flat, top-level Attach-equivalent), no ParentTaskID either. ValidateParent
// must accept it exactly like any other first-level task — RootStageID being empty is never
// consulted on the no-parent path.
func TestValidateParent_RunScopedTaskWithNoParentIsValid(t *testing.T) {
	role := Task{ID: "qa", RunID: "run-1", Scope: ScopeRun, RootStageID: ""}
	if err := ValidateParent(&role, nil, "run-1"); err != nil {
		t.Fatalf("ValidateParent(run-scoped, no parent) = %v, want nil", err)
	}
}

// TestValidateTree_RunScopedTaskValidatesAlongsideStageScopedTasks (Agent Mesh, T6.1): a run-scoped
// role coexists in the same candidate slice as ordinary stage-scoped tasks (e.g. during the
// whole-tree replay check in applyTaskCreated) without tripping ErrCrossStageParent/ErrCycle/etc. —
// it never appears as anyone's parent and never looks at a parent itself.
func TestValidateTree_RunScopedTaskValidatesAlongsideStageScopedTasks(t *testing.T) {
	stageTask := Task{ID: "a", RunID: "run-1", RootStageID: "stage-1"}
	runRole := Task{ID: "qa", RunID: "run-1", Scope: ScopeRun, RootStageID: ""}

	if err := ValidateTree([]Task{stageTask, runRole}, "run-1"); err != nil {
		t.Fatalf("ValidateTree(stage task + run-scoped role) = %v, want nil", err)
	}
}

func TestBlocksRootCompletion_RequiredChain(t *testing.T) {
	aID := TaskID("a")

	t.Run("stage -> required A -> required B blocks on B", func(t *testing.T) {
		a := Task{ID: aID, Mode: ModeRequired}
		b := Task{ID: "b", Mode: ModeRequired, ParentTaskID: &aID}
		lookup := func(id TaskID) (Task, bool) {
			if id == aID {
				return a, true
			}
			return Task{}, false
		}

		if !BlocksRootCompletion(b, lookup) {
			t.Fatal("BlocksRootCompletion(B) = false, want true (stage -> required A -> required B)")
		}
	})

	t.Run("stage -> advisory A -> required B does not block on B or A", func(t *testing.T) {
		a := Task{ID: aID, Mode: ModeAdvisory}
		b := Task{ID: "b", Mode: ModeRequired, ParentTaskID: &aID}
		lookup := func(id TaskID) (Task, bool) {
			if id == aID {
				return a, true
			}
			return Task{}, false
		}

		// §4.3: обязательность не повышается при вложении — advisory-звено рвёт цепь.
		if BlocksRootCompletion(b, lookup) {
			t.Fatal("BlocksRootCompletion(B) = true, want false (advisory A must not escalate to required)")
		}
		if BlocksRootCompletion(a, lookup) {
			t.Fatal("BlocksRootCompletion(A) = true, want false (A itself is advisory)")
		}
	})
}

// TestBlocksRootCompletion_RunScopedTaskDoesNotPanic (Agent Mesh, T6.1): BlocksRootCompletion is a
// pure tree-walk with no concept of "root stage" beyond the ParentTaskID chain — calling it directly
// on a parentless, empty-RootStageID run-scoped task must not panic regardless of Mode. The actual
// exclusion from any STAGE's barrier is structural one level up: pkg/state.RequiredBlocker only ever
// builds its roots from as.ByRootStage[stageID] for a real stageID, and a run-scoped task is indexed
// under as.ByRootStage[""] — see TestRequiredBlocker_RunScopedTaskNeverBlocksAnyStage in
// pkg/state for that proof; this test only pins down that the pure function itself is safe to call.
func TestBlocksRootCompletion_RunScopedTaskDoesNotPanic(t *testing.T) {
	lookup := func(TaskID) (Task, bool) { return Task{}, false }

	advisory := Task{ID: "qa", Scope: ScopeRun, RootStageID: "", Mode: ModeAdvisory}
	if BlocksRootCompletion(advisory, lookup) {
		t.Fatal("BlocksRootCompletion(advisory run-scoped role) = true, want false")
	}

	// A required, parentless task trivially "blocks completion of its own (nonexistent) root" — true,
	// but inert: nothing ever reaches this task through a real stage's RootStageID bucket (see above).
	required := Task{ID: "qa", Scope: ScopeRun, RootStageID: "", Mode: ModeRequired}
	if !BlocksRootCompletion(required, lookup) {
		t.Fatal("BlocksRootCompletion(required run-scoped role) = false, want true (parentless chain always resolves true; isolation happens one layer up)")
	}
}

func TestTaskStatus_TerminalSet(t *testing.T) {
	terminal := []TaskStatus{StatusCompleted, StatusFailed, StatusCancelled}
	for _, s := range terminal {
		if !s.Terminal() {
			t.Errorf("%q.Terminal() = false, want true", s)
		}
	}

	nonTerminal := []TaskStatus{
		StatusWaitingParent, StatusQueued, StatusRunning,
		StatusWaitingChildren, StatusWatching, StatusPaused,
	}
	for _, s := range nonTerminal {
		if s.Terminal() {
			t.Errorf("%q.Terminal() = true, want false", s)
		}
	}
}

func TestTaskGate_CancelIsNotPass(t *testing.T) {
	if TaskGateClosed(StatusCancelled, "", false) {
		t.Fatal("TaskGateClosed(cancelled) = true, want false — cancellation must never close a required gate")
	}
	if TaskGateClosed(StatusFailed, "", false) {
		t.Fatal("TaskGateClosed(failed) = true, want false")
	}
}

func TestTaskGate_NegativeVerdictCanFinishTurnButNotSatisfyParent(t *testing.T) {
	// The task's own TURN finishes (Status: Completed) with a valid negative verdict — that's a legitimate
	// terminal outcome for the CHECK itself, but it must not satisfy the required gate (§5.3).
	for _, v := range []Verdict{VerdictNeedsChanges, VerdictInconclusive} {
		if TaskGateClosed(StatusCompleted, v, true) {
			t.Fatalf("TaskGateClosed(completed, %q) = true, want false", v)
		}
	}
	if !TaskGateClosed(StatusCompleted, VerdictPass, true) {
		t.Fatal("TaskGateClosed(completed, pass) = false, want true")
	}
	if TaskGateClosed(StatusCompleted, VerdictPass, false) {
		t.Fatal("TaskGateClosed(completed, pass, hasVerdict=false) = true, want false (fail-closed on missing result)")
	}
}

func TestChain_RequiredRequiredBlocksStage(t *testing.T) {
	aID := TaskID("a")
	a := Task{ID: aID, Mode: ModeRequired, Status: StatusCompleted} // A itself already closed...
	b := Task{ID: "b", Mode: ModeRequired, ParentTaskID: &aID, Status: StatusRunning}
	closed := func(t Task) bool { return TaskGateClosed(t.Status, VerdictPass, t.Status == StatusCompleted) }
	children := func(t Task) []Task {
		if t.ID == aID {
			return []Task{b}
		}
		return nil
	}

	// stage -> required A (closed) -> required B (still running): the chain still blocks the stage on B.
	open, blocker := RequiredGateOpen([]Task{a}, children, closed)
	if !open || blocker == nil || blocker.ID != b.ID {
		t.Fatalf("RequiredGateOpen = (%v, %v), want (true, B) — required B still open under closed required A", open, blocker)
	}

	// Once B also closes, the whole chain is satisfied.
	b.Status = StatusCompleted
	open, _ = RequiredGateOpen([]Task{a}, children, closed)
	if open {
		t.Fatal("RequiredGateOpen = true after both A and B closed, want false")
	}
}

func TestChain_AdvisoryRequiredDoesNotBlockStage(t *testing.T) {
	aID := TaskID("a")
	a := Task{ID: aID, Mode: ModeAdvisory, Status: StatusRunning}
	b := Task{ID: "b", Mode: ModeRequired, ParentTaskID: &aID, Status: StatusRunning}
	closed := func(t Task) bool { return TaskGateClosed(t.Status, VerdictPass, t.Status == StatusCompleted) }
	children := func(t Task) []Task {
		if t.ID == aID {
			return []Task{b}
		}
		return nil
	}

	// §4.3: advisory A must not escalate required B into blocking the stage — the walk never descends past A.
	open, blocker := RequiredGateOpen([]Task{a}, children, closed)
	if open {
		t.Fatalf("RequiredGateOpen = (true, %v), want (false, nil) — advisory A must not escalate required B", blocker)
	}
}

func TestVerdict_SuccessOnlyForPass(t *testing.T) {
	if !VerdictPass.ClosesGate() {
		t.Error("VerdictPass.ClosesGate() = false, want true")
	}
	for _, v := range []Verdict{VerdictNeedsChanges, VerdictInconclusive} {
		if v.ClosesGate() {
			t.Errorf("%q.ClosesGate() = true, want false", v)
		}
	}
}
