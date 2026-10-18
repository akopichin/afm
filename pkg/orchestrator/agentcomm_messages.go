package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/redact"
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

// CloseThread implements Service.CloseThread. Every still-pending wait whose condition names this thread (an
// addressed-message wait on it, or a reply-to wait on one of its messages) is resolved in the SAME commit —
// decided by applyMeshThreadClosed itself (pkg/state/mesh_projection.go), not precomputed here from a
// snapshot, so the resolution can never race a concurrent Wait registration nor break idempotent replay.
func (o *Orchestrator) CloseThread(ctx context.Context, req CloseThreadRequest) error {
	if req.OperationID == "" {
		return errors.New("agentcomm: operation id is required")
	}

	closeEff, err := marshalEffect("mesh_thread_closed", state.Target{ThreadID: string(req.ThreadID)}, struct{}{})
	if err != nil {
		return err
	}

	op := state.Operation{ID: req.OperationID, Kind: "mesh_close_thread", Actor: req.Actor, Effects: []state.Effect{closeEff}}
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventMeshThread, Data: map[string]any{"thread_id": string(req.ThreadID), "status": "closed"}})
	})
	return err
}

// Send implements Service.Send. One ApplyRunOperation commits the message plus one accepted receipt/inbox
// reference per recipient (applyMeshMessageSent, pkg/state/mesh_projection.go) — a recipient the thread
// rejects (not a member, or the thread is closed) fails the WHOLE call, nothing is written. Any PENDING wait
// this message satisfies (reply_to the message it answers, or addressed_message in its thread for one of its
// recipients — Agent Mesh Phase 5, T5.3: "a reply arriving makes exactly one continuation") is resolved by
// applyMeshMessageSent itself, in the SAME commit; beforeReady below only tells notifyAndResumeNewlyReadyWaits
// which waits to skip because they were already ready before this call.
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
	// Secrets must never reach the durable journal or a recipient's inbox verbatim — same redaction pass
	// buildTaskContext applies to everything else a task sees (assistants_context.go).
	body := redact.String(req.Body, secretValuesForRedaction())
	eff, err := marshalEffect("mesh_message_sent", state.Target{ThreadID: string(req.ThreadID), MessageID: string(messageID)},
		state.MeshMessageSentPayload{
			Sender: string(req.Sender), Recipients: recipients, Kind: string(req.Kind),
			InReplyTo: inReplyTo, Body: body, ArtifactRefs: req.ArtifactRefs,
		})
	if err != nil {
		return SendReceipt{}, err
	}

	beforeReady := readyWaitSet(o.opts.Store.Snapshot())

	op := state.Operation{ID: req.OperationID, Kind: "mesh_send", Actor: req.Actor, Effects: []state.Effect{eff}}
	res, err := o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventMeshMessage, Data: map[string]any{
			"message_id": string(messageID), "thread_id": string(req.ThreadID), "sender_id": string(req.Sender), "kind": string(req.Kind),
		}})
		o.notifyAndResumeNewlyReadyWaits(ctx, beforeReady)
		// Agent Mesh Phase 5, T5.5: a recipient parked in awaiting_peer/waiting_peer whose OWN wait this
		// message did NOT just resolve (notifyAndResumeNewlyReadyWaits above owns that case) may still need a
		// bounded turn to read and answer it — a genuine counter-question, not the reply it is waiting for.
		for _, r := range req.Recipients {
			o.maybeResumeAwaitingPeerQuestion(ctx, r)
		}
	})
	if err != nil {
		return SendReceipt{}, err
	}
	return SendReceipt{MessageID: messageID, Seq: res.Seq, ThreadID: req.ThreadID}, nil
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

// ListThreads implements Service.ListThreads: every thread req.Participant is a Member of, in the run's
// thread-open order (ThreadOrder) — a plain membership scan, since Phase 1's MeshState keeps no reverse
// participant->threads index of its own. The human run owner (meshUserParticipantID, or an empty
// Participant) is the one exception, mirroring ReadThread's own carve-out exactly: it sees every thread in
// the run, not just the ones naming it as a Member. This file does not invent any stricter, stage-scoped
// isolation beyond that — ReadThread enforces none today, so this does not either (dev-plan.md's T7.3
// dependency note).
func (o *Orchestrator) ListThreads(ctx context.Context, req ListThreadsRequest) (ThreadPage, error) {
	rs := o.opts.Store.Snapshot()
	var page ThreadPage
	sees := req.Participant == meshUserParticipantID || req.Participant == ""
	for _, id := range rs.Assistants.Mesh.ThreadOrder {
		th := rs.Assistants.Mesh.Threads[id]
		if th == nil {
			continue
		}
		if !sees && !threadHasMember(th, req.Participant) {
			continue
		}
		page.Threads = append(page.Threads, *th)
	}
	return page, nil
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
