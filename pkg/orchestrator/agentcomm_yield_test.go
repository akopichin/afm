package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 5, T5.2/T5.3: yield without a false completion, and the resume-on-reply wiring's guards.

func registerTestStageWait(t *testing.T, o *Orchestrator, stageID, opID string) agentcomm.WaitID {
	t.Helper()
	waiter := meshStageParticipantID(stageID)
	receipt, err := o.Wait(context.Background(), WaitRequest{
		OperationID: opID, Waiter: waiter,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-does-not-exist-yet"},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	return receipt.WaitID
}

func TestMesh_HasOpenMeshWait(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	if o.hasOpenMeshWait("s1") {
		t.Fatal("no wait registered yet: want false")
	}

	registerTestStageWait(t, o, "s1", "op-yield-1")
	if !o.hasOpenMeshWait("s1") {
		t.Fatal("a pending wait was just registered: want true")
	}
}

func TestMesh_HasOpenMeshWaitIgnoresResolvedWaits(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	waitID := registerTestStageWait(t, o, "s1", "op-yield-2")
	if err := o.CancelWait(context.Background(), CancelWaitRequest{OperationID: "op-yield-2-cancel", WaitID: waitID}); err != nil {
		t.Fatalf("CancelWait: %v", err)
	}
	if o.hasOpenMeshWait("s1") {
		t.Fatal("the only wait was cancelled (no longer pending): want false")
	}
}

// TestRunWithRetry_MeshWaitParksAwaitingPeerNotFalseCompletion is T5.2's central claim: an agent that
// registers a wait and exits does NOT get an IncompleteWorkError retry loop, is NOT reported done, and is
// NOT turned into a user question.
func TestRunWithRetry_MeshWaitParksAwaitingPeerNotFalseCompletion(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	registerTestStageWait(t, o, "s1", "op-yield-3")

	completionCheckCalled := false
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(retryContext string) error { return nil }, // agent registered the wait and exited cleanly
		func() error {
			completionCheckCalled = true
			return errors.New("incomplete: still waiting on qa") // would normally drive a correction retry
		},
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if completionCheckCalled {
		t.Error("completionCheck must not run when the agent yielded on a mesh wait — that is not what the open wait means")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusAwaitingPeer {
		t.Errorf("status = %v, want awaiting_peer", got)
	}
}

// TestRunWithRetry_NoOpenWaitKeepsExistingBehavior (T5.2): an agent that exits with NO open wait is
// unaffected by this change.
func TestRunWithRetry_NoOpenWaitKeepsExistingBehavior(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	events := o.critical.Recv()

	completionCheckCalled := false
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(retryContext string) error { return nil },
		func() error { completionCheckCalled = true; return nil },
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if !completionCheckCalled {
		t.Error("completionCheck must still run when there is no open mesh wait")
	}
	// runWithRetry only publishes EventAgentCompleted — advancing the FSM is the live event loop's job
	// (mirrors TestRunWithRetry_CompletionMarkerOverridesStaleOpenQuestion).
	select {
	case ev := <-events:
		if err := o.onAgentCompleted(context.Background(), ev); err != nil {
			t.Fatalf("onAgentCompleted: %v", err)
		}
	default:
		t.Fatal("expected EventAgentCompleted to be published")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusDone {
		t.Errorf("status = %v, want done (no wait, no incomplete work)", got)
	}
}

func TestMesh_ResumeAfterPeerReply_NoOpWhenNotAwaitingPeer(t *testing.T) {
	o, _ := setupHookOrch(t, "s1") // status is StatusRunning from setupHookOrch
	events := o.critical.Recv()

	o.resumeAfterPeerReply(context.Background(), "s1")

	select {
	case ev := <-events:
		t.Fatalf("expected no event when the stage is not awaiting_peer, got %+v", ev)
	default:
	}
}

func TestMesh_ResumeAfterPeerReply_PublishesWhenAwaitingPeer(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	events := o.critical.Recv()
	o.Trigger("s1", bus.EvAwaitPeer, bus.GuardCtx{Phase: phaseImplementation}, "")
	<-events // drain this Trigger's own stage_status_changed notification

	o.resumeAfterPeerReply(context.Background(), "s1")

	select {
	case ev := <-events:
		if ev.Type != bus.EventPeerReplied || ev.StageID != "s1" {
			t.Fatalf("got %+v, want EventPeerReplied for s1", ev)
		}
	default:
		t.Fatal("expected EventPeerReplied to be published")
	}
}

// TestOnPeerReplied_RequiresStashedPhase proves onPeerReplied does nothing (no panic, no spawn attempt) when
// prePeerPhase was never stashed for this stage — defensive, mirrors onUserAnswered's equivalent guard.
func TestOnPeerReplied_RequiresStashedPhase(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.Trigger("s1", bus.EvAwaitPeer, bus.GuardCtx{Phase: phaseImplementation}, "")

	if err := o.onPeerReplied(context.Background(), bus.Event{StageID: "s1"}); err != nil {
		t.Fatalf("onPeerReplied: %v", err)
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusAwaitingPeer {
		t.Errorf("status = %v, want unchanged awaiting_peer (no stashed phase to resume into)", got)
	}
}

// TestMesh_SendWakesStageWaitAndPublishesPeerReplied is T5.3's end-to-end claim at the Service level: a
// reply satisfying a stage's registered wait resolves it AND notifies the event loop in the same commit —
// no separate poller, no UI step involved.
func TestMesh_SendWakesStageWaitAndPublishesPeerReplied(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	events := o.critical.Recv()
	ctx := context.Background()

	stageParticipant := meshStageParticipantID("s1")
	colleague := agentcomm.ParticipantID("user") // always a known participant, no task/stage fixture needed
	thread, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-wake-open", Members: []agentcomm.ParticipantID{stageParticipant, colleague}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	o.Trigger("s1", bus.EvAwaitPeer, bus.GuardCtx{Phase: phaseImplementation}, "")
	<-events // drain stage_status_changed from the Trigger above
	o.prePeerPhase.Store("s1", phaseImplementation)

	waitReceipt, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-wake-wait", Waiter: stageParticipant,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: thread.ID},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waitReceipt.Status != agentcomm.WaitPending {
		t.Fatalf("wait status = %q, want pending", waitReceipt.Status)
	}

	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-wake-send", ThreadID: thread.ID, Sender: colleague,
		Recipients: []agentcomm.ParticipantID{stageParticipant}, Kind: agentcomm.MessageKindMessage, Body: "qa says: ready",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	rs := o.opts.Store.Snapshot()
	if got := rs.Assistants.Mesh.Waits[waitReceipt.WaitID].Status; got != agentcomm.WaitReady {
		t.Fatalf("wait status after reply = %q, want ready", got)
	}

	var sawPeerReplied bool
	for {
		select {
		case ev := <-events:
			if ev.Type == bus.EventPeerReplied && ev.StageID == "s1" {
				sawPeerReplied = true
			}
		default:
			if !sawPeerReplied {
				t.Fatal("expected EventPeerReplied to be published once Send resolved the stage's wait")
			}
			return
		}
	}
}
