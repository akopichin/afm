package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/state"
)

// oneChild returns a single, otherwise-valid delegate child request — a test fixture shared by every
// admission test below.
func oneChild() []assistants.DelegateRequest {
	return []assistants.DelegateRequest{{
		ProfileID: "reviewer", Title: "child", Instruction: "do the sub-task",
		Mode: assistants.ModeAdvisory, Trigger: assistants.TriggerOneShot,
	}}
}

func nChildren(n int) []assistants.DelegateRequest {
	out := make([]assistants.DelegateRequest, n)
	for i := range out {
		out[i] = assistants.DelegateRequest{
			ProfileID: "reviewer", Title: fmt.Sprintf("child-%d", i), Instruction: "do the sub-task",
			Mode: assistants.ModeAdvisory, Trigger: assistants.TriggerOneShot,
		}
	}
	return out
}

// runningTask attaches a top-level task and drives it to "running" with a fake runner that blocks on
// release — used by every admission test that needs an actively-running PARENT task to delegate from
// (admitChildren's precondition requires the parent's CURRENT status, which is only meaningfully "running"
// while its own turn is in flight).
func runningParentTask(t *testing.T, o *Orchestrator, store *state.Store, release <-chan struct{}) assistants.TaskID {
	t.Helper()
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		awaitRelease(t, release)
		return passResultOutcome(), nil
	}
	taskID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, taskID, assistants.StatusRunning, 2*time.Second)
	return taskID
}

// ---- T4a.2: admission of children ----

func TestAdmission_DepthBoundary(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 2, Source: assistants.DepthSourceDefault, Revision: "rev-depth-2"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}

	release := make(chan struct{})
	defer func() {
		close(release)
		o.concurrency.WaitAgents()
	}()
	parentID := runningParentTask(t, o, store, release)

	// parent is depth 1; a child is depth 2 == MaxDepth(2) — allowed.
	childIDs, err := o.admitChildren(context.Background(), "op-child-1", parentID, oneChild(), state.Actor{Kind: "assistant", ID: string(parentID)}, "", nil)
	if err != nil {
		t.Fatalf("admitChildren at depth 2 (== max_depth): want success, got %v", err)
	}
	if len(childIDs) != 1 {
		t.Fatalf("len(childIDs) = %d, want 1", len(childIDs))
	}

	// A grandchild would be depth 3 > MaxDepth(2) — blocked.
	_, err = o.admitChildren(context.Background(), "op-grandchild-1", childIDs[0], oneChild(), state.Actor{Kind: "assistant", ID: string(childIDs[0])}, "", nil)
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeDelegationDepthExceeded {
		t.Fatalf("admitChildren at depth 3 (> max_depth 2): want CodeDelegationDepthExceeded, got %v", err)
	}
}

func TestAdmission_FlowOverrideAllowsDeeper(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	// Simulates a flow override (global=2, flow=4): the recorded policy is whatever resolveCollaboration
	// decided — admitChildren must not hardcode any depth constant of its own, it must honor whatever
	// MaxDepth got durably recorded.
	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 4, Source: assistants.DepthSourceFlow, Revision: "rev-depth-4"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}

	release := make(chan struct{})
	defer func() {
		close(release)
		o.concurrency.WaitAgents()
	}()
	cur := runningParentTask(t, o, store, release)

	// Depth 1 (parent, already running) -> 2 -> 3 -> 4: four levels total must all succeed.
	for level := 2; level <= 4; level++ {
		ids, err := o.admitChildren(context.Background(), fmt.Sprintf("op-level-%d", level), cur, oneChild(), state.Actor{Kind: "assistant", ID: string(cur)}, "", nil)
		if err != nil {
			t.Fatalf("admitChildren at depth %d (max_depth=4): want success, got %v", level, err)
		}
		cur = ids[0]
	}
	// Depth 5 must now be blocked.
	_, err := o.admitChildren(context.Background(), "op-level-5", cur, oneChild(), state.Actor{Kind: "assistant", ID: string(cur)}, "", nil)
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeDelegationDepthExceeded {
		t.Fatalf("admitChildren at depth 5 (> max_depth 4): want CodeDelegationDepthExceeded, got %v", err)
	}
}

// TestAdmission_CannotResetDepthViaNewRoot proves a delegated child is always rooted at its parent's OWN
// root stage — nothing in the request can make it a new, independent root (there is no field for it:
// RootStageID is computed from the parent, never taken from the model's request).
func TestAdmission_CannotResetDepthViaNewRoot(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	release := make(chan struct{})
	defer func() {
		close(release)
		o.concurrency.WaitAgents()
	}()
	parentID := runningParentTask(t, o, store, release)

	childIDs, err := o.admitChildren(context.Background(), "op-child", parentID, oneChild(), state.Actor{Kind: "assistant", ID: string(parentID)}, "", nil)
	if err != nil {
		t.Fatalf("admitChildren: %v", err)
	}

	rs := store.Snapshot()
	parent := rs.Assistants.Tasks[parentID]
	child := rs.Assistants.Tasks[childIDs[0]]
	if child.RootStageID != parent.RootStageID {
		t.Fatalf("child.RootStageID = %q, want parent's %q", child.RootStageID, parent.RootStageID)
	}
	if child.Depth != parent.Depth+1 {
		t.Fatalf("child.Depth = %d, want parent.Depth+1 = %d", child.Depth, parent.Depth+1)
	}
}

// TestAdmission_OnlyOwnTaskCanGetChildren: an "assistant"-origin request may only delegate to the task whose
// OWN turn is making the request (Actor.ID must equal the targeted parent task id) — creating children
// under a sibling/unrelated task as "assistant" is rejected. A "user"/"side_agent" origin (an explicit,
// human/side-agent-confirmed action — the dev-plan's "отдельное пользовательское подтверждение") is not
// subject to this self-only restriction.
func TestAdmission_OnlyOwnTaskCanGetChildren(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	release := make(chan struct{})
	defer func() {
		close(release)
		o.concurrency.WaitAgents()
	}()
	parentID := runningParentTask(t, o, store, release)

	// A different "assistant" (not the parent task itself) may not request children for it.
	_, err := o.admitChildren(context.Background(), "op-wrong-actor", parentID, oneChild(), state.Actor{Kind: "assistant", ID: "some-other-task"}, "", nil)
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeInvalidParent {
		t.Fatalf("admitChildren with a mismatched assistant actor: want CodeInvalidParent, got %v", err)
	}

	// The task's own turn (Actor.ID == parentID) succeeds.
	if _, err := o.admitChildren(context.Background(), "op-self", parentID, oneChild(), state.Actor{Kind: "assistant", ID: string(parentID)}, "", nil); err != nil {
		t.Fatalf("admitChildren with the self-matching assistant actor: %v", err)
	}

	// An explicit user action targeting the SAME task is allowed without any self-match requirement.
	if _, err := o.admitChildren(context.Background(), "op-user", parentID, oneChild(), state.Actor{Kind: "user", ID: "someone"}, "", nil); err != nil {
		t.Fatalf("admitChildren with a user actor: %v", err)
	}
}

// TestAdmission_AncestorHoldBlocksChild: pausing an ancestor between the child's creation and it actually
// acquiring the command semaphore must prevent it from ever starting — EffectivelyHeld (pkg/state) already
// walks the WHOLE parent chain, not just the immediate parent; this proves admitChildren's created child
// actually participates in that mechanism via the ordinary maybeScheduleTask/spawnTask path, not a special
// top-level-only one.
func TestAdmission_AncestorHoldBlocksChild(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	// A single-slot "helper-cmd" semaphore: the PARENT's own in-flight turn occupies the only slot, so the
	// CHILD's spawnTask goroutine queues behind it instead of running immediately — giving PauseTask (on the
	// ancestor) a window to land before the child's subprocess ever gets a chance to start. The first
	// runHelperAgent call is deterministically the parent's (it is attached and spawned before the child
	// exists at all) — same "count calls, don't match by id" technique as
	// TestPause_RunningTaskRequeuesForResumeInsteadOfFailing above.
	sem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	o.concurrency = concurrency.NewWithSemaphores(o.critical, map[string]concurrency.Semaphore{"helper-cmd": sem}, "claude")

	var calls atomic.Int32
	var childRan atomic.Bool
	parentRelease := make(chan struct{})
	parentStarted := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			close(parentStarted)
			awaitRelease(t, parentRelease)
			return passResultOutcome(), nil
		}
		childRan.Store(true)
		return passResultOutcome(), nil
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusRunning, 2*time.Second)
	select {
	case <-parentStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("parent helper did not start")
	}

	childIDs, err := o.admitChildren(context.Background(), "op-child", parentID, oneChild(), state.Actor{Kind: "assistant", ID: string(parentID)}, "", nil)
	if err != nil {
		t.Fatalf("admitChildren: %v", err)
	}

	if err := o.PauseTask(context.Background(), parentID); err != nil {
		t.Fatalf("PauseTask on the ancestor: %v", err)
	}
	close(parentRelease) // let the parent's turn finish, freeing the ONE semaphore slot the child is waiting on

	waitForTaskStatus(t, store, parentID, assistants.StatusCompleted, 2*time.Second)
	waitForTaskStatus(t, store, childIDs[0], assistants.StatusQueued, 2*time.Second)
	if childRan.Load() {
		t.Fatal("the child must not have run — its ancestor is held")
	}
}

// TestAdmission_AncestorHoldBlocksGrandchild — T11.3 (§19.3 scenario 17): the SAME ancestor-walk as
// TestAdmission_AncestorHoldBlocksChild, but one level deeper — Pause lands on the ROOT task while a
// GRANDCHILD (not a direct child) is queued behind the single command slot, racing its own admission. The
// hold must still propagate the full two hops (root -> child -> grandchild), not just to an immediate child —
// EffectivelyHeld's ancestor-walk is only genuinely exercised past depth 1 by a test like this one.
func TestAdmission_AncestorHoldBlocksGrandchild(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	// Single-slot semaphore: the ROOT's own in-flight turn occupies the only slot, so BOTH the child's and
	// the grandchild's spawnTask goroutines queue behind it — giving PauseTask (on the ROOT, two hops above
	// the grandchild) a window to land before either descendant's subprocess ever gets a chance to start.
	sem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	o.concurrency = concurrency.NewWithSemaphores(o.critical, map[string]concurrency.Semaphore{"helper-cmd": sem}, "claude")

	var calls atomic.Int32
	var childRan, grandchildRan atomic.Bool
	rootRelease := make(chan struct{})
	rootStarted := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		switch calls.Add(1) {
		case 1: // deterministically the root's turn — attached and spawned before anything else exists
			close(rootStarted)
			awaitRelease(t, rootRelease)
			return passResultOutcome(), nil
		case 2:
			childRan.Store(true)
		default:
			grandchildRan.Store(true)
		}
		return passResultOutcome(), nil
	}

	rootID := attachTestTask(context.Background(), t, o, "op-root")
	waitForTaskStatus(t, store, rootID, assistants.StatusRunning, 2*time.Second)
	select {
	case <-rootStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("root helper did not start")
	}

	childIDs, err := o.admitChildren(context.Background(), "op-child", rootID, oneChild(), state.Actor{Kind: "assistant", ID: string(rootID)}, "", nil)
	if err != nil {
		t.Fatalf("admitChildren (child under root): %v", err)
	}
	childID := childIDs[0]

	// Admit the grandchild right behind the child — admitChildren's own precondition is a CAS on the parent's
	// CURRENT status at snapshot time (whatever it happens to be: queued, or already optimistically bumped to
	// running by maybeScheduleTask while still blocked on the semaphore), not "must be running" — this is
	// legal exactly as a real delegate-while-queued race would produce, and is deliberately NOT gated on
	// observing any particular transient status here (that would itself be a race).
	grandchildIDs, err := o.admitChildren(context.Background(), "op-grandchild", childID, oneChild(), state.Actor{Kind: "assistant", ID: string(childID)}, "", nil)
	if err != nil {
		t.Fatalf("admitChildren (grandchild under child): %v", err)
	}
	grandchildID := grandchildIDs[0]

	// Pause the ROOT — two hops above the grandchild — THEN free the slot both descendants are waiting on.
	if err := o.PauseTask(context.Background(), rootID); err != nil {
		t.Fatalf("PauseTask on the root: %v", err)
	}
	close(rootRelease)

	waitForTaskStatus(t, store, rootID, assistants.StatusCompleted, 2*time.Second)
	waitForTaskStatus(t, store, childID, assistants.StatusQueued, 2*time.Second)
	waitForTaskStatus(t, store, grandchildID, assistants.StatusQueued, 2*time.Second)
	if childRan.Load() {
		t.Fatal("the child must not have run — its ancestor (root) is held")
	}
	if grandchildRan.Load() {
		t.Fatal("the grandchild must not have run — its ancestor TWO HOPS up (root) is held, not just its immediate parent")
	}
}

// TestAdmission_ConcurrentParentsRespectSharedQuota races two independently-running parent tasks, each
// admitting its own child group, against ONE shared whole-run quota — the total number of ADMITTED children
// must never exceed the configured ceiling, regardless of interleaving (-race).
func TestAdmission_ConcurrentParentsRespectSharedQuota(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	const parents, children = 2, 3
	maxTasks := parents + children // the two top-level parents count too
	o.opts.Config.Collaboration.Limits.MaxTasks = &maxTasks

	release := make(chan struct{})
	defer func() {
		close(release)
		o.concurrency.WaitAgents()
	}()
	parentA := runningParentTask(t, o, store, release)
	// A second, independent top-level parent (its own Attach, same mechanism).
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		awaitRelease(t, release)
		return passResultOutcome(), nil
	}
	parentB := attachTestTask(context.Background(), t, o, "op-parent-b")
	waitForTaskStatus(t, store, parentB, assistants.StatusRunning, 2*time.Second)

	var wg sync.WaitGroup
	var accepted atomic.Int32
	admit := func(parent assistants.TaskID, opPrefix string) {
		defer wg.Done()
		for i := 0; i < 3; i++ {
			_, err := o.admitChildren(context.Background(), fmt.Sprintf("%s-%d", opPrefix, i), parent, oneChild(), state.Actor{Kind: "assistant", ID: string(parent)}, "", nil)
			if err == nil {
				accepted.Add(1)
			}
		}
	}
	wg.Add(2)
	go admit(parentA, "op-a")
	go admit(parentB, "op-b")
	wg.Wait()

	if got := accepted.Load(); got != children {
		t.Fatalf("accepted = %d, want exactly %d (shared quota, both branches)", got, children)
	}
	rs := store.Snapshot()
	if rs.Assistants.QuotaReserved != maxTasks {
		t.Fatalf("QuotaReserved = %d, want %d", rs.Assistants.QuotaReserved, maxTasks)
	}
}

// TestAdmission_GroupIsAllOrNothing: a delegate group of 2 children against a quota ceiling that only has
// room for 1 must create NEITHER — the parent must never be left waiting on a partially-created group with
// unknown/unsaved ids.
func TestAdmission_GroupIsAllOrNothing(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	maxTasks := 2 // the parent and one child
	o.opts.Config.Collaboration.Limits.MaxTasks = &maxTasks

	release := make(chan struct{})
	defer func() {
		close(release)
		o.concurrency.WaitAgents()
	}()
	parentID := runningParentTask(t, o, store, release)

	_, err := o.admitChildren(context.Background(), "op-group", parentID, nChildren(2), state.Actor{Kind: "assistant", ID: string(parentID)}, "", nil)
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeBudgetExceeded {
		t.Fatalf("admitChildren for a 2-child group over a 1-task quota: want CodeBudgetExceeded, got %v", err)
	}

	rs := store.Snapshot()
	if len(rs.Assistants.ByParent[parentID]) != 0 {
		t.Fatalf("ByParent[parent] = %v, want no children created at all (all-or-nothing)", rs.Assistants.ByParent[parentID])
	}
	if rs.Assistants.QuotaReserved != 1 {
		t.Fatalf("QuotaReserved = %d, want only the parent's 1 (rejected group must not reserve anything)", rs.Assistants.QuotaReserved)
	}
}
