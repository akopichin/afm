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
