package orchestrator

import (
	"fmt"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// WaitBatches reads the waits of waiter as they stand now. With an empty id it lists every batch waiter
// registered, oldest first. With an id it opens that batch (also by the id of one of its waits), or the single
// wait of that id as a group of one. It is a projection of Mesh.Waits/WaitOrder and keeps nothing of its own, so
// it answers the same after a restart.
// A batch or wait of someone else is unknown to waiter: the refusal is the same as for an id that never existed.
func (o *Orchestrator) WaitBatches(waiter agentcomm.ParticipantID, id string) ([]WaitReceipt, error) {
	mesh := o.opts.Store.Snapshot().Assistants.Mesh
	batches := waitBatchesOf(mesh, waiter)
	if id == "" {
		return batches, nil
	}
	// The id of a wait of waiter that belongs to a batch opens that batch.
	if w := mesh.Waits[agentcomm.WaitID(id)]; w != nil && w.BatchID != "" && w.Waiter == waiter {
		id = w.BatchID
	}
	for _, batch := range batches {
		if batch.BatchID == id {
			return []WaitReceipt{batch}, nil
		}
	}
	if w := mesh.Waits[agentcomm.WaitID(id)]; w != nil && w.BatchID == "" && w.Waiter == waiter {
		item := waitItemReceipt(w)
		return []WaitReceipt{{WaitID: w.ID, Status: w.Status, Items: []WaitItemReceipt{item}}}, nil
	}
	return nil, &state.OperationRejectedError{Code: assistants.CodeNotFound, Reason: fmt.Sprintf("unknown batch or wait %s", id)}
}

// waitItemReceipt is the wait w as one item of a receipt.
func waitItemReceipt(w *state.MeshWait) WaitItemReceipt {
	return WaitItemReceipt{MessageID: w.Condition.MessageID, WaitID: w.ID, Status: w.Status, Reason: w.Reason}
}

// waitBatchesOf groups the batch waits of waiter by batch id, in the order each batch was first registered.
func waitBatchesOf(mesh state.MeshState, waiter agentcomm.ParticipantID) []WaitReceipt {
	var batches []WaitReceipt
	index := map[string]int{} // batch id -> position in batches
	for _, id := range mesh.WaitOrder {
		w := mesh.Waits[id]
		if w == nil || w.BatchID == "" || w.Waiter != waiter {
			continue
		}
		i, seen := index[w.BatchID]
		if !seen {
			i = len(batches)
			index[w.BatchID] = i
			batches = append(batches, WaitReceipt{BatchID: w.BatchID})
		}
		batches[i].Items = append(batches[i].Items, waitItemReceipt(w))
	}
	for i := range batches {
		batches[i].Status = batchStatus(batches[i].Items)
	}
	return batches
}
