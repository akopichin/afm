package orchestrator

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 2, T2.1: the roster. Stage agents and helper tasks are NEVER persisted as
// mesh_participant_added records of their own — Phase 1's MeshState only ever stores participants an
// operation explicitly registered (today: none; Phase 6 adds run-scoped roles and the Side agent). Instead
// ListParticipants synthesizes a stage_agent entry for every stage in the live registry and a helper entry
// for every task in the live task projection, EVERY call — "derived from the durable projection, never from
// in-memory caches" (dev-plan.md T2.1). Only run-scoped participants (run_agent, side_agent) come from
// MeshState itself, since nothing else knows about them.

const (
	meshUserParticipantID = agentcomm.ParticipantID("user")
	meshStagePrefix       = "stage:"
	meshHelperPrefix      = "helper:"
)

func meshStageParticipantID(stageID string) agentcomm.ParticipantID {
	return agentcomm.ParticipantID(meshStagePrefix + stageID)
}

func meshHelperParticipantID(taskID assistants.TaskID) agentcomm.ParticipantID {
	return agentcomm.ParticipantID(meshHelperPrefix + string(taskID))
}

// ListParticipants implements Service.ListParticipants (T2.1).
func (o *Orchestrator) ListParticipants(ctx context.Context, req ListParticipantsRequest) (ParticipantPage, error) {
	rs := o.opts.Store.Snapshot()
	return ParticipantPage{Participants: o.liveRoster(rs)}, nil
}

// liveRoster assembles the full roster from the registry (stages), the task projection (helpers) and
// MeshState (run-scoped roles), in that fixed order, each group sorted by id for a deterministic result.
func (o *Orchestrator) liveRoster(rs state.RunState) []agentcomm.Participant {
	var out []agentcomm.Participant
	out = append(out, agentcomm.Participant{
		ID: meshUserParticipantID, Kind: agentcomm.KindUser, Scope: agentcomm.ScopeRun,
		Alias: "user", DisplayName: "You", Reachable: true,
	})

	stages := o.reg().Stages()
	stageIDs := make([]string, 0, len(stages))
	stageByID := make(map[string]string, len(stages)) // id -> name
	for _, s := range stages {
		stageIDs = append(stageIDs, s.ID)
		stageByID[s.ID] = s.Name
	}
	slices.Sort(stageIDs)
	for _, id := range stageIDs {
		out = append(out, stageParticipant(rs, id, stageByID[id]))
	}

	taskIDs := make([]string, 0, len(rs.Assistants.Tasks))
	for id := range rs.Assistants.Tasks {
		taskIDs = append(taskIDs, string(id))
	}
	slices.Sort(taskIDs)
	for _, idStr := range taskIDs {
		id := assistants.TaskID(idStr)
		out = append(out, helperParticipant(id, rs.Assistants.Tasks[id]))
	}

	runIDs := make([]string, 0, len(rs.Assistants.Mesh.Participants))
	for id, p := range rs.Assistants.Mesh.Participants {
		if p.Scope == agentcomm.ScopeRun {
			runIDs = append(runIDs, string(id))
		}
	}
	slices.Sort(runIDs)
	for _, idStr := range runIDs {
		out = append(out, *rs.Assistants.Mesh.Participants[agentcomm.ParticipantID(idStr)])
	}

	return out
}

// stageParticipant synthesizes stageID's live roster entry. A stage finishing removes nothing from history
// — it stays in the roster with Reachable=false and a reason, exactly like a terminal helper task.
func stageParticipant(rs state.RunState, stageID, name string) agentcomm.Participant {
	status, known := rs.Stages[stageID]
	reachable := known && !bus.IsTerminal(status.Status)
	p := agentcomm.Participant{
		ID: meshStageParticipantID(stageID), Kind: agentcomm.KindStageAgent, Scope: agentcomm.ScopeStage,
		Alias: stageID, DisplayName: name, StageID: stageID, Reachable: reachable,
	}
	if !reachable {
		p.UnreachableReason = fmt.Sprintf("stage %s has reached a terminal status", stageID)
	}
	return p
}

// helperParticipant synthesizes taskID's live roster entry from the task projection.
func helperParticipant(id assistants.TaskID, t *state.TaskState) agentcomm.Participant {
	reachable := !t.Status.Terminal()
	p := agentcomm.Participant{
		ID: meshHelperParticipantID(id), Kind: agentcomm.KindHelper, Scope: agentcomm.ScopeStage,
		Alias: string(id), DisplayName: t.Title, StageID: t.RootStageID, Reachable: reachable,
	}
	if !reachable {
		p.UnreachableReason = fmt.Sprintf("task %s has reached a terminal status", id)
	}
	return p
}

// resolveParticipant looks up id in the live roster — the same assembly ListParticipants uses — without
// building the whole page, so a single-id check (Send/OpenThread recipient validation) does not pay for the
// entire roster scan's allocations twice.
func resolveParticipant(rs state.RunState, id agentcomm.ParticipantID) (agentcomm.Participant, bool) {
	switch {
	case id == meshUserParticipantID:
		return agentcomm.Participant{ID: meshUserParticipantID, Kind: agentcomm.KindUser, Scope: agentcomm.ScopeRun, Alias: "user", DisplayName: "You", Reachable: true}, true
	case strings.HasPrefix(string(id), meshStagePrefix):
		stageID := strings.TrimPrefix(string(id), meshStagePrefix)
		if _, known := rs.Stages[stageID]; !known {
			return agentcomm.Participant{}, false
		}
		return stageParticipant(rs, stageID, rs.StageNames[stageID]), true
	case strings.HasPrefix(string(id), meshHelperPrefix):
		taskID := assistants.TaskID(strings.TrimPrefix(string(id), meshHelperPrefix))
		t, ok := rs.Assistants.Tasks[taskID]
		if !ok {
			return agentcomm.Participant{}, false
		}
		return helperParticipant(taskID, t), true
	default:
		p, ok := rs.Assistants.Mesh.Participants[id]
		if !ok {
			return agentcomm.Participant{}, false
		}
		return *p, true
	}
}

// ensureParticipantEffects returns one mesh_participant_added Effect for every id in ids that is not
// already a known participant of rs — the lazy registration OpenThread needs before it can name a stage
// agent or helper as a thread member, since Phase 1's MeshState never auto-populates those (see this file's
// package doc comment). Run-scoped ids (run:.../side:...) are NEVER synthesized here: those are only ever
// created by the attach composite (Phase 6, T6.2/T6.7) which snapshots a real profile — a thread opened
// against one that does not exist yet is a caller error, not something to paper over.
func ensureParticipantEffects(rs state.RunState, ids []agentcomm.ParticipantID) ([]state.Effect, error) {
	seen := map[agentcomm.ParticipantID]bool{}
	var effects []state.Effect
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, known := rs.Assistants.Mesh.Participants[id]; known {
			continue
		}
		p, ok := resolveParticipant(rs, id)
		if !ok {
			return nil, fmt.Errorf("agentcomm: unknown participant %q", id)
		}
		if p.Kind == agentcomm.KindRunAgent || p.Kind == agentcomm.KindSideAgent {
			// Never reached today (resolveParticipant's default branch already requires these to be in
			// Mesh.Participants, i.e. "known" above), kept as an explicit guard against a future
			// resolveParticipant change silently starting to synthesize them.
			return nil, fmt.Errorf("agentcomm: run-scoped participant %q must be attached before it can be addressed", id)
		}
		eff, err := meshParticipantAddedEffect(p)
		if err != nil {
			return nil, err
		}
		effects = append(effects, eff)
	}
	return effects, nil
}

func meshParticipantAddedEffect(p agentcomm.Participant) (state.Effect, error) {
	payload := state.MeshParticipantAddedPayload{
		Kind: string(p.Kind), Scope: string(p.Scope), Alias: p.Alias, DisplayName: p.DisplayName, StageID: p.StageID,
	}
	return marshalEffect("mesh_participant_added", state.Target{ParticipantID: string(p.ID)}, payload)
}
