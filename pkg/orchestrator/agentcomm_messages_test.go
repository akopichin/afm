package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 2, T2.2: OpenThread/CloseThread/Send/ReadThread/ReadInbox/Acknowledge.

// newRunningHelperForMesh attaches a helper task and blocks its fake runHelperAgent on a channel this
// helper itself closes during t.Cleanup — so the goroutine runs to completion (and finishes writing its
// result) BEFORE the surrounding newAssistantsTestOrchestrator's store.Close cleanup runs (Cleanup funcs run
// LIFO, and this one is registered after that one). Blocking on ctx.Done() instead (which this test package
// never cancels) leaves the goroutine parked forever and races the *next* test's os.RemoveAll on an
// unrelated TempDir — observed as a flaky "directory not empty" cleanup failure.
func newRunningHelperForMesh(t *testing.T, o *Orchestrator, store *state.Store, opID string) assistants.TaskID {
	t.Helper()
	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		<-release
		return passResultOutcome(), nil
	}
	taskID := attachTestTask(context.Background(), t, o, opID)
	waitForTaskStatus(t, store, taskID, assistants.StatusRunning, 2*time.Second)
	t.Cleanup(func() {
		close(release)
		waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	})
	return taskID
}

func TestMesh_OpenThreadRegistersUnknownMembersLazily(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-1")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-1", Title: "chat", Scope: agentcomm.ScopeStage,
		Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	rs := store.Snapshot()
	if _, ok := rs.Assistants.Mesh.Participants[helperID]; !ok {
		t.Fatalf("helper participant %s was not lazily registered", helperID)
	}
	if _, ok := rs.Assistants.Mesh.Threads[thread.ID]; !ok {
		t.Fatalf("thread %s not found after OpenThread", thread.ID)
	}
}

func TestMesh_SendRejectsNonMemberRecipientAndWritesNothing(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-2")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-2", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	before := store.Snapshot()
	beforeCount := len(before.Assistants.Mesh.Messages)

	_, err = o.Send(ctx, SendRequest{
		OperationID: "op-send-1", ThreadID: thread.ID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{helperID, "helper:does-not-exist"}, Kind: agentcomm.MessageKindMessage, Body: "hi",
	})
	if err == nil {
		t.Fatal("Send with a non-member recipient: want error, got nil")
	}

	after := store.Snapshot()
	if len(after.Assistants.Mesh.Messages) != beforeCount {
		t.Fatalf("Send partially wrote a message: before=%d after=%d", beforeCount, len(after.Assistants.Mesh.Messages))
	}
}

func TestMesh_SendIsIdempotentPerOperationID(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-3")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-3", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	req := SendRequest{OperationID: "op-send-repeat", ThreadID: thread.ID, Sender: meshUserParticipantID, Recipients: []agentcomm.ParticipantID{helperID}, Kind: agentcomm.MessageKindMessage, Body: "hi"}
	r1, err := o.Send(ctx, req)
	if err != nil {
		t.Fatalf("Send #1: %v", err)
	}
	r2, err := o.Send(ctx, req)
	if err != nil {
		t.Fatalf("Send #2 (repeat): %v", err)
	}
	if r1.MessageID != r2.MessageID {
		t.Fatalf("repeated Send produced a different message id: %s vs %s", r1.MessageID, r2.MessageID)
	}
	rs := store.Snapshot()
	if len(rs.Assistants.Mesh.MessagesByThread[thread.ID]) != 1 {
		t.Fatalf("repeated Send created more than one message: %d", len(rs.Assistants.Mesh.MessagesByThread[thread.ID]))
	}
}

func TestMesh_ReadThreadMembershipEnforced(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-4")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-4", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	if _, err := o.ReadThread(ctx, ReadThreadRequest{ThreadID: thread.ID, Reader: meshStageParticipantID(assistantsTestStageID)}); err == nil {
		t.Fatal("ReadThread by a non-member: want error, got nil")
	}
	if _, err := o.ReadThread(ctx, ReadThreadRequest{ThreadID: thread.ID, Reader: meshUserParticipantID}); err != nil {
		t.Fatalf("ReadThread by the human run owner: %v", err)
	}
	if _, err := o.ReadThread(ctx, ReadThreadRequest{ThreadID: thread.ID, Reader: helperID}); err != nil {
		t.Fatalf("ReadThread by a member: %v", err)
	}
}

func TestMesh_AcknowledgeTransitionsReceiptAndReadingDoesNotAck(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-5")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-5", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	send, err := o.Send(ctx, SendRequest{OperationID: "op-send-5", ThreadID: thread.ID, Sender: meshUserParticipantID, Recipients: []agentcomm.ParticipantID{helperID}, Kind: agentcomm.MessageKindMessage, Body: "hi"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if _, err := o.ReadInbox(ctx, ReadInboxRequest{Participant: helperID}); err != nil {
		t.Fatalf("ReadInbox: %v", err)
	}
	rs := store.Snapshot()
	if got := rs.Assistants.Mesh.Receipts[send.MessageID][helperID]; got != agentcomm.DeliveryAccepted {
		t.Fatalf("reading the inbox must not advance the receipt: got %q, want accepted", got)
	}

	if err := o.Acknowledge(ctx, AcknowledgeRequest{OperationID: "op-ack-5", MessageID: send.MessageID, Participant: helperID}); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	rs = store.Snapshot()
	if got := rs.Assistants.Mesh.Receipts[send.MessageID][helperID]; got != agentcomm.DeliveryAcknowledged {
		t.Fatalf("Acknowledge did not advance the receipt: got %q", got)
	}
}

func TestMesh_CloseThreadResolvesDependentWaits(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-6")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-6", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	waitReceipt, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-wait-6", Waiter: helperID,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: thread.ID},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waitReceipt.Status != agentcomm.WaitPending {
		t.Fatalf("expected a fresh wait to start pending, got %q", waitReceipt.Status)
	}

	if err := o.CloseThread(ctx, CloseThreadRequest{OperationID: "op-close-6", ThreadID: thread.ID}); err != nil {
		t.Fatalf("CloseThread: %v", err)
	}

	rs := store.Snapshot()
	w := rs.Assistants.Mesh.Waits[waitReceipt.WaitID]
	if w.Status != agentcomm.WaitCancelled {
		t.Fatalf("wait status after CloseThread = %q, want cancelled", w.Status)
	}
}
