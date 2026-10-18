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

// SendFromAgent is Send for an agent's own `afm agent send` / `afm agent reply`, which knows names, not
// thread ids:
//   - recipients may be short aliases ("docs", "@docs") as well as participant ids;
//   - a reply without a thread goes into the thread of the message it answers (and, without recipients,
//     back to that message's sender);
//   - any other send without a thread goes into the open conversation with exactly the sender and those
//     recipients, or opens one. The open is its own idempotent operation derived from req.OperationID, so a
//     retried request finds the same thread and replays the same send.
func (o *Orchestrator) SendFromAgent(ctx context.Context, req SendRequest) (SendReceipt, error) {
	rs := o.opts.Store.Snapshot()
	roster := o.liveRoster(rs)
	// A retry of an already-committed request replays its outcome, whatever changed since (a new agent with
	// the same alias, the conversation closed) — never re-resolving names or picking another thread. The same
	// request id with a different message is a conflict, exactly like the store's own idempotency check.
	if m, ok := rs.Assistants.Mesh.Messages[deriveMeshMessageID(req.OperationID)]; ok {
		if !committedSendMatches(rs, roster, *m, req) {
			return SendReceipt{}, state.OperationIDConflictError(req.OperationID)
		}
		return SendReceipt{MessageID: m.ID, Seq: m.Seq, ThreadID: m.ThreadID}, nil
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

	if req.ThreadID == "" {
		if len(req.Recipients) == 0 {
			return SendReceipt{}, &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: "no recipients — pass --to (see `afm agent list`)"}
		}
		members := append([]agentcomm.ParticipantID{req.Sender}, req.Recipients...)
		// Check everything the send itself would reject before opening anything, so a request that cannot be
		// sent leaves no empty conversation behind: the message's shape, the recipients' reachability and their
		// inbox limits — the same validators Send and its projection apply (Send re-checks them at commit).
		draft := agentcomm.Message{Sender: req.Sender, Recipients: req.Recipients, Kind: req.Kind, InReplyTo: req.InReplyTo, Body: req.Body, ArtifactRefs: req.ArtifactRefs}
		if err := agentcomm.ValidateMessage(draft, members, nil); err != nil {
			return SendReceipt{}, &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: err.Error()}
		}
		if err := rejectUnreachableRecipients(rs, req.Recipients); err != nil {
			return SendReceipt{}, err
		}
		if err := rs.Assistants.Mesh.CheckInboxCapacity(req.Recipients); err != nil {
			return SendReceipt{}, err
		}
		if th := openThreadWithMembers(rs, members); th != "" {
			req.ThreadID = th
		} else {
			// The open's operation id is derived from the member set (and how many conversations they already
			// had), not from this request: two agents starting the same conversation at once commit the SAME
			// operation, and the second one is an idempotent replay returning the same thread.
			opened, err := o.OpenThread(ctx, OpenThreadRequest{
				OperationID: conversationOpenID(rs, members), Scope: agentcomm.ScopeRun, Members: sortedMemberSet(members), Actor: req.Actor,
			})
			if err != nil {
				return SendReceipt{}, err
			}
			req.ThreadID = opened.ID
		}
	}
	return o.Send(ctx, req)
}

// committedSendMatches reports whether m — the message already committed under req's request id — is the
// message req asks to send: same sender, kind, body (as redacted at send time), reply target, artifacts,
// thread (when req names one) and recipients. Recipient names are resolved against m's own recipients, so an
// alias that has since become ambiguous (a new agent with the same alias) still names whom it named then.
func committedSendMatches(rs state.RunState, roster []agentcomm.Participant, m agentcomm.Message, req SendRequest) bool {
	if m.Sender != req.Sender || m.Kind != req.Kind || m.Body != redactMeshText(req.Body) ||
		!slices.Equal(m.ArtifactRefs, req.ArtifactRefs) || !sameMessageRef(m.InReplyTo, req.InReplyTo) {
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

// conversationOpenID is the operation id for opening the conversation of exactly members: the sorted member
// set plus how many threads with that exact set exist already (so a conversation opened after an earlier one
// was closed gets a fresh id).
func conversationOpenID(rs state.RunState, members []agentcomm.ParticipantID) string {
	set := sortedMemberSet(members)
	n := 0
	for _, th := range rs.Assistants.Mesh.Threads {
		if slices.Equal(sortedMemberSet(th.Members), set) {
			n++
		}
	}
	key := ""
	for _, m := range set {
		key += string(m) + "\x00"
	}
	return fmt.Sprintf("agent-conversation:%s:%d", deriveMeshID("agent-conversation", key), n)
}

func sortedMemberSet(members []agentcomm.ParticipantID) []agentcomm.ParticipantID {
	set := slices.Clone(members)
	slices.Sort(set)
	return slices.Compact(set)
}

// openThreadWithMembers returns the open thread whose member set is exactly members (order-insensitive), or
// "" — the conversation an agent continues when it messages the same colleagues again.
func openThreadWithMembers(rs state.RunState, members []agentcomm.ParticipantID) agentcomm.ThreadID {
	want := sortedMemberSet(members)
	for _, id := range rs.Assistants.Mesh.ThreadOrder {
		th := rs.Assistants.Mesh.Threads[id]
		if th != nil && th.Status == agentcomm.ThreadOpen && slices.Equal(sortedMemberSet(th.Members), want) {
			return id
		}
	}
	return ""
}
