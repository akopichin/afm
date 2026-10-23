package orchestrator

import (
	"context"
	"reflect"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// A batch reads back as it was registered: its id, the items in request order, and each one's own outcome.
func TestMesh_WaitBatchesShowsTheItemsAndTheirOutcomes(t *testing.T) {
	f := newBatchFixture(t, 3)
	registered := f.mustWait("op-batch-wait", nil, 0, 1, 2)
	f.reply(1)

	got, err := f.o.WaitBatches(f.stage, registered.BatchID)
	if err != nil {
		t.Fatalf("WaitBatches: %v", err)
	}
	if len(got) != 1 || got[0].BatchID != registered.BatchID || got[0].Status != agentcomm.WaitPending {
		t.Fatalf("batches = %+v, want the one registered batch, still pending", got)
	}
	wantStatus := []agentcomm.WaitStatus{agentcomm.WaitPending, agentcomm.WaitReady, agentcomm.WaitPending}
	for i, item := range got[0].Items {
		if item.MessageID != f.requests[i] || item.WaitID != registered.Items[i].WaitID || item.Status != wantStatus[i] {
			t.Fatalf("item %d = %+v, want request %s, wait %s, status %s", i, item, f.requests[i], registered.Items[i].WaitID, wantStatus[i])
		}
	}

	// The id of one item is the waiter's own wait id: it opens the batch the item belongs to.
	byItem, err := f.o.WaitBatches(f.stage, string(registered.Items[1].WaitID))
	if err != nil {
		t.Fatalf("WaitBatches(item id): %v", err)
	}
	if !reflect.DeepEqual(byItem, got) {
		t.Fatalf("batch by item id = %+v, want the same as by batch id %+v", byItem, got)
	}
	_, err = f.o.WaitBatches("stage:other", string(registered.Items[1].WaitID))
	requireRejected(t, err, assistants.CodeNotFound)
}

// Without an id the waiter lists its own batches, oldest first, and a cancelled item keeps its reason. A batch
// whose every item was cancelled before any deadline ended is ready, not expired: cancelling is a way of
// finishing a wait, not a timeout.
func TestMesh_WaitBatchesWithoutAnIDListsTheWaitersOwnBatches(t *testing.T) {
	f := newBatchFixture(t, 3)
	first := f.mustWait("op-batch-one", nil, 0)
	second := f.mustWait("op-batch-two", nil, 1, 2)
	cancel := CancelWaitRequest{OperationID: "op-cancel-two", BatchID: second.BatchID, Reason: "not needed any more", Actor: state.Actor{Kind: "agent", ID: string(f.stage)}}
	if err := f.o.CancelWait(context.Background(), cancel); err != nil {
		t.Fatalf("CancelWait: %v", err)
	}

	got, err := f.o.WaitBatches(f.stage, "")
	if err != nil {
		t.Fatalf("WaitBatches: %v", err)
	}
	if len(got) != 2 || got[0].BatchID != first.BatchID || got[1].BatchID != second.BatchID {
		t.Fatalf("batches = %+v, want %s then %s", got, first.BatchID, second.BatchID)
	}
	if got[1].Status != agentcomm.WaitReady || got[1].Items[0].Reason != "not needed any more" || got[1].Items[0].Status != agentcomm.WaitCancelled {
		t.Fatalf("cancelled batch = %+v, want every item cancelled with the reason, read as ready", got[1])
	}
}

// Another participant's batch is unknown to the reader: it is neither listed nor opened by its id, and the
// refusal is the same as for an id that never existed.
func TestMesh_WaitBatchesHidesTheBatchesOfOthers(t *testing.T) {
	f := newBatchFixture(t, 1)
	registered := f.mustWait("op-batch-wait", nil, 0)
	stranger := agentcomm.ParticipantID("stage:other")

	listed, err := f.o.WaitBatches(stranger, "")
	if err != nil || len(listed) != 0 {
		t.Fatalf("batches of a stranger = %+v, %v, want an empty list", listed, err)
	}
	for _, id := range []string{registered.BatchID, "mb-unknown"} {
		_, err := f.o.WaitBatches(stranger, id)
		requireRejected(t, err, assistants.CodeNotFound)
	}
}

// A plain reply_to wait is no batch: the list holds batches only, but its own id opens it as a group of one that
// reads as the wait itself, and only for its waiter.
func TestMesh_WaitBatchesOpensASingleWaitAsAGroupOfOne(t *testing.T) {
	f := newBatchFixture(t, 1)
	single, err := f.o.Wait(context.Background(), WaitRequest{
		OperationID: "op-single", Waiter: f.stage, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: f.requests[0]},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	cancel := CancelWaitRequest{OperationID: "op-cancel-single", WaitID: single.WaitID, Reason: "not needed any more", Actor: state.Actor{Kind: "agent", ID: string(f.stage)}}
	if err := f.o.CancelWait(context.Background(), cancel); err != nil {
		t.Fatalf("CancelWait: %v", err)
	}

	listed, err := f.o.WaitBatches(f.stage, "")
	if err != nil || len(listed) != 0 {
		t.Fatalf("batches = %+v, %v, want none", listed, err)
	}
	got, err := f.o.WaitBatches(f.stage, string(single.WaitID))
	if err != nil || len(got) != 1 {
		t.Fatalf("WaitBatches(single) = %+v, %v, want one group", got, err)
	}
	want := WaitReceipt{WaitID: single.WaitID, Status: agentcomm.WaitCancelled, Items: []WaitItemReceipt{
		{MessageID: f.requests[0], WaitID: single.WaitID, Status: agentcomm.WaitCancelled, Reason: "not needed any more"},
	}}
	if !reflect.DeepEqual(got[0], want) {
		t.Fatalf("group of one = %+v, want %+v", got[0], want)
	}
	_, err = f.o.WaitBatches("stage:other", string(single.WaitID))
	requireRejected(t, err, assistants.CodeNotFound)
}
