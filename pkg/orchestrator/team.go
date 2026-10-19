package orchestrator

import (
	"context"
	"fmt"
	"log"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// teamOperationID names the AddRunAgent operation that creates a team member's agent. It depends only on
// the declaration id, so a resumed run finds the agent it already created instead of adding a second one.
func teamOperationID(memberID string) string {
	return "team/" + memberID
}

// initTeam adds the flow's team: agents, in declaration order, before any stage starts. A member whose
// operation already committed (a resumed run) keeps its saved agent; only missing ones go through
// AddRunAgent — the same path as the dashboard's "Add run agent". The agents get no first message: each
// waits, idle, until somebody writes to it.
//
// Best effort, like stage assistants: an agent that cannot be added (an unavailable profile, a finished run)
// is reported and skipped without stopping the run; only a journal-write failure ends it.
func (o *Orchestrator) initTeam(ctx context.Context) {
	if len(o.opts.Team) == 0 || !o.opts.AssistantsEnabled {
		return
	}
	for _, m := range o.opts.Team {
		opID := teamOperationID(m.ID)
		op, committed, err := o.opts.Store.CommittedOperation(opID, opKindAttachAndSend)
		if err != nil {
			o.teamWarning(m.ID, fmt.Sprintf("cannot read the saved agent: %v", err))
			continue
		}
		if committed {
			o.warnIfTeamMemberChanged(op, m)
			continue
		}
		_, err = o.AddRunAgent(ctx, AddRunAgentRequest{
			OperationID: opID,
			ProfileID:   assistants.ProfileID(m.Profile),
			Title:       m.Title,
			Instruction: m.Instruction,
			Actor:       state.Actor{Kind: "system", ID: "flow"},
		})
		if err != nil {
			o.handleTaskOperationErr(err)
			o.teamWarning(m.ID, fmt.Sprintf("agent not added: %v", err))
		}
	}
}

// warnIfTeamMemberChanged compares a saved team agent with its declaration in the current flow.yaml. A
// difference is not a conflict: the run keeps the saved agent, and the new declaration applies to a new run.
func (o *Orchestrator) warnIfTeamMemberChanged(op state.CommittedOperation, m flow.TeamMember) {
	var saved state.TaskCreatedPayload
	if _, found, err := op.Effect("task_created", &saved); err != nil || !found {
		o.teamWarning(m.ID, "cannot read the saved agent")
		return
	}
	if saved.ProfileID == m.Profile && o.sameRedactedText(saved.Title, m.Title) && o.sameRedactedText(saved.Instruction, m.Instruction) {
		return
	}
	o.teamWarning(m.ID, "changed in flow.yaml; using the saved version, the new one applies to a new run")
}

// teamWarning reports a team: problem in the log and as a durable run-level notice.
func (o *Orchestrator) teamWarning(memberID, message string) {
	log.Printf("team member %q: %s", memberID, message)
	o.publishNotice(bus.Event{Type: bus.EventTeamWarning, Data: map[string]any{"member_id": memberID, "message": message}})
}
