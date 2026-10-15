package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/state"
)

// waitForTrue polls fn until it reports true or timeout elapses — used where the condition is not a durable
// projected status (e.g. an in-memory atomic flag a fake runHelperAgent sets), so waitForTaskStatus's
// Snapshot-based polling does not apply.
func waitForTrue(t *testing.T, fn func() bool, timeout time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !fn() {
		t.Fatal(msg)
	}
}

// childJSON renders one delegate child request with the given mode — a test fixture for the delegate-turn
// raw text the fake runHelperAgent returns.
func childJSON(mode assistants.Mode) string {
	return fmt.Sprintf(`{"profile_id":"reviewer","title":"child","instruction":"do it","mode":%q,"trigger":"one_shot"}`, mode)
}

// delegateOutcome builds a HelperOutcome whose RawText is a kind:"delegate" turn requesting the given
// children (already-rendered JSON objects, comma-joined by the caller) with an on_completion continuation.
func delegateOutcome(childrenJSON ...string) executor.HelperOutcome {
	children := ""
	for i, c := range childrenJSON {
		if i > 0 {
			children += ","
		}
		children += c
	}
	raw := `{"schema_version":1,"kind":"delegate","children":[` + children +
		`],"continuation":{"trigger":"on_completion"}}`
	return executor.HelperOutcome{ProcessOK: true, RawText: raw}
}

// ---- T4a.3: waiting_children + durable continuation ----

func TestWaitingChildren_PersistedWithExpectedSet(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	var calls atomic.Int32
	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			return delegateOutcome(childJSON(assistants.ModeAdvisory), childJSON(assistants.ModeAdvisory)), nil
		}
		<-release
		return passResultOutcome(), nil
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingChildren, 2*time.Second)

	rs := store.Snapshot()
	parent := rs.Assistants.Tasks[parentID]
	if parent.Continuation == nil || parent.Continuation.Trigger != assistants.TriggerOnCompletion {
		t.Fatalf("Continuation = %+v, want a persisted on_completion continuation", parent.Continuation)
	}
	children := rs.Assistants.ByParent[parentID]
	if len(children) != 2 {
		t.Fatalf("ByParent[parent] = %v, want exactly 2 children", children)
	}
	for _, cid := range children {
		// "queued" or already optimistically "running" (maybeScheduleTask's queued->running CAS commits
		// before the goroutine that will actually invoke the helper is even guaranteed to have been
		// scheduled, same as every other admission test above) — either is "eligible to run", which is the
		// actual claim here; "waiting_parent" would mean admitChildren never activated them at all.
		c, ok := rs.Assistants.Tasks[cid]
		if !ok || c.Status == assistants.StatusWaitingParent {
			t.Fatalf("child %s = %+v, want it already activated (queued/running), not waiting_parent", cid, c)
		}
	}

	close(release)
	o.concurrency.WaitAgents()
}

// TestWaitingChildren_NewRequiredChildReopensReadiness: a required child added WHILE the parent is already
// waiting (simulating a race between two admissions) must be included in the readiness check — resuming the
// parent as soon as the FIRST child finishes (ignoring the second, newer one) would silently drop it.
func TestWaitingChildren_NewRequiredChildReopensReadiness(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	var calls atomic.Int32
	release1 := make(chan struct{})
	release2 := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		switch calls.Add(1) {
		case 1:
			return delegateOutcome(childJSON(assistants.ModeRequired)), nil
		case 2:
			<-release1
			return passResultOutcome(), nil
		default:
			<-release2
			return passResultOutcome(), nil
		}
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingChildren, 2*time.Second)

	firstChildren := append([]assistants.TaskID(nil), store.Snapshot().Assistants.ByParent[parentID]...)
	if len(firstChildren) != 1 {
		t.Fatalf("ByParent[parent] = %v, want exactly 1 child from the first delegate turn", firstChildren)
	}

	secondChildren, err := o.admitChildren(context.Background(), "op-extra-child", parentID,
		[]assistants.DelegateRequest{{ProfileID: "reviewer", Title: "late child", Instruction: "do it too", Mode: assistants.ModeRequired, Trigger: assistants.TriggerOneShot}},
		state.Actor{Kind: "assistant", ID: string(parentID)}, "", nil)
	if err != nil {
		t.Fatalf("admitChildren (racing addition): %v", err)
	}

	close(release1)
	waitForTaskStatus(t, store, firstChildren[0], assistants.StatusCompleted, 2*time.Second)
	// The FIRST child alone finishing must NOT resume the parent — the second (required) child is still open.
	if got := store.Snapshot().Assistants.Tasks[parentID].Status; got != assistants.StatusWaitingChildren {
		t.Fatalf("parent status = %s after only the first child finished, want still waiting_children (second required child still open)", got)
	}

	close(release2)
	waitForTaskStatus(t, store, secondChildren[0], assistants.StatusCompleted, 2*time.Second)
	waitForTaskStatus(t, store, parentID, assistants.StatusCompleted, 2*time.Second)
}

// TestWaitingChildren_NotAncestorInactive: a parent in waiting_children is NOT "inactive" for admission
// purposes (admitChildren's CodeAncestorInactive gate only fires for a TERMINAL parent, dev-plan's distinct
// concept from "waiting") — more children can still be admitted under it, and its existing child is free to
// actually run (never blocked by its own parent's waiting_children status).
func TestWaitingChildren_NotAncestorInactive(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	var calls atomic.Int32
	release := make(chan struct{})
	var childRan atomic.Bool
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
		}
		childRan.Store(true)
		<-release
		return passResultOutcome(), nil
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingChildren, 2*time.Second)
	// childRan, not the child's projected status: the queued->running CAS commits optimistically before the
	// goroutine that actually invokes the fake is guaranteed to have been scheduled (same caveat as every
	// other admission test above) — childRan is what actually proves the fake ran.
	waitForTrue(t, childRan.Load, 2*time.Second, "the existing child must be free to run while its parent is merely waiting_children")

	if _, err := o.admitChildren(context.Background(), "op-more", parentID, oneChild(), state.Actor{Kind: "assistant", ID: string(parentID)}, "", nil); err != nil {
		t.Fatalf("admitChildren under a waiting_children parent: want success (not CodeAncestorInactive), got %v", err)
	}

	close(release)
	o.concurrency.WaitAgents()
}

// TestWaitingChildren_RestoredAfterRestart: waiting_children + its saved continuation + the ByParent set
// survive a store close/reopen (replay from events.jsonl) exactly as projected live — Инвариант №1.
func TestWaitingChildren_RestoredAfterRestart(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	runDir := o.opts.RunDir

	var calls atomic.Int32
	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
		}
		<-release
		return passResultOutcome(), nil
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingChildren, 2*time.Second)
	childID := store.Snapshot().Assistants.ByParent[parentID][0]

	// Simulate a crash WHILE the child is still mid-flight (blocked on release, never completes) — close the
	// journal out from under it without waiting for that goroutine first. This is deliberately NOT the
	// graceful shutdown sequence (cancel -> WaitAgents -> Close): a real process crash gives recovery no such
	// courtesy either, and that is exactly the scenario this test needs to reproduce.
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := state.Open(runDir, []string{assistantsTestStageID})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}

	rs := reopened.Snapshot()
	parent, ok := rs.Assistants.Tasks[parentID]
	if !ok || parent.Status != assistants.StatusWaitingChildren {
		t.Fatalf("restored parent = %+v, want waiting_children", parent)
	}
	if parent.Continuation == nil || parent.Continuation.Trigger != assistants.TriggerOnCompletion {
		t.Fatalf("restored Continuation = %+v, want the persisted on_completion continuation", parent.Continuation)
	}
	if got := rs.Assistants.ByParent[parentID]; len(got) != 1 || got[0] != childID {
		t.Fatalf("restored ByParent[parent] = %v, want [%s]", got, childID)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened: %v", err)
	}

	// Let the orphaned goroutine from the ORIGINAL (now-closed) store unwind — its eventual write against a
	// closed journal is a benign, already-handled no-op (handleTaskOperationErr); this just drains it before
	// the test's TempDir cleanup runs.
	close(release)
	o.concurrency.WaitAgents()
}

// TestWaitingChildren_FailedChildDelivered: a child that finishes WITHOUT a result (runner failure) must
// still unblock its waiting parent — the parent would otherwise wait forever for a "pass" that can never
// come — and the resumed parent's next context must mention the failure (not silently omit it).
func TestWaitingChildren_FailedChildDelivered(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	var calls atomic.Int32
	var resumedPrompt string
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		switch calls.Add(1) {
		case 1:
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
		case 2:
			return executor.HelperOutcome{}, errors.New("boom: child runner crashed")
		default:
			resumedPrompt = prompt
			return passResultOutcome(), nil
		}
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingChildren, 2*time.Second)
	childID := store.Snapshot().Assistants.ByParent[parentID][0]
	waitForTaskStatus(t, store, childID, assistants.StatusFailed, 2*time.Second)

	waitForTaskStatus(t, store, parentID, assistants.StatusCompleted, 2*time.Second)
	if resumedPrompt == "" {
		t.Fatal("the parent's resumed turn never ran")
	}
	if !strings.Contains(resumedPrompt, "boom: child runner crashed") {
		t.Fatalf("resumed prompt does not mention the failed child's reason: %q", resumedPrompt)
	}
}

// TestContinuation_ParentPassRejectedWithOpenRequiredChild is the orchestrator-level enforcement of
// dev-plan.md's TestResult_ConsistencyWithRequiredChildren (T4a.1): pkg/assistants.DecodeResult cannot see a
// task's children at all (see pkg/assistants/delegate_test.go's note), so the actual gate lives here — a
// parent's resumed turn claiming verdict:"pass" while its required child did NOT itself finish with pass
// must be rejected, not silently accepted.
func TestContinuation_ParentPassRejectedWithOpenRequiredChild(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	var calls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		switch calls.Add(1) {
		case 1:
			return delegateOutcome(childJSON(assistants.ModeRequired)), nil
		case 2: // the required child itself reports needs_changes, never pass
			return executor.HelperOutcome{ProcessOK: true, RawText: `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"not done yet"}`}, nil
		default: // the resumed parent dishonestly claims pass anyway
			return passResultOutcome(), nil
		}
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingChildren, 2*time.Second)
	childID := store.Snapshot().Assistants.ByParent[parentID][0]
	waitForTaskStatus(t, store, childID, assistants.StatusCompleted, 2*time.Second)

	tk := waitForTaskStatus(t, store, parentID, assistants.StatusFailed, 2*time.Second)
	if !strings.Contains(tk.StatusReason, "protocol_error") || !strings.Contains(tk.StatusReason, "required") {
		t.Fatalf("reason = %q, want it to reject the dishonest pass over an unresolved required child", tk.StatusReason)
	}
}

// ---- T4a.4: lease/slot released before waiting ----

// TestLease_ReleasedBeforeWaitingChildren proves a parent's turn ending in waiting_children releases its
// command-slot semaphore immediately — with a single-capacity semaphore, its child (same command) must be
// able to acquire that exact slot right away, with no further action from the parent.
func TestLease_ReleasedBeforeWaitingChildren(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	sem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	o.concurrency = concurrency.NewWithSemaphores(o.critical, map[string]concurrency.Semaphore{"helper-cmd": sem}, "claude")

	var calls atomic.Int32
	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
		}
		<-release
		return passResultOutcome(), nil
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingChildren, 2*time.Second)
	children := store.Snapshot().Assistants.ByParent[parentID]
	// The child must reach "running" (i.e. actually acquire the ONE semaphore slot) without the test ever
	// freeing anything else — proof the parent's own lease was already released.
	waitForTaskStatus(t, store, children[0], assistants.StatusRunning, 2*time.Second)

	close(release)
	o.concurrency.WaitAgents()
}

// TestSingleSlot_ChainCompletes: max_parallel effectively 1 end-to-end — stage -> helper -> child ->
// grandchild — must complete with no deadlock. The whole test itself is the timeout: go test's own per-test
// deadline turns a real deadlock into a clean failure rather than a hang.
func TestSingleSlot_ChainCompletes(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	sem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	o.concurrency = concurrency.NewWithSemaphores(o.critical, map[string]concurrency.Semaphore{"helper-cmd": sem}, "claude")
	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 3, Source: assistants.DepthSourceDefault, Revision: "rev-chain"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}

	var calls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		switch calls.Add(1) {
		case 1, 2: // helper, then child — each delegates one more level
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
		default: // grandchild — ends the chain
			return passResultOutcome(), nil
		}
	}

	helperID := attachTestTask(context.Background(), t, o, "op-helper")
	waitForTaskStatus(t, store, helperID, assistants.StatusCompleted, 5*time.Second)
}

// TestWaitingChildren_DoesNotCountInActiveInvocations: a parent sitting in waiting_children is not spuriously
// treated as "the stage's own active agent" — IsActive only ever looks at "stage:" owner keys (T3.3's
// existing separation), so a long wait here can never masquerade as the stage's own runner still working.
func TestWaitingChildren_DoesNotCountInActiveInvocations(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	var calls atomic.Int32
	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
		}
		<-release
		return passResultOutcome(), nil
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingChildren, 2*time.Second)
	if o.concurrency.IsActive(assistantsTestStageID) {
		t.Fatal("a waiting_children helper task must never be visible through the STAGE's own IsActive check")
	}

	close(release)
	o.concurrency.WaitAgents()
}

// TestChildDeadlines_DoNotExtendParent: once a parent reaches waiting_children, its own in-flight-invocation
// cancel func is cleared (runHelperTask's defer, same cleanup path every turn already uses) — nothing about
// a child's (potentially long) run keeps the parent's OWN last turn's context/deadline alive or extends it.
func TestChildDeadlines_DoNotExtendParent(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	var calls atomic.Int32
	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
		}
		<-release
		return passResultOutcome(), nil
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingChildren, 2*time.Second)

	// The status commit (inside admitChildren) and runHelperTask's own deferred taskCancels.Delete are two
	// separate steps in the SAME unwinding goroutine — status is visible a hair before the defer actually
	// runs, so this is a short, bounded poll, not an instantaneous check.
	waitForTrue(t, func() bool {
		_, ok := o.taskCancels.Load(parentID)
		return !ok
	}, 2*time.Second, "the parent's own (now-finished) turn must not keep a live cancel func registered while its children run")

	close(release)
	o.concurrency.WaitAgents()
}
