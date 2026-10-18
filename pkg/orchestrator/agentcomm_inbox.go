package orchestrator

import (
	"context"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/prompts"
)

// Agent Mesh Phase 4, T4.4: the <inbox> block and its delivery marking. A turn's prompt and the set of
// messages marked delivered for it come from ONE read of the state (agentCommInboxFor): a message that
// arrives after the prompt was built is not in it, so it must stay unseen and reach the next turn.

// inboxBlockByteBudget bounds how much message text one turn's <inbox> block carries; inboxMessageOverhead
// approximates each message's header line. Messages past the budget stay accepted for the next turn (or an
// `afm agent inbox` call), and the block says how many are still waiting.
const (
	inboxBlockByteBudget = 64 * 1024
	inboxMessageOverhead = 256
)

// agentCommInboxFor renders participant's pending (not-yet-delivered) inbox messages, oldest first and within
// inboxBlockByteBudget, and returns a closure that marks exactly those messages delivered. The same set is
// remembered as the participant's rendered inbox for deliverRenderedInbox — executor.Config.OnDelivered,
// called only after cmd.Start() succeeds (pkg/executor/executor.go). Marking never happens at
// prompt-assembly time, so a crash or a failed start between building this prompt and spawning the process
// leaves the messages at "accepted" and they are re-injected, unchanged, on the next attempt.
func (o *Orchestrator) agentCommInboxFor(participant agentcomm.ParticipantID) (block string, markDelivered func()) {
	if o.opts.Store == nil {
		// Some tests build a bare Orchestrator literal to exercise a single concern (e.g. browser/mesh Config
		// wiring) without a real Store — nothing to read, so there is nothing pending and nothing to deliver.
		return "", func() {}
	}
	rs := o.opts.Store.Snapshot()

	var pending []agentcomm.MessageID
	var msgs []prompts.AgentCommInboxMessage
	size, more := 0, 0
	for _, id := range rs.Assistants.Mesh.Inbox[participant] {
		m := rs.Assistants.Mesh.Messages[id]
		if m == nil || rs.Assistants.Mesh.Receipts[id][participant] != agentcomm.DeliveryAccepted {
			continue
		}
		size += len(m.Body) + inboxMessageOverhead
		if len(msgs) > 0 && size > inboxBlockByteBudget {
			more++
			continue
		}
		pending = append(pending, id)
		msgs = append(msgs, inboxMessageView(m))
	}

	o.renderedInbox.Store(participant, pending)
	return prompts.AgentCommInboxBlock(msgs, more), func() { o.markInboxDelivered(participant, pending) }
}

func inboxMessageView(m *agentcomm.Message) prompts.AgentCommInboxMessage {
	var inReplyTo string
	if m.InReplyTo != nil {
		inReplyTo = string(*m.InReplyTo)
	}
	recipients := make([]string, len(m.Recipients))
	for i, r := range m.Recipients {
		recipients[i] = string(r)
	}
	return prompts.AgentCommInboxMessage{
		MessageID: string(m.ID), ThreadID: string(m.ThreadID), From: string(m.Sender),
		Recipients: recipients, InReplyTo: inReplyTo, Body: m.Body,
	}
}

// deliverRenderedInbox returns the executor.Config.OnDelivered callback for participant: it marks the
// messages of participant's latest rendered <inbox> block delivered — never a fresh read of the inbox, which
// could include a message that arrived after the prompt was built.
func (o *Orchestrator) deliverRenderedInbox(participant agentcomm.ParticipantID) func() {
	return func() {
		if v, ok := o.renderedInbox.LoadAndDelete(participant); ok {
			o.markInboxDelivered(participant, v.([]agentcomm.MessageID))
		}
	}
}

// markInboxDelivered records the handover of ids to participant. A failed journal write is storage-fatal and
// ends the run (handleTaskOperationErr): the messages are already in the agent's prompt, and leaving them
// accepted would hand them over again on another turn. A rejection (the run is finalizing) changes nothing.
func (o *Orchestrator) markInboxDelivered(participant agentcomm.ParticipantID, ids []agentcomm.MessageID) {
	for _, id := range ids {
		if err := o.MarkMessageDelivered(context.Background(), id, participant); err != nil {
			o.handleTaskOperationErr(err)
			return
		}
	}
}

// agentCommInboxForTask is agentCommInboxFor's helper-task-scoped wrapper, used by buildTaskContext (T4.3).
func (o *Orchestrator) agentCommInboxForTask(taskID assistants.TaskID) (block string, markDelivered func()) {
	return o.agentCommInboxFor(o.taskMeshParticipant(taskID))
}
