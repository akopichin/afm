package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 2, T2.2: OpenThread/CloseThread/Send/ReadThread/ReadInbox/Acknowledge. Every mutation is
// exactly one ApplyRunOperation — the all-or-nothing guarantee that makes "a Send to a mixed recipient set
// where one is bad fails entirely and writes nothing" fall out of the storage layer rather than needing its
// own rollback here.

const defaultMeshPageLimit = 50

// ErrSideAgentExplicitReply means the side process already answered its source
// directly. Its automatic terminal response must be suppressed.
var ErrSideAgentExplicitReply = errors.New("side agent explicitly replied to source")

// OpenThread implements Service.OpenThread. Members not yet known to the mesh (a stage agent or helper
// addressed for the first time) are lazily registered in the SAME operation via ensureParticipantEffects —
// see that function's doc comment for why Phase 1 never pre-populates them. A retry of an already-committed
// request returns the thread it opened, whatever changed since (a member finished, the thread was closed).
func (o *Orchestrator) OpenThread(ctx context.Context, req OpenThreadRequest) (agentcomm.Thread, error) {
	if req.OperationID == "" {
		return agentcomm.Thread{}, errors.New("agentcomm: operation id is required")
	}
	if err := rejectReservedRequestID(req.OperationID, req.Actor); err != nil {
		return agentcomm.Thread{}, err
	}
	if !req.Scope.Valid() {
		return agentcomm.Thread{}, &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: fmt.Sprintf("invalid scope %q (want stage or run)", req.Scope)}
	}
	req.Title = o.RedactSecrets(req.Title)

	threadID := deriveMeshThreadID(req.OperationID)
	if th, replayed, err := o.replayedOpenThread(threadID, req); replayed {
		return th, err
	}
	rs := o.opts.Store.Snapshot()
	if _, known := rs.Stages[req.OriginStage]; req.OriginStage != "" && !known {
		return agentcomm.Thread{}, &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: fmt.Sprintf("unknown origin stage %q", req.OriginStage)}
	}
	effects, err := threadOpenEffects(rs, threadID, req.Title, req.Scope, req.Members, req.MaxMessages, req.OriginStage)
	if err != nil {
		return agentcomm.Thread{}, err
	}

	op := state.Operation{ID: req.OperationID, Kind: opKindMeshOpenThread, Actor: req.Actor, Effects: effects}
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishThreadOpened(threadID, req.Title, req.Members)
	})
	if err != nil {
		return agentcomm.Thread{}, err
	}

	return openedThread(threadID, req), nil
}

// openedThread is the receipt of the open req describes: the thread as that operation opened it.
func openedThread(threadID agentcomm.ThreadID, req OpenThreadRequest) agentcomm.Thread {
	return agentcomm.Thread{
		ID: threadID, Title: req.Title, Scope: req.Scope, Members: req.Members, Status: agentcomm.ThreadOpen,
		Budgets: agentcomm.ThreadBudgets{MaxMessages: req.MaxMessages}, OriginStage: req.OriginStage,
	}
}

// replayedOpenThread answers an open whose request id already committed with the thread as that operation
// opened it — never its current state (a later close does not change the original receipt). Any other request
// under that id is a conflict. replayed=false: nothing committed.
func (o *Orchestrator) replayedOpenThread(threadID agentcomm.ThreadID, req OpenThreadRequest) (agentcomm.Thread, bool, error) {
	op, committed, err := o.opts.Store.CommittedOperation(req.OperationID, opKindMeshOpenThread)
	if err != nil || !committed {
		return agentcomm.Thread{}, err != nil, err // a storage error answers the request too
	}
	var p state.MeshThreadOpenedPayload
	target, found, err := op.Effect("mesh_thread_opened", &p)
	if err != nil {
		return agentcomm.Thread{}, true, err
	}
	members := make([]string, len(req.Members))
	for i, m := range req.Members {
		members[i] = string(m)
	}
	if !found || agentcomm.ThreadID(target.ThreadID) != threadID || !o.sameRedactedText(p.Title, req.Title) || p.Scope != string(req.Scope) ||
		p.MaxMessages != req.MaxMessages || p.OriginStage != req.OriginStage || !slices.Equal(p.Members, members) {
		return agentcomm.Thread{}, true, state.OperationIDConflictError(req.OperationID)
	}
	return openedThread(threadID, req), true, nil
}

// threadOpenEffects builds the effects that open threadID with members: registering every member the mesh
// roster does not hold yet, then the thread itself — shared by OpenThread and SendFromAgent, which opens a
// conversation in the same operation as its first message. originStage is the stage the thread is opened
// from (agentcomm.Thread.OriginStage).
func threadOpenEffects(rs state.RunState, threadID agentcomm.ThreadID, title string, scope agentcomm.Scope, members []agentcomm.ParticipantID, maxMessages int, originStage string) ([]state.Effect, error) {
	effects, err := ensureParticipantEffects(rs, members)
	if err != nil {
		return nil, err
	}
	memberIDs := make([]string, len(members))
	for i, m := range members {
		memberIDs[i] = string(m)
	}
	threadEff, err := marshalEffect("mesh_thread_opened", state.Target{ThreadID: string(threadID)},
		state.MeshThreadOpenedPayload{Title: title, Scope: string(scope), Members: memberIDs, MaxMessages: maxMessages, OriginStage: originStage})
	if err != nil {
		return nil, err
	}
	return append(effects, threadEff), nil
}

func (o *Orchestrator) publishThreadOpened(threadID agentcomm.ThreadID, title string, members []agentcomm.ParticipantID) {
	for _, m := range members {
		o.publishNotice(bus.Event{Type: bus.EventMeshParticipant, Data: map[string]any{"participant_id": string(m)}})
	}
	o.publishNotice(bus.Event{Type: bus.EventMeshThread, Data: map[string]any{"thread_id": string(threadID), "title": title}})
}

// UpdateThreadTitle commits a compare-and-set display title. The store checks the revision under
// its write lock and returns the original seq for a repeated operation id, even after later edits.
func (o *Orchestrator) UpdateThreadTitle(ctx context.Context, req UpdateThreadTitleRequest) (UpdateThreadTitleReceipt, error) {
	if req.OperationID == "" {
		return UpdateThreadTitleReceipt{}, errors.New("agentcomm: operation id is required")
	}
	if err := rejectReservedRequestID(req.OperationID, req.Actor); err != nil {
		return UpdateThreadTitleReceipt{}, err
	}
	if req.Actor.Kind != actorKindUser {
		return UpdateThreadTitleReceipt{}, &state.OperationRejectedError{Code: assistants.CodeActorNotAllowed, Reason: "only the run owner may edit a thread title"}
	}
	title := o.RedactSecrets(strings.TrimSpace(req.Title))
	if committed, ok, err := o.opts.Store.CommittedOperation(req.OperationID, "mesh_update_thread_title"); err != nil {
		return UpdateThreadTitleReceipt{}, err
	} else if ok {
		var p state.MeshThreadTitleUpdatedPayload
		target, found, err := committed.Effect("mesh_thread_title_updated", &p)
		if err != nil {
			return UpdateThreadTitleReceipt{}, err
		}
		if !found || target.ThreadID != string(req.ThreadID) || !o.sameRedactedText(p.Title, title) || p.ExpectedTitleSeq != req.ExpectedTitleSeq {
			return UpdateThreadTitleReceipt{}, state.OperationIDConflictError(req.OperationID)
		}
		return UpdateThreadTitleReceipt{Title: title, TitleSeq: committed.Result.Seq}, nil
	}
	eff, err := marshalEffect("mesh_thread_title_updated", state.Target{ThreadID: string(req.ThreadID)},
		state.MeshThreadTitleUpdatedPayload{Title: title, ExpectedTitleSeq: req.ExpectedTitleSeq})
	if err != nil {
		return UpdateThreadTitleReceipt{}, err
	}
	op := state.Operation{ID: req.OperationID, Kind: "mesh_update_thread_title", Actor: req.Actor, Effects: []state.Effect{eff},
		Expect: state.Preconditions{Check: func(rs *state.RunState) error {
			if _, ok := rs.Assistants.Mesh.Threads[req.ThreadID]; !ok {
				return &state.OperationRejectedError{Code: assistants.CodeNotFound, Reason: fmt.Sprintf("unknown thread %s", req.ThreadID)}
			}
			if got := rs.Assistants.Mesh.ThreadTitles[req.ThreadID].Seq; got != req.ExpectedTitleSeq {
				return &state.OperationRejectedError{Code: assistants.CodeStaleRevision, Reason: fmt.Sprintf("thread %s title changed (expected %d, got %d)", req.ThreadID, req.ExpectedTitleSeq, got)}
			}
			return nil
		}},
	}
	res, err := o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventMeshThread, Data: map[string]any{"thread_id": string(req.ThreadID), "action": "title_updated", "title": title}})
	})
	if err != nil {
		return UpdateThreadTitleReceipt{}, err
	}
	return UpdateThreadTitleReceipt{Title: title, TitleSeq: res.Seq}, nil
}

// CloseThread implements Service.CloseThread. Every still-pending wait whose condition names this thread (an
// addressed-message wait on it, or a reply-to wait on one of its messages) is resolved in the SAME commit —
// decided by applyMeshThreadClosed itself (pkg/state/mesh_projection.go), not precomputed here from a
// snapshot, so the resolution can never race a concurrent Wait registration nor break idempotent replay. The
// waits this commit cancelled are announced and their waiters resumed (announceAndWakeEnded): a stage parked
// in awaiting_peer on one continues as "wait ended".
func (o *Orchestrator) CloseThread(ctx context.Context, req CloseThreadRequest) error {
	if req.OperationID == "" {
		return errors.New("agentcomm: operation id is required")
	}
	if err := rejectReservedRequestID(req.OperationID, req.Actor); err != nil {
		return err
	}

	closeEff, err := marshalEffect("mesh_thread_closed", state.Target{ThreadID: string(req.ThreadID)}, struct{}{})
	if err != nil {
		return err
	}

	op := state.Operation{ID: req.OperationID, Kind: "mesh_close_thread", Actor: req.Actor, Effects: []state.Effect{closeEff}}
	_, err = o.opts.Store.ApplyRunOperation(op, func(res state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventMeshThread, Data: map[string]any{"thread_id": string(req.ThreadID), "status": "closed"}})
		reason := fmt.Sprintf("thread %s was closed", req.ThreadID)
		o.announceAndWakeEnded(res.EndedWaits, func(agentcomm.WaitID) string { return reason })
	})
	return err
}

// rejectUnreachableRecipients refuses a message to a participant nobody will read for: its stage or task has
// finished, or it is a script stage, which runs no agent. Reachability is the full roster's (o.liveParticipant),
// so a send by exact id is refused for exactly what the roster shows as unreachable. Senders run it as their
// operation's Preconditions.Check, against the state under the commit's own lock (a recipient can finish between
// any earlier snapshot and the commit); an idempotent replay of an already-committed send skips it, like every
// precondition, and returns its original outcome.
func (o *Orchestrator) rejectUnreachableRecipients(rs state.RunState, recipients []agentcomm.ParticipantID) error {
	for _, r := range recipients {
		if p, ok := o.liveParticipant(rs, r); ok && !p.Reachable {
			return &state.OperationRejectedError{
				Code: assistants.CodeInvalidStatus, Reason: fmt.Sprintf("%s cannot receive messages (%s)", r, p.UnreachableReason),
			}
		}
	}
	return nil
}

// liveParticipant is resolveParticipant with what only the stage config knows (withStageConfig) — the same
// derivation the full roster (liveRoster) uses, for one id.
func (o *Orchestrator) liveParticipant(rs state.RunState, id agentcomm.ParticipantID) (agentcomm.Participant, bool) {
	p, ok := resolveParticipant(rs, id)
	if !ok {
		return p, false
	}
	if stageID, isStage := stageIDFromMeshParticipant(id); isStage && o.reg() != nil {
		if s := o.reg().Stage(stageID); s != nil {
			p = o.withStageConfig(p, *s)
		}
	}
	p = o.sideParticipantAvailable(p)
	return p, true
}

// Send implements Service.Send. One ApplyRunOperation commits the message plus one accepted receipt/inbox
// reference per recipient (applyMeshMessageSent, pkg/state/mesh_projection.go) — a recipient the thread
// rejects (not a member, or the thread is closed) fails the WHOLE call, nothing is written. Any PENDING wait
// this message satisfies (reply_to the message it answers, or addressed_message in its thread for one of its
// recipients — Agent Mesh Phase 5, T5.3: "a reply arriving makes exactly one continuation") is resolved by
// applyMeshMessageSent itself, in the SAME commit, and the commit's result names them
// (OperationResult.ReadyWaits) for deliverCommittedMessages to announce and resume.
func (o *Orchestrator) Send(ctx context.Context, req SendRequest) (SendReceipt, error) {
	return o.sendWithOpen(ctx, req, nil)
}

// sendWithOpen is Send with openThread — the effects opening req.ThreadID when SendFromAgent starts a new
// conversation — committed in the SAME operation as the message: a send that is rejected leaves no empty
// conversation behind.
func (o *Orchestrator) sendWithOpen(ctx context.Context, req SendRequest, openThread []state.Effect) (SendReceipt, error) {
	if slices.Contains(req.Recipients, o.SideAgentParticipantID()) {
		o.sideAgentGate.RLock()
		defer o.sideAgentGate.RUnlock()
	}
	if req.OperationID == "" {
		return SendReceipt{}, errors.New("agentcomm: operation id is required")
	}
	if err := rejectReservedRequestID(req.OperationID, req.Actor); err != nil {
		return SendReceipt{}, err
	}
	if req.Sender == "" {
		return SendReceipt{}, errors.New("agentcomm: sender is required")
	}

	rs := o.opts.Store.Snapshot()
	if receipt, replayed, err := o.replayedSend(rs, o.liveRoster(rs), req); replayed {
		return receipt, err
	}

	msg, err := o.planMessage(rs, req, deriveMeshMessageID(req.OperationID), openThread)
	if err != nil {
		return SendReceipt{}, err
	}
	msgs := []plannedMessage{msg}
	runCtx := o.runContext(ctx) // work this message starts or resumes must not die with the HTTP request.

	op := state.Operation{ID: req.OperationID, Kind: opKindMeshSend, Actor: req.Actor, Effects: msg.effects, Expect: state.Preconditions{
		Check: func(cur *state.RunState) error {
			if err := o.rejectUnreachableRecipients(*cur, recipientsOf(msgs)); err != nil {
				return err
			}
			if req.AutoResult && req.Sender == o.SideAgentParticipantID() && req.InReplyTo != nil {
				if explicitSideReply(cur.Assistants.Mesh, req.Sender, *req.InReplyTo) {
					return &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: ErrSideAgentExplicitReply.Error(), Cause: ErrSideAgentExplicitReply}
				}
			}
			return nil
		},
	}}
	res, err := o.opts.Store.ApplyRunOperation(op, func(res state.OperationResult) {
		o.deliverCommittedMessages(runCtx, msgs, res.ReadyWaits)
	})
	if err != nil {
		return SendReceipt{}, err
	}
	return SendReceipt{MessageID: msg.messageID, Seq: res.Seq, ThreadID: req.ThreadID}, nil
}

func explicitSideReply(mesh state.MeshState, sender agentcomm.ParticipantID, source agentcomm.MessageID) bool {
	for _, m := range mesh.Messages {
		if m != nil && m.Sender == sender && !m.AutoResult && m.InReplyTo != nil && *m.InReplyTo == source {
			return true
		}
	}
	return false
}

// plannedMessage is one message an operation commits: the request it was built from (body already redacted),
// its id, and the effects that record it.
type plannedMessage struct {
	send         SendRequest
	messageID    agentcomm.MessageID
	openedThread bool
	effects      []state.Effect
}

// planMessage is the one place a message's effects are assembled — for Send, for board notices, and for any
// other operation that carries a message: openThread (the effects opening req.ThreadID in the same operation,
// if any), the mesh_message_sent itself with its body and artifact references redacted, and its Discussion mirror when it answers a
// discussion message. The operation commits them with recipientsReachable as its Preconditions.Check, and its
// post-commit work calls deliverCommittedMessages.
func (o *Orchestrator) planMessage(rs state.RunState, req SendRequest, messageID agentcomm.MessageID, openThread []state.Effect) (plannedMessage, error) {
	req.Body = o.RedactSecrets(req.Body)
	req.ArtifactRefs = o.redactAll(req.ArtifactRefs)
	eff, err := messageSentEffect(req, messageID)
	if err != nil {
		return plannedMessage{}, err
	}
	effects := append(slices.Clone(openThread), eff)
	if mirror, ok, err := discussionMirrorOfReply(rs, req, messageID, req.Body); err != nil {
		return plannedMessage{}, err
	} else if ok {
		effects = append(effects, mirror)
	}
	return plannedMessage{send: req, messageID: messageID, openedThread: len(openThread) > 0, effects: effects}, nil
}

// recipientsOf lists every recipient of msgs, in order.
func recipientsOf(msgs []plannedMessage) []agentcomm.ParticipantID {
	var out []agentcomm.ParticipantID
	for _, m := range msgs {
		out = append(out, m.send.Recipients...)
	}
	return out
}

// recipientsReachable is the commit-time check of an operation carrying msgs: rejectUnreachableRecipients over
// all their recipients, against the state under the commit's lock.
func (o *Orchestrator) recipientsReachable(msgs []plannedMessage) func(*state.RunState) error {
	recipients := recipientsOf(msgs)
	return func(rs *state.RunState) error { return o.rejectUnreachableRecipients(*rs, recipients) }
}

// deliverCommittedMessages is the post-commit tail of every operation that commits messages: publish each
// message's live notices, resume the waits the commit made ready (readyWaits, OperationResult.ReadyWaits), and
// give the recipients the turn they may need.
func (o *Orchestrator) deliverCommittedMessages(runCtx context.Context, msgs []plannedMessage, readyWaits []agentcomm.WaitID) {
	for _, m := range msgs {
		o.announceMessage(m.send, m.messageID, m.openedThread)
	}
	o.notifyAndResumeWaits(runCtx, readyWaits)
	o.wakeRecipients(runCtx, recipientsOf(msgs))
}

// messageSentEffect builds the mesh_message_sent effect of req; req.Body and req.ArtifactRefs must already be
// redacted.
func messageSentEffect(req SendRequest, messageID agentcomm.MessageID) (state.Effect, error) {
	recipients := make([]string, len(req.Recipients))
	for i, r := range req.Recipients {
		recipients[i] = string(r)
	}
	var inReplyTo *string
	if req.InReplyTo != nil {
		s := string(*req.InReplyTo)
		inReplyTo = &s
	}
	return marshalEffect("mesh_message_sent", state.Target{ThreadID: string(req.ThreadID), MessageID: string(messageID)},
		meshMessageSentRecord{
			MeshMessageSentPayload: state.MeshMessageSentPayload{
				Sender: string(req.Sender), Recipients: recipients, Kind: string(req.Kind),
				InReplyTo: inReplyTo, Body: req.Body, ArtifactRefs: req.ArtifactRefs, AutoResult: req.AutoResult,
			},
			OriginStage: req.OriginStage,
		})
}

// meshMessageSentRecord is the mesh_message_sent payload as the orchestrator journals it: the projection's
// payload plus the request's intent the projection does not need — OriginStage, the stage feed a send without a
// thread was written from, so a retry under the same request id from another feed is a conflict, not a replay.
// An additive omitempty field: older journals and readers are unaffected; a record without it is retried by the
// legacy rule (the feed is not compared).
type meshMessageSentRecord struct {
	state.MeshMessageSentPayload
	OriginStage string `json:"origin_stage,omitempty"`
}

// announceMessage publishes the live notices of a committed message — and of its thread, when the same
// operation opened it.
func (o *Orchestrator) announceMessage(req SendRequest, messageID agentcomm.MessageID, openedThread bool) {
	if openedThread {
		o.publishThreadOpened(req.ThreadID, "", append([]agentcomm.ParticipantID{req.Sender}, req.Recipients...))
	}
	o.publishNotice(bus.Event{Type: bus.EventMeshMessage, Data: map[string]any{
		"message_id": string(messageID), "thread_id": string(req.ThreadID), "sender_id": string(req.Sender), "kind": string(req.Kind),
	}})
}

// wakeRecipients gives the recipients of a committed message the turn it may need. Agent Mesh Phase 5, T5.5: a
// recipient parked in awaiting_peer/waiting_peer whose OWN wait the message did NOT resolve
// (notifyAndResumeNewlyReadyWaits owns that case) may still need a bounded turn to read and answer it — a
// genuine counter-question, not the reply it is waiting for. A role attached with no first message sits
// queued until somebody writes to it: its first addressed message starts it (maybeScheduleTask no-ops for
// anything not queued).
func (o *Orchestrator) wakeRecipients(runCtx context.Context, recipients []agentcomm.ParticipantID) {
	for _, r := range recipients {
		o.maybeResumeAwaitingPeerQuestion(runCtx, r)
		if taskID, ok := o.taskIDFromMeshParticipant(r); ok {
			o.maybeScheduleTask(runCtx, taskID)
		}
	}
}

// ReadThread implements Service.ReadThread. Membership is enforced here (not by the projection, which has
// no notion of "the caller"): a non-member cannot read a thread, except the human, who sees every thread in
// the run (the run owner — tmp/agent-mesh.md §5.4).
func (o *Orchestrator) ReadThread(ctx context.Context, req ReadThreadRequest) (MessagePage, error) {
	rs := o.opts.Store.Snapshot()
	thread, ok := rs.Assistants.Mesh.Threads[req.ThreadID]
	if !ok {
		return MessagePage{}, &state.OperationRejectedError{Code: assistants.CodeNotFound, Reason: fmt.Sprintf("unknown thread %s", req.ThreadID)}
	}
	if req.Reader != meshUserParticipantID && req.Reader != "" && !threadHasMember(thread, req.Reader) {
		return MessagePage{}, &state.OperationRejectedError{Code: assistants.CodeActorNotAllowed, Reason: fmt.Sprintf("%s is not a member of thread %s", req.Reader, req.ThreadID)}
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
	page := ThreadPage{Threads: []agentcomm.Thread{}, Summaries: map[agentcomm.ThreadID]ThreadSummary{}}
	sees := req.Participant == meshUserParticipantID || req.Participant == ""
	for _, id := range rs.Assistants.Mesh.ThreadOrder {
		th := rs.Assistants.Mesh.Threads[id]
		if th == nil {
			continue
		}
		if !sees && !threadHasMember(th, req.Participant) {
			continue
		}
		view, _ := rs.Assistants.Mesh.ThreadView(id)
		page.Threads = append(page.Threads, view)
		page.Summaries[id] = summarizeThreadMessages(rs.Assistants.Mesh, id)
	}
	return page, nil
}

// summarizeThreadMessages reads only this thread's bounded commit-order index. Looking backward finds the
// latest message and the latest message from somebody other than the human owner without a per-row store read or receipt change.
func summarizeThreadMessages(mesh state.MeshState, id agentcomm.ThreadID) ThreadSummary {
	summary := ThreadSummary{TitleSeq: mesh.ThreadTitles[id].Seq}
	ids := mesh.MessagesByThread[id]
	for i := len(ids) - 1; i >= 0; i-- {
		message := mesh.Messages[ids[i]]
		if message == nil {
			continue
		}
		if summary.LastMessage == nil {
			summary.LastMessage = &ThreadSummaryMessage{
				ID: message.ID, Seq: message.Seq, CreatedAt: message.CreatedAt,
				SenderID: message.Sender, Preview: threadMessagePreview(message.Body),
			}
		}
		if summary.LastOtherMessageSeq == 0 && message.Sender != meshUserParticipantID {
			summary.LastOtherMessageSeq = message.Seq
		}
		if summary.LastMessage != nil && summary.LastOtherMessageSeq != 0 {
			break
		}
	}
	return summary
}

func threadMessagePreview(body string) string {
	plain := strings.Join(strings.Fields(body), " ")
	runes := []rune(plain)
	if len(runes) > 160 {
		return string(runes[:160])
	}
	return plain
}

// ReadInbox implements Service.ReadInbox: every message ever addressed to Participant (or, with OnlyNew, only
// those not yet handed to it), newest additions last. It does NOT transition any receipt to delivered — "delivered" is earned only at the actual handoff
// point (a turn's prompt package, or an explicit MarkMessageDelivered call from the `afm agent inbox`
// handler), never by this read alone ("неявного ack при чтении нет", tmp/agent-mesh.md §6.1).
func (o *Orchestrator) ReadInbox(ctx context.Context, req ReadInboxRequest) (MessagePage, error) {
	rs := o.opts.Store.Snapshot()
	ids := rs.Assistants.Mesh.Inbox[req.Participant]
	if req.OnlyNew {
		ids = slices.DeleteFunc(slices.Clone(ids), func(id agentcomm.MessageID) bool {
			return rs.Assistants.Mesh.Receipts[id][req.Participant] != agentcomm.DeliveryAccepted
		})
	}
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
	if err := rejectReservedRequestID(req.OperationID, req.Actor); err != nil {
		return err
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

// SideAgentMeshResponseFailed durably releases a source whose terminal response
// cannot be added to its thread. The receipt and pending reply waits change in
// one operation; a keyed notice gives reconnecting clients the visible outcome.
func (o *Orchestrator) SideAgentMeshResponseFailed(id agentcomm.MessageID, reason string) error {
	reason = o.RedactSecrets(reason)
	if len(reason) > 256 {
		reason = reason[:256]
	}
	recipient := o.SideAgentParticipantID()
	for range maxUnreadReleaseAttempts {
		op, _, err := unreadReleaseOp(o.opts.Store.Snapshot(), id, recipient, reason)
		if err != nil || op == nil {
			return err
		}
		op.ID = "side-mesh-reply:" + string(id)
		op.Kind = "side_mesh_response_failed"
		check := op.Expect.Check
		op.Expect.Check = func(cur *state.RunState) error {
			if answeredBy(cur.Assistants.Mesh, recipient, id) {
				return &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: ErrSideAgentExplicitReply.Error(), Cause: ErrSideAgentExplicitReply}
			}
			return check(cur)
		}
		_, err = o.opts.Store.ApplyRunOperation(*op, func(res state.OperationResult) {
			o.publishNotice(bus.Event{Type: bus.EventMeshReceipt, Data: map[string]any{
				"message_id": string(id), "participant_id": string(recipient), "status": string(agentcomm.DeliveryUndeliverable),
			}})
			o.publishNotice(bus.Event{Type: bus.EventSideAgentMeshFailure, Data: map[string]any{
				"source_message_id": string(id), "reason": reason,
			}})
			o.announceAndWakeEnded(res.EndedWaits, func(agentcomm.WaitID) string { return reason })
		})
		if errors.Is(err, errUnreadReleaseStale) {
			continue
		}
		return err
	}
	return errUnreadReleaseStale
}

// SideAgentMeshResponseFailedAlready reports the durable terminal marker used
// when a thread cannot contain a linked result.
func (o *Orchestrator) SideAgentMeshResponseFailedAlready(id agentcomm.MessageID) bool {
	rs := o.opts.Store.Snapshot()
	return rs.Assistants.Mesh.Receipts[id][o.SideAgentParticipantID()] == agentcomm.DeliveryUndeliverable
}

// RepublishSideAgentMeshFailure restores the reconnect notice from the durable
// undeliverable receipt after a crash between operation commit and notice write.
// The source id is the stable UI key, so replays collapse to one visible outcome.
func (o *Orchestrator) RepublishSideAgentMeshFailure(id agentcomm.MessageID) {
	o.publishNotice(bus.Event{Type: bus.EventSideAgentMeshFailure, Data: map[string]any{
		"source_message_id": string(id), "reason": "Could not answer: conversation unavailable",
	}})
}

// SideAgentMeshSourceAccepted remains true until OnDelivered records a real
// subprocess start. A turn_started journal event occurs earlier than cmd.Start.
func (o *Orchestrator) SideAgentMeshSourceAccepted(id agentcomm.MessageID) bool {
	rs := o.opts.Store.Snapshot()
	return rs.Assistants.Mesh.Receipts[id][o.SideAgentParticipantID()] == agentcomm.DeliveryAccepted
}

// SideAgentMeshMarkUnprocessed closes an accepted receipt when no subprocess
// started for this source. A linked failure result, when possible, is committed
// first so reply waits resolve as answered rather than cancelled.
func (o *Orchestrator) SideAgentMeshMarkUnprocessed(id agentcomm.MessageID) error {
	if o.SideAgentMeshResponseFailedAlready(id) {
		return nil
	}
	recipient := o.SideAgentParticipantID()
	for range maxUnreadReleaseAttempts {
		snap := o.opts.Store.Snapshot()
		if explicitSideReply(snap.Assistants.Mesh, recipient, id) {
			return o.Acknowledge(context.Background(), AcknowledgeRequest{OperationID: "side-mesh-unprocessed:" + string(id), MessageID: id, Participant: recipient, Actor: state.Actor{Kind: "system"}})
		}
		op, _, err := unreadReleaseOp(snap, id, recipient, "side agent did not start")
		if err != nil || op == nil {
			return err
		}
		op.ID = "side-mesh-unprocessed:" + string(id)
		op.Kind = "side_mesh_unprocessed"
		check := op.Expect.Check
		op.Expect.Check = func(cur *state.RunState) error {
			if explicitSideReply(cur.Assistants.Mesh, recipient, id) {
				return &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: ErrSideAgentExplicitReply.Error(), Cause: ErrSideAgentExplicitReply}
			}
			return check(cur)
		}
		_, err = o.opts.Store.ApplyRunOperation(*op, func(res state.OperationResult) {
			o.publishNotice(bus.Event{Type: bus.EventMeshReceipt, Data: map[string]any{
				"message_id": string(id), "participant_id": string(recipient), "status": string(agentcomm.DeliveryUndeliverable),
			}})
			o.announceAndWakeEnded(res.EndedWaits, func(agentcomm.WaitID) string { return "side agent did not start" })
		})
		if errors.Is(err, errUnreadReleaseStale) {
			continue
		}
		if errors.Is(err, ErrSideAgentExplicitReply) {
			return nil
		}
		return err
	}
	return errUnreadReleaseStale
}

func sortedWaitIDs(waits map[agentcomm.WaitID]*state.MeshWait) []agentcomm.WaitID {
	ids := make([]agentcomm.WaitID, 0, len(waits))
	for id := range waits {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
