package orchestrator

import (
	"context"
	"strings"
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

// TestMesh_OpenThreadRetryAfterLazyRegistrationIsIdempotent is the regression test for a bug where retrying
// the EXACT SAME OpenThread call (same operation id) after it already lazily registered a member failed with
// ErrOperationIDConflict: ensureParticipantEffects used to skip members already present in Mesh.Participants,
// so the second call built a SHORTER effects list than the first — and ApplyRunOperation's idempotency check
// compares effects byte-for-byte. A client retrying a dropped response (the whole point of operation ids)
// must get back the ORIGINAL result, not a conflict.
func TestMesh_OpenThreadRetryAfterLazyRegistrationIsIdempotent(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-retry")
	helperID := meshHelperParticipantID(taskID)

	req := OpenThreadRequest{
		OperationID: "op-open-retry", Title: "chat", Scope: agentcomm.ScopeStage,
		Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Actor: state.Actor{Kind: "user"},
	}

	first, err := o.OpenThread(ctx, req)
	if err != nil {
		t.Fatalf("OpenThread (first): %v", err)
	}

	retry, err := o.OpenThread(ctx, req)
	if err != nil {
		t.Fatalf("OpenThread (retry of the same operation id): %v", err)
	}
	if retry.ID != first.ID {
		t.Fatalf("retry returned a different thread: first=%s retry=%s", first.ID, retry.ID)
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

// TestMesh_SendResolvesWaitRegisteredBeforeIt and TestMesh_WaitRegisteredAfterSendStartsReady together cover
// BOTH possible commit orderings between a Wait registration and the Send that satisfies it — the exact
// interleaving a codex review round flagged: Send used to decide which waits it resolved from a snapshot
// read BEFORE its own commit, so a Wait that committed in the gap between that snapshot and Send's commit
// would see an unsatisfied condition at registration AND never be resolved by Send either, stranding it
// Pending forever. Resolution now happens inside applyMeshMessageSent itself (pkg/state/mesh_projection.go),
// against live state under the commit's own lock, so the wait resolves correctly regardless of which
// operation's ApplyRunOperation call actually lands first.
func TestMesh_SendResolvesWaitRegisteredBeforeIt(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-7")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-7", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	// Order A: Wait registers FIRST (still unsatisfied — no message addressed to helperID yet).
	waitReceipt, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-wait-7", Waiter: helperID,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: thread.ID},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waitReceipt.Status != agentcomm.WaitPending {
		t.Fatalf("expected a fresh wait to start pending, got %q", waitReceipt.Status)
	}

	// Send commits SECOND — it must resolve the already-registered wait, not just its own message.
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-send-7", ThreadID: thread.ID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{helperID}, Kind: agentcomm.MessageKindMessage, Body: "hi",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	rs := store.Snapshot()
	if got := rs.Assistants.Mesh.Waits[waitReceipt.WaitID].Status; got != agentcomm.WaitReady {
		t.Fatalf("wait status after Send = %q, want ready", got)
	}
}

func TestMesh_WaitRegisteredAfterSendStartsReady(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-8")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-8", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	// Order B: Send commits FIRST.
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-send-8", ThreadID: thread.ID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{helperID}, Kind: agentcomm.MessageKindMessage, Body: "hi",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Wait registers SECOND — applyMeshWaitRegistered's own "already satisfied at registration" check
	// (meshWaitConditionSatisfied) must see the message that already landed and start it Ready directly.
	waitReceipt, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-wait-8", Waiter: helperID,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: thread.ID},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waitReceipt.Status != agentcomm.WaitReady {
		t.Fatalf("wait registered after a satisfying Send: status = %q, want ready", waitReceipt.Status)
	}
}

// TestMesh_SendRetryAfterResolvingWaitStaysIdempotent regression-tests a second codex finding: Send used to
// bundle a mesh_wait_resolved effect for every wait it resolved INTO THE SAME operation as the message
// itself. On an identical retry (same operation_id), the wait is no longer Pending, so that effect would not
// be recomputed — the retry's effects list would differ from the original, and Store.ApplyRunOperation's
// idempotent-replay check (reconcileIdempotentRepeatLocked) would reject it with ErrOperationIDConflict
// instead of returning the original result. Send's own effect is now always just the one, deterministic
// mesh_message_sent effect — wait resolution is a side effect of applying it, not a separate effect — so a
// retry is a plain, stable replay.
func TestMesh_SendRetryAfterResolvingWaitStaysIdempotent(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-9")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-9", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	if _, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-wait-9", Waiter: helperID,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: thread.ID},
	}); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	req := SendRequest{
		OperationID: "op-send-9", ThreadID: thread.ID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{helperID}, Kind: agentcomm.MessageKindMessage, Body: "hi",
	}
	r1, err := o.Send(ctx, req)
	if err != nil {
		t.Fatalf("Send #1 (resolves the pending wait): %v", err)
	}

	// By now the wait this Send resolved is Ready, not Pending — the exact state that broke the old
	// effects-recomputation-on-replay approach.
	r2, err := o.Send(ctx, req)
	if err != nil {
		t.Fatalf("Send #2 (identical retry): %v", err)
	}
	if r1.MessageID != r2.MessageID || r1.Seq != r2.Seq {
		t.Fatalf("retry of an already-committed Send produced a different result: %+v vs %+v", r1, r2)
	}
}

// TestMesh_SendRedactsSecretsInBody regression-tests the third codex finding: Send used to copy req.Body
// straight into the durable mesh_message_sent payload with no redaction, so a body containing a configured
// secret value was written to events.jsonl (and delivered to the recipient's inbox) verbatim. Send now
// redacts the body the same way buildTaskContext redacts everything else a task sees
// (assistants_context.go's secretValuesForRedaction + pkg/redact).
func TestMesh_SendRedactsSecretsInBody(t *testing.T) {
	const secretValue = "sk-test-supersecret-token-12345"
	t.Setenv("ANTHROPIC_API_KEY", secretValue)

	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-mt-10")
	helperID := meshHelperParticipantID(taskID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-open-10", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	send, err := o.Send(ctx, SendRequest{
		OperationID: "op-send-10", ThreadID: thread.ID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{helperID}, Kind: agentcomm.MessageKindMessage,
		Body: "here is the token: " + secretValue,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	rs := store.Snapshot()
	body := rs.Assistants.Mesh.Messages[send.MessageID].Body
	if strings.Contains(body, secretValue) {
		t.Fatalf("persisted message body still contains the raw secret: %q", body)
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
