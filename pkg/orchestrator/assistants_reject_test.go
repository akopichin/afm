package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/state"
)

// readFollowUp reads a task's own followup.md directly off disk — deliverDelegationRejection's note never
// gets deleted after being read (loadFollowUp is read-only, same as every other turn's follow-up), so it is
// still there to inspect once the task has moved on to a later turn.
func readFollowUp(t *testing.T, o *Orchestrator, taskID assistants.TaskID) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(o.taskDir(taskID), "followup.md"))
	if err != nil {
		t.Fatalf("read followup.md for %s: %v", taskID, err)
	}
	return string(raw)
}

// ---- T4a.7: typed delegation rejections ----

// TestReject_DepthExceededReturnsTypedResultToParent: a delegate request that would exceed max_depth is
// rejected with a typed, readable note delivered back to the SAME task (not a bare failure) — the task gets
// a chance to do the remaining work itself on its next turn.
func TestReject_DepthExceededReturnsTypedResultToParent(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 1, Source: assistants.DepthSourceDefault, Revision: "rev-shallow"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}

	var calls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil // depth 2 > max_depth 1
		}
		return passResultOutcome(), nil // having seen the rejection, do the work itself instead
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusCompleted, 2*time.Second)

	note := readFollowUp(t, o, parentID)
	if !strings.Contains(note, string(assistants.CodeDelegationDepthExceeded)) {
		t.Fatalf("follow-up note = %q, want it to mention %q", note, assistants.CodeDelegationDepthExceeded)
	}
	if len(store.Snapshot().Assistants.ByParent[parentID]) != 0 {
		t.Fatal("a rejected delegation must not have created any child")
	}
}

// TestReject_BudgetExceeded mirrors the depth test for a whole-run quota ceiling of zero.
func TestReject_BudgetExceeded(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	zero := 0
	o.opts.Config.Collaboration.Limits.MaxTasks = &zero

	var calls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
		}
		return passResultOutcome(), nil
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusCompleted, 2*time.Second)

	note := readFollowUp(t, o, parentID)
	if !strings.Contains(note, string(assistants.CodeBudgetExceeded)) {
		t.Fatalf("follow-up note = %q, want it to mention %q", note, assistants.CodeBudgetExceeded)
	}
}

// TestReject_NoSilentPartialExecution: a 2-child delegate request against a 1-task quota ceiling creates
// NEITHER child — never a silently truncated group.
func TestReject_NoSilentPartialExecution(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	one := 1
	o.opts.Config.Collaboration.Limits.MaxTasks = &one

	var calls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			return delegateOutcome(childJSON(assistants.ModeAdvisory), childJSON(assistants.ModeAdvisory)), nil
		}
		return passResultOutcome(), nil
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusCompleted, 2*time.Second)

	if got := store.Snapshot().Assistants.ByParent[parentID]; len(got) != 0 {
		t.Fatalf("ByParent[parent] = %v, want none at all (all-or-nothing, not a partial group of 1)", got)
	}
}

// TestReject_SameRequestIdDoesNotCreateDuplicates: admitting children twice with the SAME operation id (the
// idempotency key) is a true no-op replay — the second call returns the SAME child ids, never a second group.
func TestReject_SameRequestIdDoesNotCreateDuplicates(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	release := make(chan struct{})
	defer func() {
		close(release)
		o.concurrency.WaitAgents()
	}()
	parentID := runningParentTask(t, o, store, release)

	actor := state.Actor{Kind: "assistant", ID: string(parentID)}
	ids1, err := o.admitChildren(context.Background(), "op-same", parentID, oneChild(), actor, "", nil)
	if err != nil {
		t.Fatalf("admitChildren (first): %v", err)
	}
	ids2, err := o.admitChildren(context.Background(), "op-same", parentID, oneChild(), actor, "", nil)
	if err != nil {
		t.Fatalf("admitChildren (same operation id, replayed): %v", err)
	}
	if len(ids2) != 1 || ids2[0] != ids1[0] {
		t.Fatalf("second admitChildren with the same operation id = %v, want the identical id %v", ids2, ids1)
	}
	if got := store.Snapshot().Assistants.ByParent[parentID]; len(got) != 1 {
		t.Fatalf("ByParent[parent] = %v, want exactly 1 child despite two admitChildren calls with the same operation id", got)
	}
}

// TestReject_NoHiddenRootParentSubstitution: a rejected-then-retried delegate turn never spawns a
// replacement task or changes the original task's own parent — it is the SAME task id, same (lack of)
// parent, taking another turn.
func TestReject_NoHiddenRootParentSubstitution(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 1, Source: assistants.DepthSourceDefault, Revision: "rev-shallow"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}

	var calls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
		}
		return passResultOutcome(), nil
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusCompleted, 2*time.Second)

	rs := store.Snapshot()
	if len(rs.Assistants.Tasks) != 1 {
		t.Fatalf("got %d tasks, want exactly 1 — a rejected delegate request must never spawn a substitute task", len(rs.Assistants.Tasks))
	}
	if tk := rs.Assistants.Tasks[parentID]; tk.ParentTaskID != nil {
		t.Fatalf("task's own parent = %v after a rejected delegation, want it to remain top-level (nil)", *tk.ParentTaskID)
	}
}
