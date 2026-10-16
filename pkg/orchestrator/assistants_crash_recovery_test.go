package orchestrator

// T11.3 (§19.3 scenario 18): two crash points in the recursive-delegation lifecycle that
// TestWaitingChildren_RestoredAfterRestart (assistants_continuation_test.go) stops short of —  it only
// asserts the raw post-restart SNAPSHOT is correctly restored, never builds a second Orchestrator and proves
// recovery actually drives anything to completion. These two tests close that gap end-to-end:
//   (a) a child is durably SAVED (task_created + queued) but the process crashes before it is ever spawned.
//   (b) a child's result is durably recorded (terminal status committed) but the process crashes before the
//       parent's own continuation (tryResumeParent) ever runs.
// Both commit the relevant effects directly via store.ApplyRunOperation(op, nil) — nil externalWork is this
// repo's established "crashed before the follow-up action ran" seam (see feedback_intent_crash_test.go) —
// then reopen a FRESH Store + Orchestrator against the same run directory and let recoverAssistants (called
// from a real `afm run`'s startup path) carry the stuck task to completion.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/state"
)

// reopenAssistantsOrchestrator builds a fresh Orchestrator sharing o's options/graph shape but a NEW Store —
// the test-level stand-in for "afm restarted and re-opened the same run directory" (mirrors
// feedback_intent_crash_test.go's reopenFeedbackIntentOrchestrator for the assistants fixture family).
func reopenAssistantsOrchestrator(o *Orchestrator, store2 *state.Store) *Orchestrator {
	opts := o.opts
	opts.Store = store2
	cb := bus.NewCriticalBus(16)
	return &Orchestrator{
		opts: opts, graph: o.graph, runner: o.runner,
		ui: bus.NewUIBus(), critical: cb, fsm: bus.NewFSM(store2),
		concurrency:   concurrency.New(cb, opts.Stages, "claude", 0, nil),
		fingerprintFn: func(string) (string, bool) { return "", false },
	}
}

// TestCrash_QueuedChildNeverSpawnedBeforeCrash_RecoveredOnRestart — scenario 18(a).
func TestCrash_QueuedChildNeverSpawnedBeforeCrash_RecoveredOnRestart(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	runDir := o.opts.RunDir

	parentRelease := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		<-parentRelease // the parent's own turn — never completes before the simulated crash
		return passResultOutcome(), nil
	}
	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusRunning, 2*time.Second)

	// Commit JUST "child created, queued" — bypassing admitChildren's own externalWork (which would
	// immediately call maybeScheduleTask) — the exact state a crash between "children saved" and "spawn"
	// would leave behind.
	childID := deriveTaskID("op-child:0")
	parentIDStr := string(parentID)
	created, err := taskCreatedEffect(childID, assistantsTestStageID, state.TaskCreatedPayload{
		ParentTaskID: &parentIDStr, ProfileID: "reviewer", Title: "child", Instruction: "do it",
		Mode: "advisory", Trigger: "one_shot", CreatedBy: "assistant",
	})
	if err != nil {
		t.Fatalf("build created effect: %v", err)
	}
	queued, err := taskStatusEffect(childID, assistants.StatusQueued, "")
	if err != nil {
		t.Fatalf("build queued effect: %v", err)
	}
	quota, err := quotaReservedEffect(1)
	if err != nil {
		t.Fatalf("build quota effect: %v", err)
	}
	op := state.Operation{ID: "op-child", Kind: "attach_children", Effects: []state.Effect{created, queued, quota}}
	if _, err := store.ApplyRunOperation(op, nil); err != nil { // nil externalWork == "crashed before spawn ever attempted"
		t.Fatalf("commit child (pre-crash): %v", err)
	}
	if got := store.Get(assistantsTestStageID); got == "" {
		t.Fatal("test setup sanity: stage must exist")
	}
	if s, ok := store.Snapshot().Assistants.Tasks[childID]; !ok || s.Status != assistants.StatusQueued {
		t.Fatalf("test setup: child must be queued pre-restart, got %+v", s)
	}

	store.Close() // release the flock, standing in for the OS reclaiming it on process death

	store2, err := state.Open(runDir, []string{assistantsTestStageID})
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store2.Close()
	o2 := reopenAssistantsOrchestrator(o, store2)

	var childRan bool
	o2.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if taskID == string(childID) {
			childRan = true
		}
		return passResultOutcome(), nil
	}

	o2.recoverAssistants(context.Background())

	waitForTaskStatus(t, store2, childID, assistants.StatusCompleted, 2*time.Second)
	if !childRan {
		t.Fatal("recovery must actually spawn the never-spawned queued child, not just leave it queued")
	}

	// Drain the original process's orphaned parent goroutine (still blocked on parentRelease against the now-
	// closed original store) before the test directory is cleaned up.
	close(parentRelease)
	o.concurrency.WaitAgents()
}

// TestCrash_ChildResultRecordedBeforeParentContinuation_RecoveredOnRestart — scenario 18(b).
func TestCrash_ChildResultRecordedBeforeParentContinuation_RecoveredOnRestart(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	runDir := o.opts.RunDir

	childRelease := make(chan struct{})
	var calls int
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		calls++
		if calls == 1 {
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
		}
		<-childRelease // the child's own turn — never completes through the normal path before the crash
		return passResultOutcome(), nil
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingChildren, 2*time.Second)
	childID := store.Snapshot().Assistants.ByParent[parentID][0]
	waitForTaskStatus(t, store, childID, assistants.StatusRunning, 2*time.Second)

	// Commit JUST the child's terminal status — bypassing finishTaskFrom's own externalWork (which would
	// immediately call tryResumeParent in the SAME synchronous commit) — the exact state a crash between
	// "child result durably recorded" and "parent continuation ran" would leave behind.
	finished, err := taskStatusEffect(childID, assistants.StatusCompleted, "")
	if err != nil {
		t.Fatalf("build finished effect: %v", err)
	}
	op := state.Operation{
		ID: "op-child-finish", Kind: "finish_task",
		Expect:  state.Preconditions{TaskStatus: map[string]assistants.TaskStatus{string(childID): assistants.StatusRunning}},
		Effects: []state.Effect{finished},
	}
	if _, err := store.ApplyRunOperation(op, nil); err != nil { // nil externalWork == "crashed before tryResumeParent ran"
		t.Fatalf("commit child finish (pre-crash): %v", err)
	}
	if s, ok := store.Snapshot().Assistants.Tasks[parentID]; !ok || s.Status != assistants.StatusWaitingChildren {
		t.Fatalf("test setup: parent must still be waiting_children pre-restart, got %+v", s)
	}

	store.Close()

	store2, err := state.Open(runDir, []string{assistantsTestStageID})
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store2.Close()
	o2 := reopenAssistantsOrchestrator(o, store2)

	var parentResumed bool
	o2.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if taskID == string(parentID) {
			parentResumed = true
		}
		return passResultOutcome(), nil
	}

	o2.recoverAssistants(context.Background())

	waitForTaskStatus(t, store2, parentID, assistants.StatusCompleted, 2*time.Second)
	if !parentResumed {
		t.Fatal("recovery must actually resume the parent via tryResumeParent, not leave it stuck in waiting_children")
	}

	close(childRelease)
	o.concurrency.WaitAgents()
}

// TestDuplicateChildResult_OnlyResumesParentOnce — T11.3 (§19.3 scenario 19, the "duplicate child result"
// half; duplicate child REQUEST is already covered by TestReject_SameRequestIdDoesNotCreateDuplicates). The
// child completes NORMALLY first (naturally resuming its parent exactly once) — then a REDUNDANT, late
// delivery of the SAME child's completion arrives (e.g. a duplicate completion event, or a recovery sweep
// reaching a conclusion the live path already acted on). The redundant delivery's CAS precondition
// (`from: running`) can no longer match an already-terminal child, so it must be a clean no-op: the parent
// must never be re-entered/re-resumed a second time.
func TestDuplicateChildResult_OnlyResumesParentOnce(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	var calls atomic.Int32
	var parentResumedCount atomic.Int32
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		switch calls.Add(1) {
		case 1:
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
		case 2: // the child's own real, successful turn
			return passResultOutcome(), nil
		default: // the parent's (one and only) resumed turn
			parentResumedCount.Add(1)
			return passResultOutcome(), nil
		}
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingChildren, 2*time.Second)
	childID := store.Snapshot().Assistants.ByParent[parentID][0]

	// Let the real completion run its full natural course first — child completes, parent is resumed and
	// completes too.
	waitForTaskStatus(t, store, childID, assistants.StatusCompleted, 2*time.Second)
	waitForTaskStatus(t, store, parentID, assistants.StatusCompleted, 2*time.Second)
	if n := parentResumedCount.Load(); n != 1 {
		t.Fatalf("test setup: parent's resumed turn ran %d times before the duplicate, want exactly 1", n)
	}

	// NOW a redundant, late "the child completed" delivery arrives for the SAME (already-terminal) child —
	// its own from=running precondition can no longer match, so this must be a safe no-op: tryResumeParent
	// must never fire again for an already-completed parent.
	o.finishTaskFrom(childID, assistants.StatusRunning, assistants.StatusCompleted, "")
	o.concurrency.WaitAgents()

	if n := parentResumedCount.Load(); n != 1 {
		t.Fatalf("parent's resumed turn ran %d times after a duplicate child-completion delivery, want still exactly 1 (no double-resume)", n)
	}
	if got := store.Snapshot().Assistants.Tasks[parentID].Status; got != assistants.StatusCompleted {
		t.Fatalf("parent status = %s after the duplicate delivery, want unchanged completed", got)
	}
}
