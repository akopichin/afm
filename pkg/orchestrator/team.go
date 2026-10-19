package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// teamAliasAttempts bounds how often a member is added again after a concurrent attach took its @name.
const teamAliasAttempts = 3

// teamOperationID names the AddRunAgent operation that creates a team member's agent. It depends only on
// the declaration id, so a resumed run finds the agent it already created instead of adding a second one.
func teamOperationID(memberID string) string {
	return "team/" + memberID
}

// savedTeamAgent reads the committed operation that created a member's agent, if any.
func savedTeamAgent(store *state.Store, memberID string) (state.CommittedOperation, bool, error) {
	return store.CommittedOperation(teamOperationID(memberID), opKindAttachAndSend)
}

// TeamMembersToCreate returns the members whose agent the run has not created yet — the only ones whose
// profile must exist now; a saved agent keeps the profile snapshot it was created with. A member whose id
// was taken by another kind of operation is left out (initTeam reports it). A journal read failure is an
// error.
func TeamMembersToCreate(store *state.Store, team flow.Team) (flow.Team, error) {
	var out flow.Team
	for _, m := range team {
		_, saved, err := savedTeamAgent(store, m.ID)
		if errors.Is(err, state.ErrOperationIDConflict) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("team member %q: %w", m.ID, err)
		}
		if !saved {
			out = append(out, m)
		}
	}
	return out, nil
}

// initTeam adds the flow's team: agents, in declaration order, before any stage starts. A member whose
// operation already committed (a resumed run) keeps its saved agent; only missing ones go through
// AddRunAgent — the same path as the dashboard's "Add run agent". The agents get no first message: each
// waits, idle, until somebody writes to it.
//
// An agent that cannot be added (an unavailable profile, a finished run) is reported and skipped, like stage
// assistants. A journal that cannot be read or written is returned: the run must not start without it.
func (o *Orchestrator) initTeam(ctx context.Context) error {
	if len(o.opts.Team) == 0 || !o.opts.AssistantsEnabled {
		return nil
	}
	for _, m := range o.opts.Team {
		op, saved, err := savedTeamAgent(o.opts.Store, m.ID)
		switch {
		case errors.Is(err, state.ErrOperationIDConflict):
			o.teamWarning(m.ID, fmt.Sprintf("not added: %v", err))
		case err != nil:
			return fmt.Errorf("team member %q: %w", m.ID, err)
		case saved:
			o.warnIfTeamMemberChanged(op, m)
		default:
			if err := o.addTeamAgent(ctx, m); err != nil {
				var se *state.StorageError
				if errors.As(err, &se) {
					return fmt.Errorf("team member %q: %w", m.ID, err)
				}
				o.teamWarning(m.ID, fmt.Sprintf("agent not added: %v", err))
			}
		}
	}
	return nil
}

// addTeamAgent creates a member's agent. The dashboard is already up, so an "Add run agent" can take the
// member's @name between its read of the roster and its commit; that rejection is retried with a fresh
// roster, which gives the member the next free name.
func (o *Orchestrator) addTeamAgent(ctx context.Context, m flow.TeamMember) error {
	var err error
	for range teamAliasAttempts {
		_, err = o.AddRunAgent(ctx, AddRunAgentRequest{
			OperationID: teamOperationID(m.ID),
			ProfileID:   assistants.ProfileID(m.Profile),
			Title:       m.Title,
			Instruction: m.Instruction,
			Actor:       state.Actor{Kind: "system", ID: "flow"},
		})
		var rejected *state.OperationRejectedError
		if !errors.As(err, &rejected) || rejected.Code != assistants.CodeStaleRevision {
			return err
		}
	}
	return err
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
