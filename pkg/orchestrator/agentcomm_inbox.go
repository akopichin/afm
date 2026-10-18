package orchestrator

import (
	"context"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/prompts"
)

// Agent Mesh Phase 4, T4.4: the <inbox> block and its delivery-marking callback. Both halves are derived
// from the SAME read of CURRENT state (only messages still at DeliveryAccepted for this participant), so the
// prompt text and the set of messages OnDelivered marks delivered always agree.

// agentCommInboxFor renders participant's pending (not-yet-delivered) inbox messages and returns a closure
// that marks exactly those messages delivered. The closure is meant for executor.Config.OnDelivered, called
// ONLY after cmd.Start() actually succeeds (pkg/executor/executor.go) — never at context-assembly time,
// so a crash between building this prompt and spawning the process leaves the messages at "accepted" and
// they are re-injected, unchanged, on the next attempt.
func (o *Orchestrator) agentCommInboxFor(participant agentcomm.ParticipantID) (block string, onDelivered func()) {
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

	return prompts.AgentCommInboxBlock(msgs), func() {
		for _, id := range pending {
			_ = o.MarkMessageDelivered(context.Background(), id, participant)
		}
	}
}

// agentCommInboxForTask is agentCommInboxFor's helper-task-scoped wrapper, used by buildTaskContext (T4.3)
// and helperExecutorConfig's OnDelivered wiring.
func (o *Orchestrator) agentCommInboxForTask(taskID assistants.TaskID) (block string, onDelivered func()) {
	return o.agentCommInboxFor(meshHelperParticipantID(taskID))
}
