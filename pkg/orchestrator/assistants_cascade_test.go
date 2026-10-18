package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/state"
)

// ---- T4a.6: recursive holds/cancel + generation invalidation ----

// TestPause_CascadesToSubtree: pausing branch A (a task that has already delegated to a running child) must
// stop BOTH the branch's own future turns AND its already-running child — while an entirely independent
// branch B keeps working, untouched.
func TestPause_CascadesToSubtree(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	// Both branches' helper command ("helper-cmd", the "reviewer" profile's command — unregistered with any
	// stage) would otherwise share concurrency's single lazily-created capacity-1 dynamic semaphore
	// (semForCmd's conservative default for any command no stage ever declared) — give it real headroom so
	// branch A's child and branch B can genuinely run at the same time, which this test depends on.
	o.concurrency = concurrency.NewWithSemaphores(o.critical, map[string]concurrency.Semaphore{"helper-cmd": concurrency.ChannelSemaphore(make(chan struct{}, 2))}, "claude")
	parentAID := deriveTaskID("op-parentA")
	parentBID := deriveTaskID("op-parentB")

	releaseB := make(chan struct{})
	releaseChild := make(chan struct{})
	childStarted := make(chan struct{}, 1)
	bStarted := make(chan struct{}, 1)
	var childSawCancel, bSawCancel atomic.Bool
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		switch taskID {
		case string(parentAID):
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
		case string(parentBID):
			bStarted <- struct{}{}
			select {
			case <-ctx.Done():
				bSawCancel.Store(true)
				return executor.HelperOutcome{}, nil
			case <-releaseB:
				return passResultOutcome(), nil
			}
		default: // the child of branch A
			// Signal BEFORE selecting — taskCancels is populated (runHelperTask's very first steps) well
			// before this fake body ever runs, but the task's PROJECTED status flips to "running" optimistically
			// even earlier than that (maybeScheduleTask's own CAS, same gap documented throughout this test
			// file) — waiting on the status alone would let PauseTask race ahead of taskCancels actually being
			// populated and find nothing to cancel.
			childStarted <- struct{}{}
			select {
			case <-ctx.Done():
				childSawCancel.Store(true)
				return executor.HelperOutcome{}, nil
			case <-releaseChild:
				return passResultOutcome(), nil
			}
		}
	}

	parentA := attachTestTask(context.Background(), t, o, "op-parentA")
	parentB := attachTestTask(context.Background(), t, o, "op-parentB")
	waitForTaskStatus(t, store, parentA, assistants.StatusWaitingChildren, 2*time.Second)
	childID := store.Snapshot().Assistants.ByParent[parentA][0]
	<-childStarted
	<-bStarted

	if err := o.PauseTask(context.Background(), parentA); err != nil {
		t.Fatalf("PauseTask: %v", err)
	}

	// The child, held only through its ancestor, must be interrupted and requeued — never left running.
	waitForTaskStatus(t, store, childID, assistants.StatusQueued, 2*time.Second)
	waitForTrue(t, childSawCancel.Load, 2*time.Second, "the child must have observed cancellation from the ancestor's pause")

	// Branch B must be completely unaffected.
	time.Sleep(30 * time.Millisecond)
	if bSawCancel.Load() {
		t.Fatal("an unrelated sibling branch must not be interrupted by pausing branch A")
	}

	close(releaseB)
	waitForTaskStatus(t, store, parentB, assistants.StatusCompleted, 2*time.Second)
	close(releaseChild) // drain the (already requeued/held) child's leaked goroutine, if still referenced
	o.concurrency.WaitAgents()
}

// TestContinue_DoesNotClearOtherHolds: Continue on an ANCESTOR clears only that ancestor's own "manual" hold
// — a descendant separately paused in its own right keeps its own hold and stays paused.
func TestContinue_DoesNotClearOtherHolds(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	parentID := deriveTaskID("op-parent")
	releaseChild := make(chan struct{})
	childStarted := make(chan struct{}, 1)
	var parentDelegated atomic.Bool
	var childRanToCompletion atomic.Bool
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if taskID == string(parentID) {
			// The parent's FIRST turn delegates; its RESUMED turn (after the child eventually completes)
			// just reports pass — distinguished by task id and a one-shot flag, not call order, so the
			// child's own pause/continue/re-run cycle below can never be mistaken for a second parent turn.
			if !parentDelegated.Swap(true) {
				return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
			}
			return passResultOutcome(), nil
		}
		// Signal BEFORE selecting: taskCancels is populated well before this fake body runs, but the
		// PROJECTED status flips to "running" optimistically even earlier than that — PauseTask racing ahead
		// of taskCancels being populated would find nothing to cancel (see this file's other tests).
		select {
		case childStarted <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			return executor.HelperOutcome{}, nil
		case <-releaseChild:
			childRanToCompletion.Store(true)
			return passResultOutcome(), nil
		}
	}

	attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingChildren, 2*time.Second)
	childID := store.Snapshot().Assistants.ByParent[parentID][0]
	<-childStarted

	if err := o.PauseTask(context.Background(), childID); err != nil {
		t.Fatalf("PauseTask(child): %v", err)
	}
	waitForTaskStatus(t, store, childID, assistants.StatusQueued, 2*time.Second)
	if childRanToCompletion.Load() {
		t.Fatal("the child must have been interrupted by its own PauseTask, not run to completion")
	}

	if err := o.PauseTask(context.Background(), parentID); err != nil {
		t.Fatalf("PauseTask(parent): %v", err)
	}
	if err := o.ContinueTask(context.Background(), parentID); err != nil {
		t.Fatalf("ContinueTask(parent): %v", err)
	}

	// The child's OWN hold must still be in effect — Continue on the parent must not have touched it.
	time.Sleep(30 * time.Millisecond)
	held, _ := store.Snapshot().Assistants.EffectivelyHeld(childID)
	if !held {
		t.Fatal("the child's own manual hold must still be in effect after Continue on its parent")
	}
	if got := store.Snapshot().Assistants.Tasks[childID].Status; got != assistants.StatusQueued {
		t.Fatalf("child status = %s, want still queued (held)", got)
	}

	if err := o.ContinueTask(context.Background(), childID); err != nil {
		t.Fatalf("ContinueTask(child): %v", err)
	}
	close(releaseChild)
	waitForTaskStatus(t, store, childID, assistants.StatusCompleted, 2*time.Second)
	waitForTaskStatus(t, store, parentID, assistants.StatusCompleted, 2*time.Second)
}

// TestCancel_CascadesAndKeepsHistory: cancelling a branch cancels its running child too, and a repeated
// Cancel afterward is a no-op that never rewrites the now-terminal history.
func TestCancel_CascadesAndKeepsHistory(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	var calls atomic.Int32
	releaseChild := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
		}
		<-releaseChild
		return passResultOutcome(), nil
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingChildren, 2*time.Second)
	childID := store.Snapshot().Assistants.ByParent[parentID][0]
	waitForTaskStatus(t, store, childID, assistants.StatusRunning, 2*time.Second)

	if err := o.CancelTask(context.Background(), parentID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	waitForTaskStatus(t, store, parentID, assistants.StatusCancelled, 2*time.Second)
	waitForTaskStatus(t, store, childID, assistants.StatusCancelled, 2*time.Second)

	if err := o.CancelTask(context.Background(), parentID); err != nil {
		t.Fatalf("CancelTask (repeated, on terminal task): %v", err)
	}
	rs := store.Snapshot()
	if rs.Assistants.Tasks[parentID].Status != assistants.StatusCancelled {
		t.Fatalf("parent status = %s, want cancelled (repeated cancel must not change it)", rs.Assistants.Tasks[parentID].Status)
	}
	if rs.Assistants.Tasks[childID].Status != assistants.StatusCancelled {
		t.Fatalf("child status = %s, want cancelled (history preserved)", rs.Assistants.Tasks[childID].Status)
	}

	close(releaseChild)
	o.concurrency.WaitAgents()
}

// TestReviseRoot_InvalidatesRootGenerationAcrossTree: bumping a root stage's generation cancels its whole
// existing tree and ensures a FRESH top-level task attached afterward is stamped with the NEW generation —
// old results can never again satisfy a new required check (see currentChildren's generation filter).
// BumpRootGeneration is exercised directly here — wiring an automatic call site into the stage-level
// Revise/retry flow is a deliberately separate integration step (see dev-plan.md's T4a.6 note).
func TestReviseRoot_InvalidatesRootGenerationAcrossTree(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	var calls atomic.Int32
	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			return delegateOutcome(childJSON(assistants.ModeRequired)), nil
		}
		<-release
		return passResultOutcome(), nil
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingChildren, 2*time.Second)
	childID := store.Snapshot().Assistants.ByParent[parentID][0]
	waitForTaskStatus(t, store, childID, assistants.StatusRunning, 2*time.Second)

	if err := o.BumpRootGeneration(assistantsTestStageID); err != nil {
		t.Fatalf("BumpRootGeneration: %v", err)
	}
	waitForTaskStatus(t, store, parentID, assistants.StatusCancelled, 2*time.Second)
	waitForTaskStatus(t, store, childID, assistants.StatusCancelled, 2*time.Second)

	rs := store.Snapshot()
	if rs.Assistants.RootGenerations[assistantsTestStageID] != 1 {
		t.Fatalf("RootGenerations[%s] = %d, want 1", assistantsTestStageID, rs.Assistants.RootGenerations[assistantsTestStageID])
	}
	if rs.Assistants.Tasks[parentID].RootGeneration != 0 {
		t.Fatalf("old parent's RootGeneration = %d, want 0 (stamped once, never rewritten)", rs.Assistants.Tasks[parentID].RootGeneration)
	}

	newParentID := attachTestTask(context.Background(), t, o, "op-parent-2")
	rs2 := store.Snapshot()
	if got := rs2.Assistants.Tasks[newParentID].RootGeneration; got != 1 {
		t.Fatalf("new top-level task's RootGeneration = %d, want 1 (post-bump)", got)
	}

	close(release)
	o.concurrency.WaitAgents()
}

// TestLateResult_AfterGenerationBump_DoesNotResurrectCancelledTask — T11.3 (§19.3 scenario 5): the SAME setup
// as TestReviseRoot_InvalidatesRootGenerationAcrossTree, but actually observing what happens once the child's
// stale, already-in-flight runHelperAgent call finally returns a SUCCESSFUL result AFTER BumpRootGeneration
// already cancelled it. A late pass/fail/anything result for a task whose generation has already moved on
// must never resurrect it from "cancelled" back to "completed", and must never write a result file or emit a
// task_result event for the superseded attempt.
func TestLateResult_AfterGenerationBump_DoesNotResurrectCancelledTask(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	var calls atomic.Int32
	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			return delegateOutcome(childJSON(assistants.ModeRequired)), nil
		}
		<-release
		// The stale subprocess "wins the race" and reports a clean PASS — exactly as if it had been about to
		// succeed right as the generation bump cancelled it.
		return passResultOutcome(), nil
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingChildren, 2*time.Second)
	childID := store.Snapshot().Assistants.ByParent[parentID][0]
	waitForTaskStatus(t, store, childID, assistants.StatusRunning, 2*time.Second)

	if err := o.BumpRootGeneration(assistantsTestStageID); err != nil {
		t.Fatalf("BumpRootGeneration: %v", err)
	}
	waitForTaskStatus(t, store, childID, assistants.StatusCancelled, 2*time.Second)

	// NOW let the stale subprocess call return its late, successful result.
	close(release)
	o.concurrency.WaitAgents()

	rs := store.Snapshot()
	if got := rs.Assistants.Tasks[childID].Status; got != assistants.StatusCancelled {
		t.Fatalf("child status = %s, want still cancelled — a late PASS result must never resurrect a superseded-generation task", got)
	}
	if path := filepath.Join(o.taskDir(childID), "result.json"); fileExists(path) {
		t.Fatalf("result.json must never be written for a task whose generation was already superseded: %s", path)
	}
}

// TestRetryTask_InvalidatesOwnSubtreeNotSiblings: retrying a failed task bumps ITS OWN TaskGeneration and
// invalidates ITS OWN (still non-terminal) descendants — an unrelated sibling branch is completely
// untouched.
func TestRetryTask_InvalidatesOwnSubtreeNotSiblings(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	var calls atomic.Int32
	releaseRest := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		switch calls.Add(1) {
		case 1: // the parent's first turn: delegate to one required child
			return delegateOutcome(childJSON(assistants.ModeRequired)), nil
		case 2: // the required child itself crashes
			return executor.HelperOutcome{}, errors.New("boom: child crashed")
		case 3: // the parent's resumed turn sends garbage -> protocol_error -> failed
			return executor.HelperOutcome{ProcessOK: true, RawText: "not json"}, nil
		default: // the sibling branch, and later the parent's RETRIED turn
			<-releaseRest
			return passResultOutcome(), nil
		}
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingChildren, 2*time.Second)
	childID := store.Snapshot().Assistants.ByParent[parentID][0]
	waitForTaskStatus(t, store, childID, assistants.StatusFailed, 2*time.Second)
	waitForTaskStatus(t, store, parentID, assistants.StatusFailed, 2*time.Second)

	siblingID := attachTestTask(context.Background(), t, o, "op-sibling")
	waitForTaskStatus(t, store, siblingID, assistants.StatusRunning, 2*time.Second)

	if err := o.RetryTask(context.Background(), parentID); err != nil {
		t.Fatalf("RetryTask: %v", err)
	}
	waitForTaskStatus(t, store, parentID, assistants.StatusRunning, 2*time.Second)

	rs := store.Snapshot()
	if got := rs.Assistants.Tasks[parentID].TaskGeneration; got != 1 {
		t.Fatalf("parent TaskGeneration = %d, want 1 after retry", got)
	}
	if got := rs.Assistants.Tasks[childID].Status; got != assistants.StatusFailed {
		t.Fatalf("old child's history = %s, want still failed (never rewritten)", got)
	}
	if got := rs.Assistants.Tasks[siblingID].Status; got == assistants.StatusCancelled {
		t.Fatal("RetryTask must never touch an unrelated sibling branch")
	}

	close(releaseRest)
	waitForTaskStatus(t, store, siblingID, assistants.StatusCompleted, 2*time.Second)
	waitForTaskStatus(t, store, parentID, assistants.StatusCompleted, 2*time.Second)
	o.concurrency.WaitAgents()
}

// TestRetryTask_RejectsNonFailedTask: only a FAILED task can be retried.
func TestRetryTask_RejectsNonFailedTask(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}
	taskID := attachTestTask(context.Background(), t, o, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)

	err := o.RetryTask(context.Background(), taskID)
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeInvalidStatus {
		t.Fatalf("RetryTask on a completed (never failed) task: want CodeInvalidStatus, got %v", err)
	}
}

// TestRunEnd_AdvisoryTasksDoNotBlockForever: an advisory task still "running" when the run's own context is
// cancelled must observe that cancellation promptly and let WaitAgents' bounded drain return quickly — not
// hang until the drain timeout, and never block run shutdown. The cut-short turn is not recorded as a failure.
func TestRunEnd_AdvisoryTasksDoNotBlockForever(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	runCtx, cancelRun := context.WithCancel(context.Background())
	o.runMu.Lock()
	o.runCtx = runCtx
	o.runMu.Unlock()

	started := make(chan struct{})
	var sawCancel atomic.Bool
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		close(started)
		<-ctx.Done()
		sawCancel.Store(true)
		return executor.HelperOutcome{}, nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-1") // advisory by default
	<-started

	done := make(chan struct{})
	go func() {
		cancelRun()
		o.concurrency.WaitAgents()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitAgents did not return promptly after run-ctx cancellation — an advisory task blocked the drain")
	}
	if !sawCancel.Load() {
		t.Fatal("the advisory task's own ctx must have observed the run-wide cancellation")
	}
	// An interrupted run did not fail the task: it stays running and is re-queued when the run resumes
	// (recoverAssistants), see TestInterruptedRun_TaskMidTurnIsRetriedOnResume.
	rs := store.Snapshot()
	if tk := rs.Assistants.Tasks[taskID]; tk.Status != assistants.StatusRunning {
		t.Fatalf("task status = %s after the run was interrupted, want running (retried on resume, not failed)", tk.Status)
	}
}
