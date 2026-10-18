package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 2, T2.2: OpenThread/CloseThread/Send/ReadThread/ReadInbox/Acknowledge. Every mutation is
// exactly one ApplyRunOperation — the all-or-nothing guarantee that makes "a Send to a mixed recipient set
// where one is bad fails entirely and writes nothing" fall out of the storage layer rather than needing its
// own rollback here.

const defaultMeshPageLimit = 50

// OpenThread implements Service.OpenThread. Members not yet known to the mesh (a stage agent or helper
// addressed for the first time) are lazily registered in the SAME operation via ensureParticipantEffects —
// see that function's doc comment for why Phase 1 never pre-populates them.
func (o *Orchestrator) OpenThread(ctx context.Context, req OpenThreadRequest) (agentcomm.Thread, error) {
	if req.OperationID == "" {
		return agentcomm.Thread{}, errors.New("agentcomm: operation id is required")
	}
	if !req.Scope.Valid() {
		return agentcomm.Thread{}, fmt.Errorf("agentcomm: invalid scope %q", req.Scope)
	}

	rs := o.opts.Store.Snapshot()
	participantEffects, err := ensureParticipantEffects(rs, req.Members)
	if err != nil {
		return agentcomm.Thread{}, err
	}

	threadID := deriveMeshThreadID(req.OperationID)
	members := make([]string, len(req.Members))
	for i, m := range req.Members {
		members[i] = string(m)
	}
	threadEff, err := marshalEffect("mesh_thread_opened", state.Target{ThreadID: string(threadID)},
		state.MeshThreadOpenedPayload{Title: req.Title, Scope: string(req.Scope), Members: members, MaxMessages: req.MaxMessages})
	if err != nil {
		return agentcomm.Thread{}, err
	}

	effects := append(participantEffects, threadEff)
	op := state.Operation{ID: req.OperationID, Kind: "mesh_open_thread", Actor: req.Actor, Effects: effects}
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		for _, m := range req.Members {
			o.publishNotice(bus.Event{Type: bus.EventMeshParticipant, Data: map[string]any{"participant_id": string(m)}})
		}
		o.publishNotice(bus.Event{Type: bus.EventMeshThread, Data: map[string]any{"thread_id": string(threadID), "title": req.Title}})
	})
	if err != nil {
		return agentcomm.Thread{}, err
	}

	return agentcomm.Thread{
		ID: threadID, Title: req.Title, Scope: req.Scope, Members: req.Members, Status: agentcomm.ThreadOpen,
		Budgets: agentcomm.ThreadBudgets{MaxMessages: req.MaxMessages},
	}, nil
}

// CloseThread implements Service.CloseThread. Every still-pending wait whose condition names this thread
// (an addressed-message wait on it, or a reply-to wait on one of its messages) is resolved in the SAME
// operation with reason "thread_closed" — closure is never passed off as the answer those waits were
// registered for (tmp/agent-mesh.md §5.4/§6.6).
func (o *Orchestrator) CloseThread(ctx context.Context, req CloseThreadRequest) error {
	if req.OperationID == "" {
		return errors.New("agentcomm: operation id is required")
	}
	rs := o.opts.Store.Snapshot()

	closeEff, err := marshalEffect("mesh_thread_closed", state.Target{ThreadID: string(req.ThreadID)}, struct{}{})
	if err != nil {
		return err
	}
	effects := []state.Effect{closeEff}

	threadMessages := map[agentcomm.MessageID]bool{}
	for _, id := range rs.Assistants.Mesh.MessagesByThread[req.ThreadID] {
		threadMessages[id] = true
	}
	for _, id := range sortedWaitIDs(rs.Assistants.Mesh.Waits) {
		w := rs.Assistants.Mesh.Waits[id]
		if w.Status != agentcomm.WaitPending {
			continue
		}
		affected := (w.Condition.Kind == agentcomm.WaitOnAddressedMessage && w.Condition.ThreadID == req.ThreadID) ||
			(w.Condition.Kind == agentcomm.WaitOnReplyTo && threadMessages[w.Condition.MessageID])
		if !affected {
			continue
		}
		eff, err := marshalEffect("mesh_wait_resolved", state.Target{WaitID: string(id)},
			state.MeshWaitResolvedPayload{Status: string(agentcomm.WaitCancelled), Reason: "thread_closed"})
		if err != nil {
			return err
		}
		effects = append(effects, eff)
	}

	op := state.Operation{ID: req.OperationID, Kind: "mesh_close_thread", Actor: req.Actor, Effects: effects}
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventMeshThread, Data: map[string]any{"thread_id": string(req.ThreadID), "status": "closed"}})
	})
	return err
}

// Send implements Service.Send. One ApplyRunOperation commits the message plus one accepted receipt/inbox
// reference per recipient (applyMeshMessageSent, pkg/state/mesh_projection.go) — a recipient the thread
// rejects (not a member, or the thread is closed) fails the WHOLE call, nothing is written.
func (o *Orchestrator) Send(ctx context.Context, req SendRequest) (SendReceipt, error) {
	if req.OperationID == "" {
		return SendReceipt{}, errors.New("agentcomm: operation id is required")
	}
	if req.Sender == "" {
		return SendReceipt{}, errors.New("agentcomm: sender is required")
	}

	messageID := deriveMeshMessageID(req.OperationID)
	recipients := make([]string, len(req.Recipients))
	for i, r := range req.Recipients {
		recipients[i] = string(r)
	}
	var inReplyTo *string
	if req.InReplyTo != nil {
		s := string(*req.InReplyTo)
		inReplyTo = &s
	}
	eff, err := marshalEffect("mesh_message_sent", state.Target{ThreadID: string(req.ThreadID), MessageID: string(messageID)},
		state.MeshMessageSentPayload{
			Sender: string(req.Sender), Recipients: recipients, Kind: string(req.Kind),
			InReplyTo: inReplyTo, Body: req.Body, ArtifactRefs: req.ArtifactRefs,
		})
	if err != nil {
		return SendReceipt{}, err
	}
	effects := []state.Effect{eff}

	// Agent Mesh Phase 5, T5.3: "a reply arriving makes exactly one continuation" — this Send resolves any
	// PENDING wait it satisfies (reply_to the message it answers, or addressed_message in its thread for one
	// of its recipients) in the SAME operation, mirroring Publish's board_key wakeup (agentcomm_board.go).
	// Without this, a wait registered before this reply would stay pending forever: Phase 1 only evaluates
	// "already satisfied" AT REGISTRATION time (applyMeshWaitRegistered), never retroactively.
	rs := o.opts.Store.Snapshot()
	resolvedWaits, err := meshWaitResolutionEffects(rs, req.ThreadID, req.InReplyTo, req.Recipients)
	if err != nil {
		return SendReceipt{}, err
	}
	effects = append(effects, resolvedWaits.effects...)

	op := state.Operation{ID: req.OperationID, Kind: "mesh_send", Actor: req.Actor, Effects: effects}
	res, err := o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventMeshMessage, Data: map[string]any{
			"message_id": string(messageID), "thread_id": string(req.ThreadID), "sender_id": string(req.Sender), "kind": string(req.Kind),
		}})
		for _, id := range resolvedWaits.ids {
			o.publishNotice(bus.Event{Type: bus.EventMeshWait, Data: map[string]any{"wait_id": string(id), "status": string(agentcomm.WaitReady)}})
		}
		o.resumeWaitersAfterCommit(ctx, resolvedWaits.waiters)
	})
	if err != nil {
		return SendReceipt{}, err
	}
	return SendReceipt{MessageID: messageID, Seq: res.Seq}, nil
}

// meshWaitResolution bundles the resolve effects a Send/Publish must commit atomically with the ids/waiters
// needed afterward for notices and resume (T5.3).
type meshWaitResolution struct {
	effects []state.Effect
	ids     []agentcomm.WaitID
	waiters []agentcomm.ParticipantID
}

// meshWaitResolutionEffects finds every PENDING wait this new message satisfies: a reply_to wait on the
// message being answered, or an addressed_message wait on this thread held by one of the recipients.
func meshWaitResolutionEffects(rs state.RunState, threadID agentcomm.ThreadID, inReplyTo *agentcomm.MessageID, recipients []agentcomm.ParticipantID) (meshWaitResolution, error) {
	recipientSet := make(map[agentcomm.ParticipantID]bool, len(recipients))
	for _, r := range recipients {
		recipientSet[r] = true
	}

	var out meshWaitResolution
	for _, id := range sortedWaitIDs(rs.Assistants.Mesh.Waits) {
		w := rs.Assistants.Mesh.Waits[id]
		if w.Status != agentcomm.WaitPending {
			continue
		}
		satisfied := false
		switch w.Condition.Kind {
		case agentcomm.WaitOnReplyTo:
			satisfied = inReplyTo != nil && w.Condition.MessageID == *inReplyTo
		case agentcomm.WaitOnAddressedMessage:
			satisfied = w.Condition.ThreadID == threadID && recipientSet[w.Waiter]
		default:
			satisfied = false
		}
		if !satisfied {
			continue
		}
		eff, err := marshalEffect("mesh_wait_resolved", state.Target{WaitID: string(id)}, state.MeshWaitResolvedPayload{Status: string(agentcomm.WaitReady)})
		if err != nil {
			return meshWaitResolution{}, err
		}
		out.effects = append(out.effects, eff)
		out.ids = append(out.ids, id)
		out.waiters = append(out.waiters, w.Waiter)
	}
	return out, nil
}

// ReadThread implements Service.ReadThread. Membership is enforced here (not by the projection, which has
// no notion of "the caller"): a non-member cannot read a thread, except the human, who sees every thread in
// the run (the run owner — tmp/agent-mesh.md §5.4).
func (o *Orchestrator) ReadThread(ctx context.Context, req ReadThreadRequest) (MessagePage, error) {
	rs := o.opts.Store.Snapshot()
	thread, ok := rs.Assistants.Mesh.Threads[req.ThreadID]
	if !ok {
		return MessagePage{}, fmt.Errorf("agentcomm: unknown thread %q", req.ThreadID)
	}
	if req.Reader != meshUserParticipantID && req.Reader != "" && !threadHasMember(thread, req.Reader) {
		return MessagePage{}, fmt.Errorf("agentcomm: %q is not a member of thread %q", req.Reader, req.ThreadID)
	}
	ids := rs.Assistants.Mesh.MessagesByThread[req.ThreadID]
	return pageMessages(rs, ids, req.Cursor, req.Limit), nil
}

// ReadInbox implements Service.ReadInbox: every message ever addressed to Participant, newest additions
// last. It does NOT transition any receipt to delivered — "delivered" is earned only at the actual handoff
// point (a turn's prompt package, or an explicit MarkMessageDelivered call from the `afm agent inbox`
// handler), never by this read alone ("неявного ack при чтении нет", tmp/agent-mesh.md §6.1).
func (o *Orchestrator) ReadInbox(ctx context.Context, req ReadInboxRequest) (MessagePage, error) {
	rs := o.opts.Store.Snapshot()
	ids := rs.Assistants.Mesh.Inbox[req.Participant]
	return pageMessages(rs, ids, req.Cursor, req.Limit), nil
}

func threadHasMember(t *agentcomm.Thread, id agentcomm.ParticipantID) bool {
	for _, m := range t.Members {
		if m == id {
			return true
		}
	}
	return false
}

func pageMessages(rs state.RunState, ids []agentcomm.MessageID, cursor uint64, limit int) MessagePage {
	if limit <= 0 {
		limit = defaultMeshPageLimit
	}
	var page MessagePage
	for _, id := range ids {
		m := rs.Assistants.Mesh.Messages[id]
		if m == nil || m.Seq <= cursor {
			continue
		}
		page.Messages = append(page.Messages, *m)
		page.NextCursor = m.Seq
		if len(page.Messages) >= limit {
			break
		}
	}
	return page
}

// Acknowledge implements Service.Acknowledge, bound to message+recipient (and, once Phase 5 adds
// generations to this call, the recipient's current generation) — reading a message never implies this.
func (o *Orchestrator) Acknowledge(ctx context.Context, req AcknowledgeRequest) error {
	if req.OperationID == "" {
		return errors.New("agentcomm: operation id is required")
	}
	eff, err := marshalEffect("mesh_message_acked",
		state.Target{MessageID: string(req.MessageID), ParticipantID: string(req.Participant)}, state.MeshMessageReceiptPayload{})
	if err != nil {
		return err
	}
	op := state.Operation{ID: req.OperationID, Kind: "mesh_ack", Actor: req.Actor, Effects: []state.Effect{eff}}
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventMeshReceipt, Data: map[string]any{
			"message_id": string(req.MessageID), "participant_id": string(req.Participant), "status": string(agentcomm.DeliveryAcknowledged),
		}})
	})
	return err
}

// MarkMessageDelivered records that messageID was actually handed to recipient — included in a turn's
// prompt package (Phase 4) or returned by an explicit `afm agent inbox` call (Phase 3) — the one moment
// agent-mesh.md's "delivered" status is earned. Not part of the Service interface (tmp/agent-mesh.md §5.4
// does not list it): it is an internal primitive those two callers share, not a client-facing operation.
// The operation id is deterministic per (message, recipient) so calling this more than once for the same
// pair (e.g. the message is re-included after a retry) is a harmless idempotent replay, never a conflict.
func (o *Orchestrator) MarkMessageDelivered(ctx context.Context, messageID agentcomm.MessageID, recipient agentcomm.ParticipantID) error {
	opID := "mesh-deliver-" + deriveMeshID("mesh-deliver", string(messageID)+"|"+string(recipient))
	eff, err := marshalEffect("mesh_message_delivered",
		state.Target{MessageID: string(messageID), ParticipantID: string(recipient)}, state.MeshMessageReceiptPayload{})
	if err != nil {
		return err
	}
	op := state.Operation{ID: opID, Kind: "mesh_deliver", Actor: state.Actor{Kind: "system"}, Effects: []state.Effect{eff}}
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventMeshReceipt, Data: map[string]any{
			"message_id": string(messageID), "participant_id": string(recipient), "status": string(agentcomm.DeliveryDelivered),
		}})
	})
	return err
}

func sortedWaitIDs(waits map[agentcomm.WaitID]*state.MeshWait) []agentcomm.WaitID {
	ids := make([]agentcomm.WaitID, 0, len(waits))
	for id := range waits {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
