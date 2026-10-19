package orchestrator

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
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

// meshParticipantForTask resolves task's OWN mesh participant identity: meshRunParticipantID(task.ID) for a
// scope=run role, meshHelperParticipantID(task.ID) for every other (stage-rooted) task. Every call site that
// means "this task, as a mesh participant" must go through this, not meshHelperParticipantID directly: a
// scope=run task's own Wait/resume checks, inbox and prompt self-description, and its subprocess's MeshClient
// identity must all resolve to its "run:" id (Agent Mesh T8.1 caught call sites assuming the stage shape).
// A run task from a journal written before run agents were keyed by task keeps its legacy "run:<profileID>"
// identity (runTaskForParticipant decides which task owns it), so its inbox and waits survive a restart.
func meshParticipantForTask(rs state.RunState, task assistants.Task) agentcomm.ParticipantID {
	if task.Scope != assistants.ScopeRun {
		return meshHelperParticipantID(task.ID)
	}
	own := meshRunParticipantID(task.ID)
	if _, ok := rs.Assistants.Mesh.Participants[own]; ok {
		return own
	}
	legacy := agentcomm.ParticipantID(meshRunPrefix + string(task.ProfileID))
	if owner, ok := runTaskForParticipant(rs, legacy); ok && owner == task.ID {
		return legacy
	}
	return own
}

// runTaskForParticipant maps a registered run participant id to the task behind it: "run:<taskID>" of a
// scope=run task directly (never a stage helper that merely shares the id suffix); a legacy
// "run:<profileID>" (journals from before run agents were keyed by task, when there was one per profile) to
// that profile's run task without a "run:<taskID>" participant of its own — the lowest id if several, so the
// answer is deterministic and never flips to a newer agent of the same profile.
func runTaskForParticipant(rs state.RunState, id agentcomm.ParticipantID) (assistants.TaskID, bool) {
	suffix, ok := strings.CutPrefix(string(id), meshRunPrefix)
	if !ok || suffix == "" {
		return "", false
	}
	if _, registered := rs.Assistants.Mesh.Participants[id]; !registered {
		return "", false
	}
	if t, isTask := rs.Assistants.Tasks[assistants.TaskID(suffix)]; isTask && t.Scope == assistants.ScopeRun {
		return assistants.TaskID(suffix), true
	}
	var owner assistants.TaskID
	for tid, t := range rs.Assistants.Tasks {
		if t.Scope != assistants.ScopeRun || string(t.ProfileID) != suffix {
			continue
		}
		if _, hasOwn := rs.Assistants.Mesh.Participants[meshRunParticipantID(tid)]; hasOwn {
			continue
		}
		if owner == "" || tid < owner {
			owner = tid
		}
	}
	return owner, owner != ""
}

// taskMeshParticipant is meshParticipantForTask's convenience wrapper for a caller that only has the bare
// task id in hand (e.g. execHelperAgent, constrained to the helperAgentRunner seam's string-only signature) —
// a fresh snapshot lookup, falling back to the ScopeStage default for an id with no task record (never
// expected in practice; keeps this total rather than requiring every such caller to handle a lookup miss).
func (o *Orchestrator) taskMeshParticipant(taskID assistants.TaskID) agentcomm.ParticipantID {
	rs := o.opts.Store.Snapshot()
	if t, ok := rs.Assistants.Tasks[taskID]; ok {
		return meshParticipantForTask(rs, t.Task)
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
	stageByID := make(map[string]flow.Stage, len(stages))
	for _, s := range stages {
		stageIDs = append(stageIDs, s.ID)
		stageByID[s.ID] = s
	}
	slices.Sort(stageIDs)
	for _, id := range stageIDs {
		out = append(out, o.withStageConfig(stageParticipant(rs, id, stageByID[id].Name), stageByID[id]))
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
		out = append(out, o.withEffectiveReadonly(helperParticipant(id, t), t))
	}

	runIDs := make([]string, 0, len(rs.Assistants.Mesh.Participants))
	for id, p := range rs.Assistants.Mesh.Participants {
		if p.Scope == agentcomm.ScopeRun {
			runIDs = append(runIDs, string(id))
		}
	}
	slices.Sort(runIDs)
	for _, idStr := range runIDs {
		p := liveRunParticipant(rs, *rs.Assistants.Mesh.Participants[agentcomm.ParticipantID(idStr)])
		if taskID, ok := runTaskForParticipant(rs, p.ID); ok && rs.Assistants.Tasks[taskID] != nil {
			p = o.withEffectiveReadonly(p, rs.Assistants.Tasks[taskID])
		}
		out = append(out, p)
	}

	return out
}

// scriptStageUnreachableReason — a script stage runs a shell script, not an agent: nobody reads its inbox.
const scriptStageUnreachableReason = "script stage: no agent reads messages"

// withStageConfig adds what only the stage's config knows: the command its agent runs, or — for a script
// stage, which has no agent — that it cannot be written to. Only the full roster (liveRoster) applies it:
// resolveParticipant has no registry at hand.
func (o *Orchestrator) withStageConfig(p agentcomm.Participant, s flow.Stage) agentcomm.Participant {
	if s.Script != "" {
		p.Reachable = false
		p.UnreachableReason = scriptStageUnreachableReason
		return p
	}
	p.Command = o.stageCommand(s)
	return p
}

// withEffectiveReadonly sets the read-only guarantee a task-backed participant's badge shows. A live task
// runs its next turn in THIS process's environment, so it gets the guarantee that environment gives its
// command now (a host run resumed in a container loses the OS sandbox); a finished task keeps the guarantee
// it ran under. The admission-time value stays in AdmittedReadonlyEnforcement.
func (o *Orchestrator) withEffectiveReadonly(p agentcomm.Participant, t *state.TaskState) agentcomm.Participant {
	p.ReadonlyEnforcement = effectiveReadonlyEnforcement(t, o.opts.Config)
	return p
}

func effectiveReadonlyEnforcement(t *state.TaskState, cfg config.Config) string {
	if t.Status.Terminal() {
		return string(t.ProfileSnapshot.ReadonlyEnforcement)
	}
	return config.HelperReadonlyEnforcement(t.ProfileSnapshot.Command, cfg)
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
		Activity: taskActivity(t, false),
		Command:  t.ProfileSnapshot.Command, ReadonlyEnforcement: string(t.ProfileSnapshot.ReadonlyEnforcement),
		AdmittedReadonlyEnforcement: string(t.ProfileSnapshot.ReadonlyEnforcement),
	}
	if !reachable {
		p.UnreachableReason = fmt.Sprintf("task %s has reached a terminal status", id)
	}
	return p
}

// taskActivity says what a task's participant is doing now. idle — a queued run agent with nothing to read
// (idleRunAgent); any other queued task is about to start, so it counts as working.
func taskActivity(t *state.TaskState, idle bool) agentcomm.Activity {
	switch {
	case t.Status.Terminal():
		return agentcomm.ActivityDone
	case t.Status == assistants.StatusPaused || len(t.Holds) > 0:
		return agentcomm.ActivityPaused
	case idle:
		return agentcomm.ActivityIdle
	case t.Status == assistants.StatusWaitingPeer, t.Status == assistants.StatusWaitingChildren, t.Status == assistants.StatusWaitingParent:
		return agentcomm.ActivityWaiting
	default:
		return agentcomm.ActivityWorking
	}
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
		return liveRunParticipant(rs, *p), true
	}
}

// liveRunParticipant derives a run agent's reachability from its task, like helperParticipant does: the
// journal's descriptor says Reachable for good, but once the agent's task is terminal nobody will read new
// messages, so it must not look (or be addressable) as if somebody would.
func liveRunParticipant(rs state.RunState, p agentcomm.Participant) agentcomm.Participant {
	if p.Kind != agentcomm.KindRunAgent {
		return p
	}
	taskID, ok := runTaskForParticipant(rs, p.ID)
	if !ok {
		return p
	}
	t := rs.Assistants.Tasks[taskID]
	if t == nil {
		return p
	}
	p.Activity = taskActivity(t, idleRunAgent(rs, t))
	p.Command = t.ProfileSnapshot.Command
	p.ReadonlyEnforcement = string(t.ProfileSnapshot.ReadonlyEnforcement)
	p.AdmittedReadonlyEnforcement = string(t.ProfileSnapshot.ReadonlyEnforcement)
	if t.Status.Terminal() {
		p.Reachable = false
		p.UnreachableReason = fmt.Sprintf("task %s has reached a terminal status", taskID)
	}
	return p
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
