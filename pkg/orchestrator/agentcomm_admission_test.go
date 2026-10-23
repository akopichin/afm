package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Mesh admission rules decided under the commit's lock, and the agent send's replay check.

func requireRejected(t *testing.T, err error, code assistants.ErrorCode) {
	t.Helper()
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != code {
		t.Fatalf("err = %v, want a typed %s rejection", err, code)
	}
}

func finishStage(t *testing.T, o *Orchestrator, stageID string) {
	t.Helper()
	if _, ok := o.Trigger(stageID, bus.EvComplete, bus.GuardCtx{}, ""); !ok {
		t.Fatalf("EvComplete failed for %s", stageID)
	}
}

// Reusing a request id for a different message — another body, or another sender — is a conflict, never a
// silent "success" that replays the first message.
func TestSendFromAgent_ReusedRequestIDWithOtherMessageConflicts(t *testing.T) {
	o, _ := newTwoStageMeshOrchestrator(t)
	impl := string(meshStageParticipantID(assistantsTestStageID))
	if _, err := agentSend(o, "op-reused", "stage:docs", []string{impl}, "hi"); err != nil {
		t.Fatalf("first: %v", err)
	}

	_, err := agentSend(o, "op-reused", "stage:docs", []string{impl}, "something else")
	requireRejected(t, err, assistants.CodeInvalidStatus)
	if !errors.Is(err, state.ErrOperationIDConflict) {
		t.Fatalf("changed body: err = %v, want ErrOperationIDConflict", err)
	}

	_, err = agentSend(o, "op-reused", impl, []string{"stage:docs"}, "hi")
	if !errors.Is(err, state.ErrOperationIDConflict) {
		t.Fatalf("other sender: err = %v, want ErrOperationIDConflict", err)
	}

	// The exact same request (recipient named by alias this time) still replays.
	if _, err := agentSend(o, "op-reused", "stage:docs", []string{assistantsTestStageID}, "hi"); err != nil {
		t.Fatalf("retry of the same request by alias: %v", err)
	}
}

// The store's own idempotency conflict is a typed rejection (HTTP 409), not a bare error (HTTP 500).
func TestSend_ReusedRequestIDIsTypedConflict(t *testing.T) {
	o, _ := newTwoStageMeshOrchestrator(t)
	ctx := context.Background()
	impl := meshStageParticipantID(assistantsTestStageID)
	r, err := agentSend(o, "op-first", "stage:docs", []string{string(impl)}, "hi")
	if err != nil {
		t.Fatal(err)
	}
	_, err = o.Send(ctx, SendRequest{
		OperationID: "op-first", ThreadID: r.ThreadID, Sender: "stage:docs", Recipients: []agentcomm.ParticipantID{impl},
		Kind: agentcomm.MessageKindRequest, Body: "other", Actor: state.Actor{Kind: "agent", ID: "stage:docs"},
	})
	requireRejected(t, err, assistants.CodeInvalidStatus)
}

// The passive flag is part of the message: the same text under the same request id with the flag flipped wakes
// its recipient differently, so it is a conflict, while the identical request still replays.
func TestSend_ReusedRequestIDWithOtherPassiveFlagConflicts(t *testing.T) {
	o, _ := newTwoStageMeshOrchestrator(t)
	ctx := context.Background()
	impl := meshStageParticipantID(assistantsTestStageID)
	r, err := agentSend(o, "op-first", "stage:docs", []string{string(impl)}, "hi")
	if err != nil {
		t.Fatal(err)
	}
	req := SendRequest{
		OperationID: "op-passive", ThreadID: r.ThreadID, Sender: "stage:docs", Recipients: []agentcomm.ParticipantID{impl},
		Kind: agentcomm.MessageKindMessage, Body: "result", AutoResult: true, Actor: state.Actor{Kind: "agent", ID: "stage:docs"},
	}
	if _, err := o.Send(ctx, req); err != nil {
		t.Fatalf("passive send: %v", err)
	}
	if _, err := o.Send(ctx, req); err != nil {
		t.Fatalf("replay of the same passive send: %v", err)
	}
	req.AutoResult = false
	if _, err := o.Send(ctx, req); !errors.Is(err, state.ErrOperationIDConflict) {
		t.Fatalf("flipped passive flag: err = %v, want ErrOperationIDConflict", err)
	}
}

// A conversation is opened only for a message that can be sent: a finished recipient leaves no empty thread.
func TestSendFromAgent_UnreachableRecipientOpensNothing(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	finishStage(t, o, "docs")

	_, err := agentSend(o, "op-to-finished", string(meshStageParticipantID(assistantsTestStageID)), []string{"stage:docs"}, "hello?")
	requireRejected(t, err, assistants.CodeInvalidStatus)
	if n := len(store.Snapshot().Assistants.Mesh.Threads); n != 0 {
		t.Fatalf("threads = %d, want none left behind", n)
	}
}

// A recipient whose inbox is full is rejected before a conversation is opened.
func TestSendFromAgent_FullInboxOpensNothing(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	ctx := context.Background()
	impl := meshStageParticipantID(assistantsTestStageID)
	th, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-filler", Scope: agentcomm.ScopeRun, Members: []agentcomm.ParticipantID{meshUserParticipantID, impl}})
	if err != nil {
		t.Fatal(err)
	}
	for i := range agentcomm.MaxInboxUnread {
		if _, err := o.Send(ctx, SendRequest{
			OperationID: "op-fill-" + string(rune('a'+i%26)) + string(rune('a'+i/26)), ThreadID: th.ID, Sender: meshUserParticipantID,
			Recipients: []agentcomm.ParticipantID{impl}, Kind: agentcomm.MessageKindMessage, Body: "x",
		}); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}

	_, err = agentSend(o, "op-to-full", "stage:docs", []string{string(impl)}, "one more")
	requireRejected(t, err, assistants.CodeBudgetExceeded)
	if n := len(store.Snapshot().Assistants.Mesh.Threads); n != 1 {
		t.Fatalf("threads = %d, want only the filler thread", n)
	}
}

// The first message of an attach is refused when an extra recipient has already finished.
func TestAttachAndSend_FirstMessageToFinishedRecipientRejected(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	finishStage(t, o, "docs")
	msg := "hello"
	_, err := o.AttachAndSend(context.Background(), AttachAndSendRequest{
		OperationID: "op-attach", ProfileID: "qa", Scope: assistants.ScopeRun, Title: "QA", FirstMessage: &msg,
		Recipients: []agentcomm.ParticipantID{"stage:docs"}, Actor: state.Actor{Kind: "user"},
	})
	requireRejected(t, err, assistants.CodeInvalidStatus)
	if n := len(store.Snapshot().Assistants.Tasks); n != 0 {
		t.Fatalf("tasks = %d, want nothing written", n)
	}
}

// A discussion post to a finished stage keeps its history but is never a deliverable (unread) message.
func TestDiscussion_PostToFinishedStageIsUndeliverable(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	finishStage(t, o, assistantsTestStageID)

	msgID, err := o.PostDiscussionMessage(context.Background(), PostMessageRequest{
		OperationID: "op-late-post", StageID: assistantsTestStageID, AuthorKind: assistants.AuthorHuman,
		Round: assistants.RoundFreeform, Body: "one more thought", Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}
	stage := meshStageParticipantID(assistantsTestStageID)
	rs := store.Snapshot()
	if _, ok := rs.Assistants.Mesh.Messages[agentcomm.MessageID(msgID)]; !ok {
		t.Fatal("the post is missing from the discussion thread's history")
	}
	if got := rs.Assistants.Mesh.Receipts[agentcomm.MessageID(msgID)][stage]; got != agentcomm.DeliveryUndeliverable {
		t.Fatalf("receipt for the finished stage = %s, want undeliverable", got)
	}
	if rs.Assistants.Mesh.HasWakingUnreadFor(stage) {
		t.Fatal("a finished stage must not have an unread message")
	}
}

// Two waits that together form a cycle, registered at the same time: exactly one is admitted.
func TestWait_ConcurrentCycleAdmitsOne(t *testing.T) {
	o, _ := newTwoStageMeshOrchestrator(t)
	ctx := context.Background()
	impl, docs := meshStageParticipantID(assistantsTestStageID), agentcomm.ParticipantID("stage:docs")
	q1, err := agentSend(o, "op-q1", string(impl), []string{string(docs)}, "q1")
	if err != nil {
		t.Fatal(err)
	}
	q2, err := agentSend(o, "op-q2", string(docs), []string{string(impl)}, "q2")
	if err != nil {
		t.Fatal(err)
	}

	for round := range 20 {
		var wg sync.WaitGroup
		errs := make([]error, 2)
		register := func(i int, waiter agentcomm.ParticipantID, on agentcomm.MessageID) {
			defer wg.Done()
			opID := "op-wait-" + string(rune('a'+round)) + string(rune('0'+i))
			_, errs[i] = o.Wait(ctx, WaitRequest{OperationID: opID, Waiter: waiter, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: on}})
		}
		wg.Add(2)
		go register(0, impl, q1.MessageID)
		go register(1, docs, q2.MessageID)
		wg.Wait()

		failed := 0
		for _, e := range errs {
			if e != nil {
				requireRejected(t, e, agentcomm.CodeWaitCycle)
				failed++
			}
		}
		if failed != 1 {
			t.Fatalf("round %d: %d of 2 cyclic waits rejected, want exactly 1", round, failed)
		}
		// Cancel whatever was admitted so the next round starts from no waits.
		for id, w := range o.opts.Store.Snapshot().Assistants.Mesh.Waits {
			if w.Status == agentcomm.WaitPending {
				if err := o.CancelWait(ctx, CancelWaitRequest{OperationID: "cancel-" + string(id), WaitID: id, Actor: state.Actor{Kind: "user"}}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

// ---- Batch wait admission: the whole set is admitted or refused by one commit. ----

// batchWait registers a batch wait of waiter on messageIDs.
func batchWait(o *Orchestrator, opID string, waiter agentcomm.ParticipantID, messageIDs ...agentcomm.MessageID) (WaitReceipt, error) {
	return o.Wait(context.Background(), WaitRequest{
		OperationID: opID, Waiter: waiter, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo}, MessageIDs: messageIDs,
	})
}

// requireBatchRefused checks that Wait refused the whole set with code, named the message at fault, and left
// neither a wait nor a committed operation behind.
func requireBatchRefused(t *testing.T, o *Orchestrator, store *state.Store, opID string, err error, code assistants.ErrorCode, culprit agentcomm.MessageID) {
	t.Helper()
	requireRejected(t, err, code)
	if culprit != "" && !strings.Contains(err.Error(), string(culprit)) {
		t.Fatalf("err = %v, want it to name %s", err, culprit)
	}
	if n := len(store.Snapshot().Assistants.Mesh.Waits); n != 0 {
		t.Fatalf("waits = %d after a refused batch, want none (no partial write)", n)
	}
	if _, committed, err := o.opts.Store.CommittedOperation(opID, opKindMeshWait); err != nil || committed {
		t.Fatalf("operation %s committed = %v (%v), want not committed", opID, committed, err)
	}
}

func TestMeshBatchWait_ListShapeIsCheckedBeforeAnythingIsWritten(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	docs := meshStageParticipantID("docs")
	impl := string(meshStageParticipantID(assistantsTestStageID))
	var ids []agentcomm.MessageID
	for i := range MaxBatchWaits + 1 {
		sent, err := agentSend(o, fmt.Sprintf("op-ask-%d", i), string(docs), []string{impl}, "review this")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sent.MessageID)
	}

	_, err := batchWait(o, "op-empty", docs)
	requireBatchRefused(t, o, store, "op-empty", err, assistants.CodeInvalidRequest, "")
	_, err = batchWait(o, "op-dup", docs, ids[0], ids[1], ids[0])
	requireBatchRefused(t, o, store, "op-dup", err, assistants.CodeInvalidRequest, ids[0])
	_, err = batchWait(o, "op-17", docs, ids...)
	requireBatchRefused(t, o, store, "op-17", err, assistants.CodeInvalidRequest, "")
	if !strings.Contains(err.Error(), fmt.Sprint(MaxBatchWaits)) {
		t.Fatalf("err = %v, want it to state the limit %d", err, MaxBatchWaits)
	}
	_, err = o.Wait(context.Background(), WaitRequest{
		OperationID: "op-both", Waiter: docs, MessageIDs: ids[:2],
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: ids[0]},
	})
	requireBatchRefused(t, o, store, "op-both", err, assistants.CodeInvalidRequest, "")
	_, err = o.Wait(context.Background(), WaitRequest{
		OperationID: "op-other-kind", Waiter: docs, MessageIDs: ids[:2], Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage},
	})
	requireBatchRefused(t, o, store, "op-other-kind", err, assistants.CodeInvalidRequest, "")

	// The limit itself is allowed.
	receipt, err := batchWait(o, "op-16", docs, ids[:MaxBatchWaits]...)
	if err != nil || len(receipt.Items) != MaxBatchWaits {
		t.Fatalf("16 requests: %+v, %v, want an admitted batch of %d", receipt, err, MaxBatchWaits)
	}
}

func TestMeshBatchWait_UnknownOrForeignRequestRefusesTheWholeSet(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	docs := meshStageParticipantID("docs")
	impl := meshStageParticipantID(assistantsTestStageID)
	mine, err := agentSend(o, "op-mine", string(docs), []string{string(impl)}, "review this")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := agentSend(o, "op-theirs", string(impl), []string{string(docs)}, "and this?")
	if err != nil {
		t.Fatal(err)
	}

	_, err = batchWait(o, "op-unknown", docs, mine.MessageID, "mm-does-not-exist")
	requireBatchRefused(t, o, store, "op-unknown", err, assistants.CodeNotFound, "mm-does-not-exist")
	_, err = batchWait(o, "op-foreign", docs, mine.MessageID, theirs.MessageID)
	requireBatchRefused(t, o, store, "op-foreign", err, assistants.CodeActorNotAllowed, theirs.MessageID)
}

func TestMeshBatchWait_RequestNobodyCanAnswerRefusesTheWholeSet(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	docs := meshStageParticipantID("docs")
	impl := meshStageParticipantID(assistantsTestStageID)
	toImpl, err := agentSend(o, "op-to-impl", string(docs), []string{string(impl)}, "review this")
	if err != nil {
		t.Fatal(err)
	}
	toHuman, err := agentSend(o, "op-to-human", string(docs), []string{string(meshUserParticipantID)}, "is this ok?")
	if err != nil {
		t.Fatal(err)
	}
	finishStage(t, o, assistantsTestStageID)

	_, err = batchWait(o, "op-gone", docs, toHuman.MessageID, toImpl.MessageID)
	requireBatchRefused(t, o, store, "op-gone", err, assistants.CodeInvalidStatus, toImpl.MessageID)
}

// A batch that would close a cycle is refused as a whole even when its other requests are fine, and the cycle
// check treats the batch's pending items as one all-of wait once it is admitted.
func TestMeshBatchWait_CycleRefusesTheWholeSet(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	docs := meshStageParticipantID("docs")
	impl := meshStageParticipantID(assistantsTestStageID)
	implAsk, err := agentSend(o, "op-impl-ask", string(impl), []string{string(docs)}, "can you check?")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Wait(context.Background(), WaitRequest{
		OperationID: "op-impl-wait", Waiter: impl, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: implAsk.MessageID},
	}); err != nil {
		t.Fatalf("impl wait: %v", err)
	}
	toImpl, err := agentSend(o, "op-to-impl", string(docs), []string{string(impl)}, "review this")
	if err != nil {
		t.Fatal(err)
	}
	toHuman, err := agentSend(o, "op-to-human", string(docs), []string{string(meshUserParticipantID)}, "is this ok?")
	if err != nil {
		t.Fatal(err)
	}
	waitsBefore := len(store.Snapshot().Assistants.Mesh.Waits)

	_, err = batchWait(o, "op-cycle", docs, toHuman.MessageID, toImpl.MessageID)
	requireRejected(t, err, agentcomm.CodeWaitCycle)
	if !strings.Contains(err.Error(), string(toImpl.MessageID)) {
		t.Fatalf("err = %v, want it to name %s", err, toImpl.MessageID)
	}
	if n := len(store.Snapshot().Assistants.Mesh.Waits); n != waitsBefore {
		t.Fatalf("waits = %d, want the %d before the refused batch", n, waitsBefore)
	}
}

// Another participant's wait on a batch owner closes a cycle through the batch when ANY of its items depends on
// that participant: the batch is blocked until all of its items end.
func TestMeshBatchWait_AdmittedBatchBlocksItsWaiterAsOneAllOfUnit(t *testing.T) {
	o, _ := newTwoStageMeshOrchestrator(t)
	docs := meshStageParticipantID("docs")
	impl := meshStageParticipantID(assistantsTestStageID)
	toImpl, err := agentSend(o, "op-to-impl", string(docs), []string{string(impl)}, "review this")
	if err != nil {
		t.Fatal(err)
	}
	toHuman, err := agentSend(o, "op-to-human", string(docs), []string{string(meshUserParticipantID)}, "is this ok?")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := batchWait(o, "op-docs-batch", docs, toImpl.MessageID, toHuman.MessageID); err != nil {
		t.Fatalf("batch: %v", err)
	}
	// impl asks docs: docs cannot answer while its batch is open, and impl is one of the batch's targets.
	implAsk, err := agentSend(o, "op-impl-ask", string(impl), []string{string(docs)}, "can you check?")
	if err != nil {
		t.Fatal(err)
	}
	_, err = o.Wait(context.Background(), WaitRequest{
		OperationID: "op-impl-wait", Waiter: impl, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: implAsk.MessageID},
	})
	requireRejected(t, err, agentcomm.CodeWaitCycle)
}
