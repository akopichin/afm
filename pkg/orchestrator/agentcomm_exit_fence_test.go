package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Every exit and resume action belongs to the turn (execution epoch) or park it was decided for, and is checked
// against it under the store lock: an old process, or an event decided for an earlier park, never acts on the
// stage someone else owns now.

// takeOverByPauseContinue commits what Pause and then Continue commit: the stage now belongs to a new process
// (which this bare harness does not start).
func takeOverByPauseContinue(t *testing.T, o *Orchestrator, stageID string) {
	t.Helper()
	if _, ok := o.Trigger(stageID, bus.EvPause, bus.GuardCtx{}, "manual pause"); !ok {
		t.Fatal("EvPause failed")
	}
	if _, ok := o.Trigger(stageID, bus.EvContinue, bus.GuardCtx{PausedFrom: state.StatusRunning}, ""); !ok {
		t.Fatal("EvContinue failed")
	}
}

// An old process exits after Pause → Continue handed the stage to a new process, which has already yielded and
// parked. The old exit must not run the completion check or publish a completion for the new process's park.
func TestExitFence_StaleExitNeverCompletesTheReplacementsPark(t *testing.T) {
	f := newExitFixture(t)

	out := f.runTurn(t, func() {
		takeOverByPauseContinue(t, f.o, assistantsTestStageID)
		// The replacement yields and parks.
		_, wait := f.askUser(t, "op-new-turn-ask")
		if _, ok := f.o.Trigger(assistantsTestStageID, bus.EvAwaitPeer, bus.GuardCtx{Phase: phaseImplementation, PeerWaits: []agentcomm.WaitID{wait}}, ""); !ok {
			t.Fatal("the replacement's park failed")
		}
	})
	if out.completionChecked || out.completions != 0 {
		t.Fatalf("outcome = %+v; the stale exit must leave the park alone", out)
	}
	if got := f.o.currentStatus(assistantsTestStageID); got != state.StatusAwaitingPeer {
		t.Fatalf("status = %s, want the replacement's awaiting_peer", got)
	}
}

// A completion decided by a turn whose epoch is over is ignored by the event loop, even when the stage is
// running again under the new process: it must not finish the new process's work.
func TestExitFence_StaleCompletionEventIsIgnored(t *testing.T) {
	f := newExitFixture(t)
	stale := f.store.Snapshot().Stages[assistantsTestStageID].Epoch
	takeOverByPauseContinue(t, f.o, assistantsTestStageID)

	if err := f.o.onAgentCompleted(context.Background(), bus.Event{
		Type: bus.EventAgentCompleted, StageID: assistantsTestStageID, Data: phaseImplementation, Epoch: &stale,
	}); err != nil {
		t.Fatal(err)
	}

	if got := f.o.currentStatus(assistantsTestStageID); got != state.StatusRunning {
		t.Fatalf("status = %s, want running: a stale completion must not complete the new process's turn", got)
	}
}

// Old turn → Pause → Continue → Revise of the NEW turn, then the old process exits: the revise is not the old
// turn's, so it must not start a feedback run (the new process's interrupted turn does) — on the park path and
// on the completion path alike.
func TestExitFence_ReviseOfANewerTurnIsNotHandedOff(t *testing.T) {
	for _, yielded := range []bool{true, false} {
		name := "completion path"
		if yielded {
			name = "park path"
		}
		t.Run(name, func(t *testing.T) {
			f := newExitFixture(t)

			out := f.runTurn(t, func() {
				if yielded {
					f.askUser(t, "op-old-ask")
				}
				takeOverByPauseContinue(t, f.o, assistantsTestStageID)
				if applied, _, err := f.o.Revise(context.Background(), assistantsTestStageID, "use YAML"); err != nil || !applied {
					t.Fatalf("Revise = %v, %v", applied, err)
				}
			})

			if out.interrupted != 0 || out.completionChecked || len(out.continuations) != 0 {
				t.Fatalf("outcome = %+v, want the old exit to do nothing", out)
			}
			if got := f.o.currentStatus(assistantsTestStageID); got != state.StatusRevising {
				t.Fatalf("status = %s, want revising", got)
			}
		})
	}
}

// Two hand-offs for one Revise (e.g. the old exit and the revised turn's own interrupted path) start one feedback
// run: the run's first transition out of revising is the claim, and the loser does not run an agent.
func TestExitFence_OneFeedbackRunPerRevise(t *testing.T) {
	runner := &scriptedStageRunner{turn: func(int32) error { return nil }}
	o, _ := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
	activateStage(t, o, unreadTestStageID)
	if applied, _, err := o.Revise(context.Background(), unreadTestStageID, "use YAML"); err != nil || !applied {
		t.Fatalf("Revise = %v, %v", applied, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stage := *o.reg().Stage(unreadTestStageID)

	o.spawnKind(ctx, stage, kindAutonomous, o.runAutonomousWithFeedback)
	o.spawnKind(ctx, stage, kindAutonomous, o.runAutonomousWithFeedback)
	o.concurrency.WaitAgents()

	if got := runner.turns.Load(); got != 1 {
		t.Fatalf("feedback agent runs = %d, want 1", got)
	}
}

// A resume decided for one park is bound to it: after the first event resumed the stage and the new turn parked
// again, a second event queued for the first park (here a counter-question queued with the reply) is a no-op —
// it must not resume the new park, whose wait is still pending.
func TestExitFence_ResumeOfAnEarlierParkIsANoOp(t *testing.T) {
	runner := &scriptedStageRunner{turn: func(int32) error { return nil }}
	o, store := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); o.concurrency.WaitAgents() })
	activateStage(t, o, unreadTestStageID)
	th, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-park-thread", Members: []agentcomm.ParticipantID{meshUserParticipantID, unreadTestStage}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatal(err)
	}
	askReviewers(t, o, unreadTestStage, th.ID, []agentcomm.ParticipantID{meshUserParticipantID})
	epoch := store.Snapshot().Stages[unreadTestStageID].Epoch
	o.parkForPeers(ctx, unreadTestStageID, phaseAutonomous, epoch, o.turnEpisode(unreadTestStage, nil), func() { t.Error("no revise here") })
	park1 := store.Snapshot().Stages[unreadTestStageID].Seq

	// The first event resumes park 1; the new turn ends with the wait still pending and parks again.
	if err := o.onPeerReplied(ctx, bus.Event{StageID: unreadTestStageID, ParkSeq: park1}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for runner.turns.Load() != 1 || store.Get(unreadTestStageID) != state.StatusAwaitingPeer {
		if time.Now().After(deadline) {
			t.Fatalf("the resumed turn did not park again (turns %d, status %s)", runner.turns.Load(), store.Get(unreadTestStageID))
		}
		time.Sleep(5 * time.Millisecond)
	}

	if err := o.onPeerQuestion(ctx, bus.Event{StageID: unreadTestStageID, ParkSeq: park1}); err != nil {
		t.Fatal(err)
	}
	o.concurrency.WaitAgents()

	if got := runner.turns.Load(); got != 1 {
		t.Fatalf("stage turns = %d, want 1: the event for park 1 resumed park 2", got)
	}
	if got := store.Get(unreadTestStageID); got != state.StatusAwaitingPeer {
		t.Fatalf("status = %s, want the second park untouched", got)
	}
}

// A Wait retried under a request id committed before reply_to waits carried recipient_reply_only is the same
// request: it replays the committed wait instead of failing as a conflict.
func TestWait_RetryOfALegacyReplyToWaitReplays(t *testing.T) {
	f := newExitFixture(t)
	ctx := context.Background()
	sent, err := f.o.Send(ctx, SendRequest{
		OperationID: "op-ask", ThreadID: f.thread, Sender: f.stage, Recipients: []agentcomm.ParticipantID{meshUserParticipantID},
		Kind: agentcomm.MessageKindMessage, Body: "which format?", Actor: state.Actor{Kind: "assistant", ID: assistantsTestStageID},
	})
	if err != nil {
		t.Fatal(err)
	}
	cond := agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: sent.MessageID}
	actor := state.Actor{Kind: "assistant", ID: assistantsTestStageID}
	// What an older afm committed: the same wait without the rule flag.
	legacy, err := marshalEffect("mesh_wait_registered", state.Target{WaitID: string(deriveMeshWaitID("op-legacy-wait"))},
		state.MeshWaitRegisteredPayload{Waiter: string(f.stage), Condition: cond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ApplyRunOperation(state.Operation{ID: "op-legacy-wait", Kind: "mesh_wait", Actor: actor, Effects: []state.Effect{legacy}}, nil); err != nil {
		t.Fatal(err)
	}

	receipt, err := f.o.Wait(ctx, WaitRequest{OperationID: "op-legacy-wait", Waiter: f.stage, Condition: cond, Actor: actor})
	if err != nil {
		t.Fatalf("retry of the legacy wait: %v, want a replay", err)
	}
	if receipt.WaitID != deriveMeshWaitID("op-legacy-wait") || receipt.Status != agentcomm.WaitPending {
		t.Fatalf("receipt = %+v, want the committed pending wait", receipt)
	}
	if w := f.store.Snapshot().Assistants.Mesh.Waits[receipt.WaitID]; w.RecipientReplyOnly {
		t.Fatal("the replay must keep the committed wait's legacy rule")
	}
}
