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
	// meshRunPrefix is meshRunParticipantID's own prefix (agentcomm_attach.go) — named here, next to its
	// stage/helper siblings, so taskIDFromMeshParticipant's reverse lookup (agentcomm_resume.go) can recognize
	// it without a second, drifting copy of the literal.
	meshRunPrefix = "run:"

	// actorKindUser is state.Actor.Kind's value for the human operator — the only actor kind that may send an
	// AttachAndSend first message or register a mesh Wait today. The Side agent's own mesh identity
	// (agentcomm_sideagent.go, T6.7) is registered separately from this — every attach_assistant draft it
	// proposes is still CONFIRMED by a human dashboard click, so AttachAndSend's own first message is, and
	// stays, human-authored; the Side agent addresses and is addressed through Send/OpenThread instead, like
	// any other participant.
	actorKindUser = "user"
)

func meshStageParticipantID(stageID string) agentcomm.ParticipantID {
	return agentcomm.ParticipantID(meshStagePrefix + stageID)
}

func meshHelperParticipantID(taskID assistants.TaskID) agentcomm.ParticipantID {
	return agentcomm.ParticipantID(meshHelperPrefix + string(taskID))
}

// meshParticipantForTask resolves task's OWN mesh participant identity: meshRunParticipantID(profile) for a
// scope=run standing role (AttachAndSend/AddRunAgent, T6.2/T6.2b) — whose real mesh identity is keyed by
// profile, not by task id, see meshRunParticipantID's own doc comment — meshHelperParticipantID(task.ID) for
// every other (stage-rooted) task. Every call site that means "this task, as a mesh participant" (not some
// OTHER, specifically-named task) must go through this, not meshHelperParticipantID directly: Agent Mesh
// Phase 8's own QA/DevOps acceptance test (T8.1) is what caught several call sites still assuming the
// ScopeStage shape unconditionally — a scope=run task's own Wait/resume checks, inbox and prompt
// self-description, and its real subprocess's MeshClient identity were all silently resolving to the WRONG
// participant (a "helper:<taskID>" nobody ever registered a wait or sent a message under, instead of its
// actual "run:<profileID>"), which made a run-scoped role's own cooperative yield indistinguishable from a
// protocol error.
func meshParticipantForTask(task assistants.Task) agentcomm.ParticipantID {
	if task.Scope == assistants.ScopeRun {
		return meshRunParticipantID(task.ProfileID)
	}
	return meshHelperParticipantID(task.ID)
}

// taskMeshParticipant is meshParticipantForTask's convenience wrapper for a caller that only has the bare
// task id in hand (e.g. execHelperAgent, constrained to the helperAgentRunner seam's string-only signature) —
// a fresh snapshot lookup, falling back to the ScopeStage default for an id with no task record (never
// expected in practice; keeps this total rather than requiring every such caller to handle a lookup miss).
func (o *Orchestrator) taskMeshParticipant(taskID assistants.TaskID) agentcomm.ParticipantID {
	if t, ok := o.opts.Store.Snapshot().Assistants.Tasks[taskID]; ok {
		return meshParticipantForTask(t.Task)
	}
	return meshHelperParticipantID(taskID)
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
		t := rs.Assistants.Tasks[id]
		if t.Scope == assistants.ScopeRun {
			// A scope=run task's real mesh identity is "run:<profileID>", already synthesized below from
			// Mesh.Participants — listing it again here as "helper:<taskID>" would be a second, bogus roster
			// entry for the same standing role (meshParticipantForTask's own doc comment).
			continue
		}
		out = append(out, helperParticipant(id, t))
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

// ensureParticipantEffects returns one mesh_participant_added Effect for every stage/helper/user id in ids —
// the lazy registration OpenThread needs before it can name a stage agent or helper as a thread member, since
// Phase 1's MeshState never auto-populates those (see this file's package doc comment). Run-scoped ids
// (run:.../side:...) are NEVER synthesized here: those are only ever created by the attach composite (Phase
// 6, T6.2/T6.7) which snapshots a real profile — a thread opened against one that does not exist yet is a
// caller error, not something to paper over.
//
// Deliberately NOT gated on "already a known participant of rs": a stage/helper/user member's descriptor
// (Kind/Scope/Alias/DisplayName/StageID) is re-derived from live run state on EVERY call, so a retry of the
// exact same OpenThread operation id always rebuilds the exact same effects list — required for
// ApplyRunOperation's idempotency check, which compares the marshaled effects byte-for-byte
// (pkg/state/operation.go). Naming an already-registered member again in a later, different OpenThread is
// equally harmless: applyMeshParticipantAdded (pkg/state/mesh_projection.go) treats a byte-identical re-add
// as a no-op.
func ensureParticipantEffects(rs state.RunState, ids []agentcomm.ParticipantID) ([]state.Effect, error) {
	seen := map[agentcomm.ParticipantID]bool{}
	var effects []state.Effect
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		p, ok := resolveParticipant(rs, id)
		if !ok {
			return nil, &state.OperationRejectedError{
				Code: assistants.CodeNotFound, Reason: fmt.Sprintf("unknown participant %q — `afm agent list` shows who is in this run", id),
			}
		}
		if p.Kind == agentcomm.KindRunAgent || p.Kind == agentcomm.KindSideAgent {
			// resolveParticipant's default branch only returned ok=true because id is ALREADY in
			// Mesh.Participants (the attach composite is the only thing that creates these) — nothing to add.
			continue
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
