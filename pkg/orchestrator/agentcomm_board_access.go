package orchestrator

import (
	"context"
	"maps"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// boardAvailable: assistants mode removes the Board for every source (dashboard, agents, CLI), so the
// check lives in the orchestrator rather than in the UI. The zero mode means team.
func (o *Orchestrator) boardAvailable() bool {
	return o.opts.CollaborationMode != assistants.CollaborationModeAssistants
}

// errBoardUnavailable is the rejection for any Board operation in assistants mode.
func errBoardUnavailable() error {
	return &state.OperationRejectedError{
		Code:   assistants.CodeFeatureDisabled,
		Reason: "Board is unavailable in assistants mode",
	}
}

// hiddenBoardNotices returns the Board notices no agent reads without the Board (nil while it is available): they
// would hand the agent Board commands it cannot run. A notice commits in its operation's own record, under the id
// boardNoticeMessageID derives from that operation and its recipient.
func (o *Orchestrator) hiddenBoardNotices(rs state.RunState) map[agentcomm.MessageID]bool {
	if o.boardAvailable() {
		return nil
	}
	opAt := make(map[uint64]string, len(rs.Assistants.OperationIndex))
	for op, seq := range rs.Assistants.OperationIndex {
		opAt[seq] = op
	}
	hidden := map[agentcomm.MessageID]bool{}
	for id, m := range rs.Assistants.Mesh.Messages {
		if op, ok := opAt[m.Seq]; ok && len(m.Recipients) == 1 && id == boardNoticeMessageID(op, m.Recipients[0]) {
			hidden[id] = true
		}
	}
	return hidden
}

// agentMesh is the mesh an agent reads: without the hidden Board notices. Only for reading and rendering — the
// projection's wake and wait rules expect every inbox id to have its message.
func (o *Orchestrator) agentMesh(rs state.RunState) state.MeshState {
	return meshWithout(rs.Assistants.Mesh, o.hiddenBoardNotices(rs))
}

// meshWithout returns mesh without the messages in ids.
func meshWithout(mesh state.MeshState, ids map[agentcomm.MessageID]bool) state.MeshState {
	if len(ids) == 0 {
		return mesh
	}
	mesh.Messages = maps.Clone(mesh.Messages)
	for id := range ids {
		delete(mesh.Messages, id)
	}
	return mesh
}

// HidesFromAgents reports whether message id is left out of what agents read, for a reader outside the orchestrator
// (the Assistant's own history).
func (o *Orchestrator) HidesFromAgents(id agentcomm.MessageID) (hidden bool) {
	if o.boardAvailable() || o.opts.Store == nil {
		return false
	}
	o.opts.Store.View(func(rs *state.RunState) { hidden = o.hiddenBoardNotices(*rs)[id] })
	return hidden
}

// retireHiddenBoardNotices marks the hidden notices still unread as handed over, so they never wake an agent or
// satisfy its wait. Once per run start is enough: no new notice commits without the Board.
func (o *Orchestrator) retireHiddenBoardNotices(ctx context.Context) error {
	rs := o.opts.Store.Snapshot()
	for id := range o.hiddenBoardNotices(rs) {
		for recipient, receipt := range rs.Assistants.Mesh.Receipts[id] {
			if receipt != agentcomm.DeliveryAccepted {
				continue
			}
			if err := o.MarkMessageDelivered(ctx, id, recipient); err != nil {
				return err
			}
		}
	}
	return nil
}
