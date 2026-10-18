package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 2, T2.3: Wait/CancelWait and the cycle check.

func TestMesh_WaitAlreadySatisfiedIsReadyImmediately(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	h1 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-w-1a"))
	h2 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-w-1b"))

	thread, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-w-1-open", Members: []agentcomm.ParticipantID{h1, h2}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	send, err := o.Send(ctx, SendRequest{OperationID: "op-w-1-send", ThreadID: thread.ID, Sender: h1, Recipients: []agentcomm.ParticipantID{h2}, Kind: agentcomm.MessageKindRequest, Body: "ping"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	reply, err := o.Send(ctx, SendRequest{OperationID: "op-w-1-reply", ThreadID: thread.ID, Sender: h2, Recipients: []agentcomm.ParticipantID{h1}, Kind: agentcomm.MessageKindReply, InReplyTo: &send.MessageID, Body: "pong"})
	if err != nil {
		t.Fatalf("Send reply: %v", err)
	}
	_ = reply

	// The reply already exists BEFORE Wait is ever called — the classic "reply arrived before Wait" race.
	receipt, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-w-1-wait", Waiter: h1,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: send.MessageID},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if receipt.Status != agentcomm.WaitReady {
		t.Fatalf("wait status = %q, want ready (reply already existed)", receipt.Status)
	}
}

func TestMesh_WaitMutualCycleRejected(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	h1 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-w-2a"))
	h2 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-w-2b"))

	thread, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-w-2-open", Members: []agentcomm.ParticipantID{h1, h2}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	msg1, err := o.Send(ctx, SendRequest{OperationID: "op-w-2-send1", ThreadID: thread.ID, Sender: h1, Recipients: []agentcomm.ParticipantID{h2}, Kind: agentcomm.MessageKindRequest, Body: "q1"})
	if err != nil {
		t.Fatalf("Send 1: %v", err)
	}
	if _, err := o.Wait(ctx, WaitRequest{OperationID: "op-w-2-wait1", Waiter: h1, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: msg1.MessageID}}); err != nil {
		t.Fatalf("Wait 1 (h1 waits on h2): %v", err)
	}

	msg2, err := o.Send(ctx, SendRequest{OperationID: "op-w-2-send2", ThreadID: thread.ID, Sender: h2, Recipients: []agentcomm.ParticipantID{h1}, Kind: agentcomm.MessageKindRequest, Body: "q2"})
	if err != nil {
		t.Fatalf("Send 2: %v", err)
	}
	_, err = o.Wait(ctx, WaitRequest{OperationID: "op-w-2-wait2", Waiter: h2, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: msg2.MessageID}})
	if err == nil {
		t.Fatal("Wait 2 (h2 waits on h1, who already waits on h2): want wait_cycle rejection, got nil error")
	}
	var rejected *state.OperationRejectedError
	if !errors.As(err, &rejected) || rejected.Code != agentcomm.CodeWaitCycle {
		t.Fatalf("expected a CodeWaitCycle rejection, got %v", err)
	}
}

func TestMesh_WaitOnBoardKeyWithNoProducerRequiresDeadline(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	h1 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-w-3"))

	_, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-w-3-wait", Waiter: h1,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnBoardKey, Key: "qa.tests", Status: "ready"},
	})
	if !errors.Is(err, ErrWaitRequiresDeadline) {
		t.Fatalf("Wait on an unpublished board key with no deadline: want ErrWaitRequiresDeadline, got %v", err)
	}
}

func TestMesh_CancelWaitIsIdempotent(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	h1 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-w-4"))
	h2 := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-w-4b"))

	thread, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-w-4-open", Members: []agentcomm.ParticipantID{h1, h2}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	receipt, err := o.Wait(ctx, WaitRequest{OperationID: "op-w-4-wait", Waiter: h1, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: thread.ID}})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if err := o.CancelWait(ctx, CancelWaitRequest{OperationID: "op-w-4-cancel1", WaitID: receipt.WaitID}); err != nil {
		t.Fatalf("CancelWait #1: %v", err)
	}
	if err := o.CancelWait(ctx, CancelWaitRequest{OperationID: "op-w-4-cancel2", WaitID: receipt.WaitID}); err != nil {
		t.Fatalf("CancelWait #2 (already cancelled): %v", err)
	}
	rs := store.Snapshot()
	if got := rs.Assistants.Mesh.Waits[receipt.WaitID].Status; got != agentcomm.WaitCancelled {
		t.Fatalf("wait status = %q, want cancelled", got)
	}
}
