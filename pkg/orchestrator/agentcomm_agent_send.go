package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// SendFromAgent is Send for a sender that knows names, not thread ids: an agent's own `afm agent send` /
// `afm agent reply`, and the human writing from a stage feed's composer (Sender "user", OriginStage set):
//   - recipients may be short aliases ("docs", "@docs") as well as participant ids;
//   - a reply without a thread goes into the thread of the message it answers (and, without recipients,
//     back to that message's sender);
//   - any other send without a thread goes into the open conversation with exactly the sender and those
//     recipients (and, with OriginStage, shown in that stage's feed), or opens one IN THE SAME operation as
//     the message, so a send that is rejected leaves no empty conversation behind. The new thread's id is derived from the member set, so two agents starting
//     the same conversation at once open the same thread.
func (o *Orchestrator) SendFromAgent(ctx context.Context, req SendRequest) (SendReceipt, error) {
	if err := rejectReservedRequestID(req.OperationID, req.Actor); err != nil {
		return SendReceipt{}, err
	}
	rs := o.opts.Store.Snapshot()
	roster := o.liveRoster(rs)
	// A retry of an already-committed request replays its outcome, whatever changed since (a new agent with
	// the same alias, the conversation closed) — never re-resolving names or picking another thread.
	if receipt, replayed, err := o.replayedSend(rs, roster, req); replayed {
		return receipt, err
	}
	recipients, err := resolveRecipients(roster, req.Recipients)
	if err != nil {
		return SendReceipt{}, err
	}
	req.Recipients = recipients

	if req.InReplyTo != nil {
		answered, ok := rs.Assistants.Mesh.Messages[*req.InReplyTo]
		if !ok {
			return SendReceipt{}, &state.OperationRejectedError{Code: assistants.CodeNotFound, Reason: fmt.Sprintf("unknown message %s", *req.InReplyTo)}
		}
		if req.ThreadID == "" {
			req.ThreadID = answered.ThreadID
		}
		if len(req.Recipients) == 0 {
			req.Recipients = []agentcomm.ParticipantID{answered.Sender}
		}
	}
	if req.ThreadID != "" {
		return o.Send(ctx, req)
	}

	if len(req.Recipients) == 0 {
		return SendReceipt{}, &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: "no recipients — pass --to (see `afm agent list`)"}
	}
	originStage := req.OriginStage
	if _, known := rs.Stages[originStage]; originStage != "" && !known {
		return SendReceipt{}, &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: fmt.Sprintf("unknown origin stage %q", originStage)}
	}
	members := conversationMembers(req.Sender, req.Recipients)
	if th := openThreadWithMembers(rs, members, req.Sender, originStage); th != "" {
		req.ThreadID = th
		return o.Send(ctx, req)
	}
	if originStage == "" {
		originStage = participantStage(rs, req.Sender)
	}
	// The requested feed is part of a new conversation's identity: two feeds opening a conversation with the
	// same people at once get two threads, each shown in its own feed (one shared id would make the second open
	// a no-op that drops its message into the first feed's thread).
	req.ThreadID = deriveMeshThreadID(conversationOpenID(rs, members, req.OriginStage))
	open, err := threadOpenEffects(rs, req.ThreadID, "", agentcomm.ScopeRun, members, 0, originStage)
	if err != nil {
		return SendReceipt{}, err
	}
	return o.sendWithOpen(ctx, req, open)
}

// replayedSend answers a send whose request id already committed, from the committed message: the same
// message is its receipt, any other request under that id is a conflict. replayed=false: nothing committed.
func (o *Orchestrator) replayedSend(rs state.RunState, roster []agentcomm.Participant, req SendRequest) (SendReceipt, bool, error) {
	op, committed, err := o.opts.Store.CommittedOperation(req.OperationID, opKindMeshSend)
	if err != nil || !committed {
		return SendReceipt{}, err != nil, err // a storage error answers the request too
	}
	var p meshMessageSentRecord
	target, found, err := op.Effect("mesh_message_sent", &p)
	if err != nil {
		return SendReceipt{}, true, err
	}
	// A record without origin_stage (written before it was kept, or a send from no feed) keeps the legacy retry
	// rule: the feed is not compared.
	if !found || (p.OriginStage != "" && p.OriginStage != req.OriginStage) || !o.committedSendMatches(rs, roster, committedMessage(target, p.MeshMessageSentPayload), req) {
		return SendReceipt{}, true, state.OperationIDConflictError(req.OperationID)
	}
	return SendReceipt{MessageID: agentcomm.MessageID(target.MessageID), Seq: op.Result.Seq, ThreadID: agentcomm.ThreadID(target.ThreadID)}, true, nil
}

// committedMessage is the message a committed mesh_message_sent effect recorded.
func committedMessage(target state.Target, p state.MeshMessageSentPayload) agentcomm.Message {
	m := agentcomm.Message{
		ID: agentcomm.MessageID(target.MessageID), ThreadID: agentcomm.ThreadID(target.ThreadID), Sender: agentcomm.ParticipantID(p.Sender),
		Kind: agentcomm.MessageKind(p.Kind), Body: p.Body, ArtifactRefs: p.ArtifactRefs, AutoResult: p.AutoResult,
	}
	for _, r := range p.Recipients {
		m.Recipients = append(m.Recipients, agentcomm.ParticipantID(r))
	}
	if p.InReplyTo != nil {
		id := agentcomm.MessageID(*p.InReplyTo)
		m.InReplyTo = &id
	}
	return m
}

// committedSendMatches reports whether m — the message already committed under req's request id — is the
// message req asks to send: same sender, kind, body, passive flag, reply target, artifacts, thread (when req
// names one) and recipients. Recipient names are resolved against m's own recipients, so an alias that has since become
// ambiguous (a new agent with the same alias) still names whom it named then.
func (o *Orchestrator) committedSendMatches(rs state.RunState, roster []agentcomm.Participant, m agentcomm.Message, req SendRequest) bool {
	if m.Sender != req.Sender || m.Kind != req.Kind || m.AutoResult != req.AutoResult || !o.sameRedactedText(m.Body, req.Body) ||
		!o.sameRedactedList(m.ArtifactRefs, req.ArtifactRefs) || !sameMessageRef(m.InReplyTo, req.InReplyTo) {
		return false
	}
	if req.ThreadID != "" && req.ThreadID != m.ThreadID {
		return false
	}
	want := req.Recipients
	if len(want) == 0 && req.InReplyTo != nil {
		// A reply without recipients went back to the sender of the message it answers.
		if answered, ok := rs.Assistants.Mesh.Messages[*req.InReplyTo]; ok {
			want = []agentcomm.ParticipantID{answered.Sender}
		}
	}
	committed := slices.DeleteFunc(slices.Clone(roster), func(p agentcomm.Participant) bool { return !slices.Contains(m.Recipients, p.ID) })
	resolved, err := resolveRecipients(committed, want)
	return err == nil && slices.Equal(sortedMemberSet(resolved), sortedMemberSet(m.Recipients))
}

func sameMessageRef(a, b *agentcomm.MessageID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// resolveRecipients maps each recipient — a participant id or an alias, optionally "@"-prefixed — to its
// participant id. An unknown name is not_found; an alias shared by several participants is invalid_request
// listing them, never a silent pick.
func resolveRecipients(roster []agentcomm.Participant, names []agentcomm.ParticipantID) ([]agentcomm.ParticipantID, error) {
	out := make([]agentcomm.ParticipantID, 0, len(names))
	for _, name := range names {
		if slices.ContainsFunc(roster, func(p agentcomm.Participant) bool { return p.ID == name }) {
			out = append(out, name)
			continue
		}
		id, err := agentcomm.ResolveAlias(string(name), roster)
		var ambiguous *agentcomm.AmbiguousAliasError
		switch {
		case err == nil:
			out = append(out, id)
		case errors.As(err, &ambiguous):
			return nil, &state.OperationRejectedError{
				Code: assistants.CodeInvalidRequest, Reason: fmt.Sprintf("%q matches several participants %v — use the full id", name, ambiguous.Candidates),
			}
		default:
			return nil, &state.OperationRejectedError{
				Code: assistants.CodeNotFound, Reason: fmt.Sprintf("unknown recipient %q — `afm agent list` shows who is in this run", name),
			}
		}
	}
	return out, nil
}

// conversationMembers is the member set of a conversation sender starts with recipients: them and the human
// (D1) — the human reads every conversation of the run and can answer in it, but is not addressed by it.
func conversationMembers(sender agentcomm.ParticipantID, recipients []agentcomm.ParticipantID) []agentcomm.ParticipantID {
	return sortedMemberSet(append([]agentcomm.ParticipantID{meshUserParticipantID, sender}, recipients...))
}

// conversationOpenID is the key a new conversation of exactly members derives its thread id from: the sorted
// member set, the stage feed it is opened from (originStage, "" for an agent's own send — two agents starting
// the same conversation still meet in one thread) and how many threads with the same agents exist already (so
// a conversation opened after an earlier one was closed gets a fresh id).
func conversationOpenID(rs state.RunState, members []agentcomm.ParticipantID, originStage string) string {
	set := sortedMemberSet(members)
	agents := agentMemberSet(members)
	n := 0
	for _, th := range rs.Assistants.Mesh.Threads {
		if slices.Equal(agentMemberSet(th.Members), agents) {
			n++
		}
	}
	key := ""
	for _, m := range set {
		key += string(m) + "\x00"
	}
	if originStage != "" {
		key += "origin:" + originStage
	}
	return fmt.Sprintf("agent-conversation:%s:%d", deriveMeshID("agent-conversation", key), n)
}

func sortedMemberSet(members []agentcomm.ParticipantID) []agentcomm.ParticipantID {
	set := slices.Clone(members)
	slices.Sort(set)
	return slices.Compact(set)
}

// agentMemberSet is sortedMemberSet without the human: the agents of a conversation.
func agentMemberSet(members []agentcomm.ParticipantID) []agentcomm.ParticipantID {
	return slices.DeleteFunc(sortedMemberSet(members), func(m agentcomm.ParticipantID) bool { return m == meshUserParticipantID })
}

// openThreadWithMembers returns the open conversation of the same agents as members (order-insensitive), or
// "" — the conversation an agent continues when it messages the same colleagues again. The human does not
// count: a conversation opened before the human joined every new thread (D1) is still the one those agents
// continue after a resume. A stage's discussion thread is never picked — it belongs to the human's Discussion
// with the stage, and the agents' own conversation must not end up there. shownIn — a stage feed the
// conversation must show in (threadInStageFeed), so a message written from that feed never lands in a
// conversation it does not show; "" — any conversation. sender — who writes: a conversation it is not a member
// of cannot take its message (the human is not in the agents' conversations opened before D1).
func openThreadWithMembers(rs state.RunState, members []agentcomm.ParticipantID, sender agentcomm.ParticipantID, shownIn string) agentcomm.ThreadID {
	want := agentMemberSet(members)
	for _, id := range rs.Assistants.Mesh.ThreadOrder {
		th := rs.Assistants.Mesh.Threads[id]
		if th == nil || th.Status != agentcomm.ThreadOpen || !slices.Equal(agentMemberSet(th.Members), want) || !slices.Contains(th.Members, sender) {
			continue
		}
		if shownIn != "" && !threadInStageFeed(rs, th, shownIn) {
			continue
		}
		if _, discussion := discussionStageOfThread(rs, id); discussion {
			continue
		}
		return id
	}
	return ""
}
