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
	recipients, err := resolveRecipients(roster, req.Recipients)
	if err != nil {
		return SendReceipt{}, err
	}
	req.Recipients = recipients

	if req.ThreadID == "" && req.InReplyTo != nil {
		answered, ok := rs.Assistants.Mesh.Messages[*req.InReplyTo]
		if !ok {
			return SendReceipt{}, &state.OperationRejectedError{Code: assistants.CodeNotFound, Reason: fmt.Sprintf("unknown message %s", *req.InReplyTo)}
		}
		req.ThreadID = answered.ThreadID
		if len(req.Recipients) == 0 {
			req.Recipients = []agentcomm.ParticipantID{answered.Sender}
		}
	}

	if req.ThreadID == "" {
		if len(req.Recipients) == 0 {
			return SendReceipt{}, &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: "no recipients — pass --to (see `afm agent list`)"}
		}
		members := append([]agentcomm.ParticipantID{req.Sender}, req.Recipients...)
		if th := openThreadWithMembers(rs, members); th != "" {
			req.ThreadID = th
		} else {
			opened, err := o.OpenThread(ctx, OpenThreadRequest{
				OperationID: req.OperationID + "/open", Scope: agentcomm.ScopeRun, Members: members, Actor: req.Actor,
			})
			if err != nil {
				return SendReceipt{}, err
			}
			req.ThreadID = opened.ID
		}
	}
	return o.Send(ctx, req)
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

// openThreadWithMembers returns the open thread whose member set is exactly members (order-insensitive), or
// "" — the conversation an agent continues when it messages the same colleagues again.
func openThreadWithMembers(rs state.RunState, members []agentcomm.ParticipantID) agentcomm.ThreadID {
	want := slices.Clone(members)
	slices.Sort(want)
	want = slices.Compact(want)
	for _, id := range rs.Assistants.Mesh.ThreadOrder {
		th := rs.Assistants.Mesh.Threads[id]
		if th == nil || th.Status != agentcomm.ThreadOpen {
			continue
		}
		got := slices.Clone(th.Members)
		slices.Sort(got)
		if slices.Equal(got, want) {
			return id
		}
	}
	return ""
}
