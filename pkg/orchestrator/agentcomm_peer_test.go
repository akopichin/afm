package orchestrator

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 5, T5.4: helper-side waiting_peer — the helper-task twin of T5.2/T5.3's stage-level
// awaiting_peer. A helper task that registers a mesh Wait on a colleague and exits cooperatively must not be
// treated as a protocol error, must release its concurrency slot, and must resume (re-running runHelperTask
// from scratch, which rebuilds the task's full context including its mesh inbox — T4.3/T4.4) once the wait
// resolves.

// TestHelperYield_ParksWaitingPeerNotProtocolError is T5.4's central claim, the helper-task analogue of
// TestRunWithRetry_MeshWaitParksAwaitingPeerNotFalseCompletion: a helper that registers a wait and exits with
// NO result/delegate envelope must land in waiting_peer, never failed/protocol_error.
func TestHelperYield_ParksWaitingPeerNotProtocolError(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	taskID := deriveTaskID("op-peer-1")
	helperID := meshHelperParticipantID(taskID)
	o.runHelperAgent = func(ctx context.Context, tid, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if _, err := o.Wait(context.Background(), WaitRequest{
			OperationID: "op-peer-1-wait", Waiter: helperID,
			Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-does-not-exist-yet"},
		}); err != nil {
			t.Errorf("Wait: %v", err)
		}
		// No result/delegate envelope at all — exactly what a cooperatively-yielding agent produces.
		return executor.HelperOutcome{ProcessOK: true}, nil
	}

	attachTestTask(ctx, t, o, "op-peer-1")
	tk := waitForTaskStatus(t, store, taskID, assistants.StatusWaitingPeer, 2*time.Second)
	if tk.Status == assistants.StatusFailed {
		t.Fatal("a yield on an open mesh wait must never be classified as a protocol error")
	}
}

// TestHelperYield_ReleasesSlotForQueuedPeer proves the yield actually releases the command semaphore slot
// (not just the durable status) — mirrors TestPause_CascadesToSubtree's concurrency-capacity setup: "reviewer"
// and "qa" (this package's two test profiles) share the SAME command ("helper-cmd"), capped to capacity 1, so
// the second task can only ever start if the first one's slot was genuinely freed.
func TestHelperYield_ReleasesSlotForQueuedPeer(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	o.concurrency = concurrency.NewWithSemaphores(o.critical, map[string]concurrency.Semaphore{
		"helper-cmd": concurrency.ChannelSemaphore(make(chan struct{}, 1)),
	}, "claude")

	firstTaskID := deriveTaskID("op-peer-slot-1")
	secondStarted := make(chan struct{}, 1)
	o.runHelperAgent = func(ctx context.Context, tid, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if tid == string(firstTaskID) {
			if _, err := o.Wait(context.Background(), WaitRequest{
				OperationID: "op-peer-slot-wait", Waiter: meshHelperParticipantID(assistants.TaskID(tid)),
				Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-does-not-exist-yet"},
			}); err != nil {
				t.Errorf("Wait: %v", err)
			}
			return executor.HelperOutcome{ProcessOK: true}, nil
		}
		select {
		case secondStarted <- struct{}{}:
		default:
		}
		return passResultOutcome(), nil
	}

	attachTestTask(ctx, t, o, "op-peer-slot-1")
	waitForTaskStatus(t, store, firstTaskID, assistants.StatusWaitingPeer, 2*time.Second)

	attachTestTask(ctx, t, o, "op-peer-slot-2")
	select {
	case <-secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("the second task never got to run — the first task's concurrency slot was not released on yield")
	}
	o.concurrency.WaitAgents()
}

// TestHelperWait_ResumesOnReplyAndCompletes is T5.4's end-to-end resume claim: a reply satisfying the
// registered wait makes runHelperTask run AGAIN for the SAME task id, from a fresh, fully rebuilt context
// (buildTaskContext — no separate "continuation payload" mechanism is needed, see T4.3/T4.4's inbox wiring),
// and the task reaches completed.
func TestHelperWait_ResumesOnReplyAndCompletes(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	taskID := deriveTaskID("op-peer-resume")
	helperID := meshHelperParticipantID(taskID)
	colleague := agentcomm.ParticipantID("user")
	threadID := deriveMeshThreadID("op-peer-resume-open")

	var calls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, tid, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if tid != string(taskID) {
			return passResultOutcome(), nil
		}
		if calls.Add(1) == 1 {
			if _, err := o.Wait(context.Background(), WaitRequest{
				OperationID: "op-peer-resume-wait", Waiter: helperID,
				Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: threadID},
			}); err != nil {
				t.Errorf("Wait: %v", err)
			}
			return executor.HelperOutcome{ProcessOK: true}, nil
		}
		return passResultOutcome(), nil
	}

	attachTestTask(ctx, t, o, "op-peer-resume")
	waitForTaskStatus(t, store, taskID, assistants.StatusWaitingPeer, 2*time.Second)

	if _, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-peer-resume-open", Members: []agentcomm.ParticipantID{helperID, colleague}, Scope: agentcomm.ScopeStage,
	}); err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-peer-resume-send", ThreadID: threadID, Sender: colleague,
		Recipients: []agentcomm.ParticipantID{helperID}, Kind: agentcomm.MessageKindMessage, Body: "qa says: ready",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	if got := calls.Load(); got != 2 {
		t.Fatalf("runHelperAgent invoked %d times, want exactly 2 (one yield, one resumed turn)", got)
	}
	o.concurrency.WaitAgents()
}

// TestTryResumePeer_ConcurrentCallsResumeExactlyOnce is dev-plan's own T5.4 claim: "tryResumePeer uses a
// TaskStatus precondition so only one winner requeues". CancelWait resolves the wait WITHOUT an automatic
// resume (unlike Send/Publish), which is exactly what reproduces the race tryResumePeer's CAS exists for: a
// live resume racing a redundant call (e.g. a startup recovery sweep) for the SAME already-resolved wait.
func TestTryResumePeer_ConcurrentCallsResumeExactlyOnce(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	taskID := deriveTaskID("op-peer-race")
	helperID := meshHelperParticipantID(taskID)
	var waitID agentcomm.WaitID
	var secondInvocations atomic.Int32
	var calls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, tid, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if tid != string(taskID) {
			return passResultOutcome(), nil
		}
		if calls.Add(1) == 1 {
			receipt, err := o.Wait(context.Background(), WaitRequest{
				OperationID: "op-peer-race-wait", Waiter: helperID,
				Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-does-not-exist-yet"},
			})
			if err != nil {
				t.Errorf("Wait: %v", err)
			}
			waitID = receipt.WaitID
			return executor.HelperOutcome{ProcessOK: true}, nil
		}
		secondInvocations.Add(1)
		return passResultOutcome(), nil
	}

	attachTestTask(ctx, t, o, "op-peer-race")
	waitForTaskStatus(t, store, taskID, assistants.StatusWaitingPeer, 2*time.Second)

	if err := o.CancelWait(ctx, CancelWaitRequest{
		OperationID: "op-peer-race-cancel", WaitID: waitID, Actor: state.Actor{Kind: "agent", ID: string(helperID)},
	}); err != nil {
		t.Fatalf("CancelWait: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			o.tryResumePeer(ctx, taskID)
		}()
	}
	wg.Wait()

	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	if got := secondInvocations.Load(); got != 1 {
		t.Fatalf("the resumed turn ran %d times, want exactly 1 — tryResumePeer's CAS must resume exactly once even when called concurrently", got)
	}
	o.concurrency.WaitAgents()
}

// TestRequiredChildOpen_WaitingPeerChildKeepsGateOpen proves requiredChildOpen's existing !Terminal() check
// already covers waiting_peer with no code change needed — a required child parked on a mesh wait must keep
// the parent's required gate open, and must never be mistaken for having delivered a final result.
func TestRequiredChildOpen_WaitingPeerChildKeepsGateOpen(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	var calls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, tid, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			return delegateOutcome(childJSON(assistants.ModeRequired)), nil
		}
		// The required child itself yields on a mesh wait instead of ever reporting a result.
		if _, err := o.Wait(context.Background(), WaitRequest{
			OperationID: "op-reqgate-wait", Waiter: meshHelperParticipantID(assistants.TaskID(tid)),
			Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-does-not-exist-yet"},
		}); err != nil {
			t.Errorf("Wait: %v", err)
		}
		return executor.HelperOutcome{ProcessOK: true}, nil
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingChildren, 2*time.Second)
	childID := store.Snapshot().Assistants.ByParent[parentID][0]
	waitForTaskStatus(t, store, childID, assistants.StatusWaitingPeer, 2*time.Second)

	if !o.requiredChildOpen(parentID) {
		t.Fatal("a required child parked in waiting_peer (non-terminal) must keep the gate open")
	}
	// tryResumeParent only fires on a TERMINAL child status — waiting_peer is not terminal, so the parent must
	// never have been resumed: its own continuation/reply is not mistaken for the child's final result.
	if got := store.Snapshot().Assistants.Tasks[parentID].Status; got != assistants.StatusWaitingChildren {
		t.Fatalf("parent status = %s, want still waiting_children (its required child has not reached a terminal status)", got)
	}
}

// TestPauseTask_HeldWaitingPeerDoesNotResumeUntilContinue proves waiting_peer participates in the pause
// cascade like every other non-terminal status: a held task's wait resolving moves it to queued (tryResumePeer
// does not itself check for a hold — mirrors tryResumeParent), but maybeScheduleTask's own held-check keeps it
// from actually starting until ContinueTask clears the hold.
func TestPauseTask_HeldWaitingPeerDoesNotResumeUntilContinue(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	taskID := deriveTaskID("op-peer-pause")
	helperID := meshHelperParticipantID(taskID)
	colleague := agentcomm.ParticipantID("user")
	threadID := deriveMeshThreadID("op-peer-pause-open")

	var calls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, tid, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if tid != string(taskID) {
			return passResultOutcome(), nil
		}
		if calls.Add(1) == 1 {
			if _, err := o.Wait(context.Background(), WaitRequest{
				OperationID: "op-peer-pause-wait", Waiter: helperID,
				Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: threadID},
			}); err != nil {
				t.Errorf("Wait: %v", err)
			}
			return executor.HelperOutcome{ProcessOK: true}, nil
		}
		return passResultOutcome(), nil
	}

	attachTestTask(ctx, t, o, "op-peer-pause")
	waitForTaskStatus(t, store, taskID, assistants.StatusWaitingPeer, 2*time.Second)

	if err := o.PauseTask(ctx, taskID); err != nil {
		t.Fatalf("PauseTask: %v", err)
	}

	if _, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-peer-pause-open", Members: []agentcomm.ParticipantID{helperID, colleague}, Scope: agentcomm.ScopeStage,
	}); err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-peer-pause-send", ThreadID: threadID, Sender: colleague,
		Recipients: []agentcomm.ParticipantID{helperID}, Kind: agentcomm.MessageKindMessage, Body: "ready",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// The wait resolving moves the task out of waiting_peer to queued (tryResumePeer's own CAS does not check
	// the hold), but it must NOT actually start running while held.
	waitForTaskStatus(t, store, taskID, assistants.StatusQueued, 2*time.Second)
	time.Sleep(30 * time.Millisecond)
	if got := store.Snapshot().Assistants.Tasks[taskID].Status; got != assistants.StatusQueued {
		t.Fatalf("status = %s, want still queued (held) — a paused task must not start just because its wait resolved", got)
	}

	if err := o.ContinueTask(ctx, taskID); err != nil {
		t.Fatalf("ContinueTask: %v", err)
	}
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	o.concurrency.WaitAgents()
}

// TestCancelTask_CancelsTaskParkedInWaitingPeer proves the cancel cascade reaches a task parked in
// waiting_peer exactly like any other non-terminal status: cancelOneTask's only guard is !Terminal(), which
// waiting_peer already satisfies by construction — no code change needed, but this is a named requirement.
func TestCancelTask_CancelsTaskParkedInWaitingPeer(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	taskID := deriveTaskID("op-peer-cancel")
	o.runHelperAgent = func(ctx context.Context, tid, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if _, err := o.Wait(context.Background(), WaitRequest{
			OperationID: "op-peer-cancel-wait", Waiter: meshHelperParticipantID(assistants.TaskID(tid)),
			Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-does-not-exist-yet"},
		}); err != nil {
			t.Errorf("Wait: %v", err)
		}
		return executor.HelperOutcome{ProcessOK: true}, nil
	}

	attachTestTask(ctx, t, o, "op-peer-cancel")
	waitForTaskStatus(t, store, taskID, assistants.StatusWaitingPeer, 2*time.Second)

	if err := o.CancelTask(ctx, taskID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	waitForTaskStatus(t, store, taskID, assistants.StatusCancelled, 2*time.Second)
}
