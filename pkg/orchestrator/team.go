package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// teamAliasAttempts bounds how often a member is added again after a concurrent attach took its @name.
const teamAliasAttempts = 3

// errTeamAliasContention: every attempt to add a member lost its @name to a concurrent attach.
var errTeamAliasContention = errors.New("its @name was taken by a concurrent attach on every attempt")

// teamOperationPrefix is the request-id namespace of the flow's team (teamOperationID). It is reserved:
// only teamActor attaches under it (AttachAndSend), so a dashboard or API request cannot stand in for a member.
const teamOperationPrefix = "team/"

// teamActor is who adds the flow's team agents.
var teamActor = state.Actor{Kind: "system", ID: "flow"}

// errTeamIDTaken: the member's request id was committed by something other than the flow's team.
var errTeamIDTaken = errors.New("its request id was used by a request that is not this flow's team")

// teamOperationID names the AddRunAgent operation that creates a team member's agent. It depends only on
// the declaration id, so a resumed run finds the agent it already created instead of adding a second one.
func teamOperationID(memberID string) string {
	return teamOperationPrefix + memberID
}

// rejectReservedRequestID keeps caller-supplied IDs out of the flow-team and
// side Mesh terminal operation namespaces shared by the run's operation index.
func rejectReservedRequestID(opID string, actor state.Actor) error {
	if actor.Kind != "system" {
		for _, prefix := range []string{"side-mesh-reply:", "side-mesh-unprocessed:", loopTickOperationPrefix} {
			if strings.HasPrefix(opID, prefix) {
				return attachRejected(assistants.CodeInvalidRequest, "request ids starting with %q are reserved", prefix)
			}
		}
	}
	if strings.HasPrefix(opID, teamOperationPrefix) && actor != teamActor {
		return attachRejected(assistants.CodeInvalidRequest, "request ids starting with %q are reserved for the flow's team", teamOperationPrefix)
	}
	return nil
}

// savedTeamAgent reads the committed operation that created a member's agent, if any. An operation under the
// member's id that the flow's team did not create (one journaled before the namespace was reserved) is not
// the member's agent: errTeamIDTaken.
func savedTeamAgent(store *state.Store, memberID string) (state.CommittedOperation, bool, error) {
	op, saved, err := store.CommittedOperation(teamOperationID(memberID), opKindAttachAndSend)
	if err != nil || !saved {
		return op, saved, err
	}
	var created state.TaskCreatedPayload
	if _, found, err := op.Effect("task_created", &created); err != nil || !found ||
		created.CreatedBy != teamActor.Kind || created.Scope != string(assistants.ScopeRun) {
		return op, true, errTeamIDTaken
	}
	return op, true, nil
}

// TeamMemberSaved reports whether the run in rs already created the agent of member memberID. The host reads
// it from a journal replay, without the writer lock, to choose Docker mounts before the run starts.
func TeamMemberSaved(rs state.RunState, memberID string) bool {
	_, saved := rs.Assistants.OperationIndex[teamOperationID(memberID)]
	return saved
}

// TeamMembersToCreate returns the members whose agent the run has not created yet — the only ones whose
// profile must exist now; a saved agent keeps the profile snapshot it was created with. A member whose id
// was taken by another kind of operation is left out (initTeam reports it). A journal read failure is an
// error.
func TeamMembersToCreate(store *state.Store, team flow.Team) (flow.Team, error) {
	var out flow.Team
	for _, m := range team {
		_, saved, err := savedTeamAgent(store, m.ID)
		if errors.Is(err, state.ErrOperationIDConflict) || errors.Is(err, errTeamIDTaken) {
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
// assistants. A journal that cannot be read or written, or a member that keeps losing its @name to concurrent
// attaches, is returned: the run must not start without its team because of either.
func (o *Orchestrator) initTeam(ctx context.Context) error {
	if len(o.opts.Team) == 0 || !o.opts.AssistantsEnabled {
		return nil
	}
	for _, m := range o.opts.Team {
		op, saved, err := savedTeamAgent(o.opts.Store, m.ID)
		switch {
		case errors.Is(err, state.ErrOperationIDConflict) || errors.Is(err, errTeamIDTaken):
			o.teamWarning(m.ID, fmt.Sprintf("not added: %v", err))
		case err != nil:
			return fmt.Errorf("team member %q: %w", m.ID, err)
		case saved:
			o.warnIfTeamMemberChanged(op, m)
		default:
			id, err := o.addTeamAgent(ctx, m)
			if err != nil {
				var se *state.StorageError
				if errors.As(err, &se) || errors.Is(err, errTeamAliasContention) {
					return fmt.Errorf("team member %q: %w", m.ID, err)
				}
				o.teamWarning(m.ID, fmt.Sprintf("agent not added: %v", err))
				continue
			}
			o.warnIfTeamMemberLostAssistant(m, id)
		}
	}
	return nil
}

// addTeamAgent creates a member's agent. The dashboard is already up, so an "Add run agent" can take the
// member's @name between its read of the roster and its commit; that rejection is retried with a fresh
// roster, which gives the member the next free name. Losing every attempt is errTeamAliasContention.
// It returns the member's participant id.
func (o *Orchestrator) addTeamAgent(ctx context.Context, m flow.TeamMember) (agentcomm.ParticipantID, error) {
	for range teamAliasAttempts {
		res, err := o.AddRunAgent(ctx, AddRunAgentRequest{
			OperationID: teamOperationID(m.ID),
			ProfileID:   assistants.ProfileID(m.Profile),
			Title:       m.Title,
			Instruction: m.Instruction,
			Skills:      m.Skills,
			SkillsDir:   string(m.SkillsDir),
			Actor:       teamActor,
		})
		var rejected *state.OperationRejectedError
		if !errors.As(err, &rejected) || rejected.Code != assistants.CodeStaleRevision {
			return res.ParticipantID, err
		}
	}
	return "", fmt.Errorf("%w (%d attempts)", errTeamAliasContention, teamAliasAttempts)
}

// warnIfTeamMemberLostAssistant tells the CLI that a member just added to the run asked for @assistant but
// the bound Assistant, saved under that name, already owns it — so the member got the next free @assistant-N.
// Only a fresh add is reported; a resumed run neither warns again nor renames the member it saved.
func (o *Orchestrator) warnIfTeamMemberLostAssistant(m flow.TeamMember, id agentcomm.ParticipantID) {
	rs := o.opts.Store.Snapshot()
	if side := rs.Assistants.Mesh.Participants[o.SideAgentParticipantID()]; side == nil || !strings.EqualFold(side.Alias, sideAgentParticipantAlias) {
		return
	}
	asked := runAgentAlias(m.Title, assistants.ProfileID(m.Profile))
	if !strings.EqualFold(asked, sideAgentParticipantAlias) {
		return
	}
	if now := effectiveAlias(rs, id); now != asked {
		o.writeAssistantAliasWarning(teamMemberHolder(m.ID, id), now)
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
	if saved.ProfileID == m.Profile && o.sameRedactedText(saved.Title, m.Title) && o.sameRedactedText(saved.Instruction, m.Instruction) &&
		slices.Equal(saved.Skills, m.Skills) && saved.SkillsDir == string(m.SkillsDir) {
		return
	}
	o.teamWarning(m.ID, "changed in flow.yaml or config; using the saved version, the new one applies to a new run")
}

// teamWarning reports a team: problem in the log and as a durable run-level notice.
func (o *Orchestrator) teamWarning(memberID, message string) {
	log.Printf("team member %q: %s", memberID, message)
	o.publishNotice(bus.Event{Type: bus.EventTeamWarning, Data: map[string]any{"member_id": memberID, "message": message}})
}
