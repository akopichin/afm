package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/memory"
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
	block, _, markDelivered = o.renderInbox(participant, nil)
	return block, markDelivered
}

// renderInbox is agentCommInboxFor that also re-renders unfinished: the messages an earlier turn of the same
// agent was handed but never finished (see turnInboxFileName). They are already delivered, yet that turn's
// input would otherwise be lost to its retry. It returns the ids it rendered, in inbox order.
func (o *Orchestrator) renderInbox(participant agentcomm.ParticipantID, unfinished []agentcomm.MessageID) (block string, rendered []agentcomm.MessageID, markDelivered func()) {
	if o.opts.Store == nil {
		// Some tests build a bare Orchestrator literal to exercise a single concern (e.g. browser/mesh Config
		// wiring) without a real Store — nothing to read, so there is nothing pending and nothing to deliver.
		return "", nil, func() {}
	}
	rs := o.opts.Store.Snapshot()

	var msgs []prompts.AgentCommInboxMessage
	size, more := 0, 0
	for _, id := range rs.Assistants.Mesh.Inbox[participant] {
		m := rs.Assistants.Mesh.Messages[id]
		if m == nil {
			continue
		}
		if rs.Assistants.Mesh.Receipts[id][participant] != agentcomm.DeliveryAccepted && !slices.Contains(unfinished, id) {
			continue
		}
		size += len(m.Body) + inboxMessageOverhead
		if len(msgs) > 0 && size > inboxBlockByteBudget {
			more++
			continue
		}
		rendered = append(rendered, id)
		msgs = append(msgs, inboxMessageView(m))
	}

	o.renderedInbox.Store(participant, rendered)
	return prompts.AgentCommInboxBlock(msgs, more), rendered, func() { o.markInboxDelivered(participant, rendered) }
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

// agentCommInboxForTask is renderInbox's helper-task-scoped wrapper, used by buildTaskContext (T4.3): the
// task's unfinished turn input (if any) is rendered again together with its pending messages.
func (o *Orchestrator) agentCommInboxForTask(taskID assistants.TaskID) (block string, rendered []agentcomm.MessageID, markDelivered func()) {
	return o.renderInbox(o.taskMeshParticipant(taskID), o.loadTurnInbox(taskID))
}

// turnInboxFileName holds the ids of the <inbox> messages handed to a task turn that has not finished yet.
// The executor marks them delivered when the process starts; if the turn is then cut short (run shutdown,
// Pause), its retry reads them back here, so the input that started the turn is not lost. The file is
// removed once a turn's process ran to its end.
const turnInboxFileName = "turn-inbox.json"

// saveTurnInbox records ids as taskID's unfinished turn input (or removes the record when there are none).
func (o *Orchestrator) saveTurnInbox(taskID assistants.TaskID, ids []agentcomm.MessageID) error {
	if len(ids) == 0 {
		return o.clearTurnInbox(taskID)
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return memory.AtomicWrite(filepath.Join(o.taskDir(taskID), turnInboxFileName), raw)
}

// clearTurnInbox forgets taskID's unfinished turn input: its turn ran to the end.
func (o *Orchestrator) clearTurnInbox(taskID assistants.TaskID) error {
	err := os.Remove(filepath.Join(o.taskDir(taskID), turnInboxFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// loadTurnInbox returns taskID's unfinished turn input; a missing or unreadable record means none.
func (o *Orchestrator) loadTurnInbox(taskID assistants.TaskID) []agentcomm.MessageID {
	raw, err := os.ReadFile(filepath.Join(o.taskDir(taskID), turnInboxFileName))
	if err != nil {
		return nil
	}
	var ids []agentcomm.MessageID
	if json.Unmarshal(raw, &ids) != nil {
		return nil
	}
	return ids
}
