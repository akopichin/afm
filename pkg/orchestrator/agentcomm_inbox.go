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

// agentCommInboxFor renders participant's pending (not-yet-delivered) inbox messages and returns a closure
// that marks exactly those messages delivered. The same set is remembered as the participant's rendered inbox
// for deliverRenderedInbox — executor.Config.OnDelivered, called only after cmd.Start() succeeds
// (pkg/executor/executor.go). Marking never happens at prompt-assembly time, so a crash or a failed start
// between building this prompt and spawning the process leaves the messages at "accepted" and they are
// re-injected, unchanged, on the next attempt.
func (o *Orchestrator) agentCommInboxFor(participant agentcomm.ParticipantID) (block string, markDelivered func()) {
	if o.opts.Store == nil {
		// Some tests build a bare Orchestrator literal to exercise a single concern (e.g. browser/mesh Config
		// wiring) without a real Store — nothing to read, so there is nothing pending and nothing to deliver.
		return "", func() {}
	}
	rs := o.opts.Store.Snapshot()
	ids := rs.Assistants.Mesh.Inbox[participant]

	var pending []agentcomm.MessageID
	var msgs []prompts.AgentCommInboxMessage
	for _, id := range ids {
		if rs.Assistants.Mesh.Receipts[id][participant] != agentcomm.DeliveryAccepted {
			continue
		}
		m := rs.Assistants.Mesh.Messages[id]
		if m == nil {
			continue
		}
		pending = append(pending, id)
		var inReplyTo string
		if m.InReplyTo != nil {
			inReplyTo = string(*m.InReplyTo)
		}
		recipients := make([]string, len(m.Recipients))
		for i, r := range m.Recipients {
			recipients[i] = string(r)
		}
		msgs = append(msgs, prompts.AgentCommInboxMessage{
			MessageID: string(m.ID), ThreadID: string(m.ThreadID), From: string(m.Sender),
			Recipients: recipients, InReplyTo: inReplyTo, Body: m.Body,
		})
	}

	o.renderedInbox.Store(participant, pending)
	return prompts.AgentCommInboxBlock(msgs), func() { o.markInboxDelivered(participant, pending) }
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

func (o *Orchestrator) markInboxDelivered(participant agentcomm.ParticipantID, ids []agentcomm.MessageID) {
	for _, id := range ids {
		_ = o.MarkMessageDelivered(context.Background(), id, participant)
	}
}

// agentCommInboxForTask is agentCommInboxFor's helper-task-scoped wrapper, used by buildTaskContext (T4.3).
func (o *Orchestrator) agentCommInboxForTask(taskID assistants.TaskID) (block string, markDelivered func()) {
	return o.agentCommInboxFor(o.taskMeshParticipant(taskID))
}
