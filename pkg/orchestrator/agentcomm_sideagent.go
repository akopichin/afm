// Package orchestrator (this file, Agent Mesh Phase 6, T6.7): the bound Side agent's own mesh identity.
// §5.1/§5.2 of the source document name the Side agent as a client of the SAME Service every stage agent and
// helper uses, with its own identity — "side:<runID>" per dev-plan.md's Vocabulary table. Unlike a stage agent
// or helper (synthesized live from the stage registry/task projection, agentcomm_roster.go) and unlike a
// run-scoped role (created by AttachAndSend, agentcomm_attach.go), the Side agent has no task and no registry
// entry of its own to derive a roster row from — RegisterSideAgent is the one place that registers it, called
// once from cmd/afm, ONLY when the side-agent capability resolves on (mirrors T2.1's existing capability-off
// pattern: nothing calls this, nothing is registered — see agentcomm_roster.go's actorKindUser comment, which
// names this file as where that gap closes).
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// sideAgentParticipantAlias is the short @-mention name a newly registered bound Side agent (shown to people
// as the Assistant) answers to. Fixed (not derived from anything per-run) because a run has at most one, and
// it keeps the name even when another participant asked for it first (reserveAssistantAlias). Runs registered
// before the rename saved the alias "side" and keep it: their descriptor is never rewritten.
const sideAgentParticipantAlias = "assistant"

// SideAgentParticipantID returns the canonical, run-scoped mesh identity for the bound Side agent —
// "side:<runID>" — computed from the orchestrator's own run id so no caller hardcodes the prefix.
func (o *Orchestrator) SideAgentParticipantID() agentcomm.ParticipantID {
	return agentcomm.ParticipantID("side:" + o.opts.RunID)
}

// SetSideAgentAvailable gates new Mesh sends to the side runtime. A closing gate
// waits for sends already committing, so the inbox bridge can drain their messages.
func (o *Orchestrator) SetSideAgentAvailable(available bool) {
	o.sideAgentGate.Lock()
	o.sideAgentReachable.Store(available)
	o.sideAgentGate.Unlock()
	o.publishNotice(bus.Event{Type: bus.EventMeshParticipant, Data: map[string]any{"participant_id": string(o.SideAgentParticipantID())}})
}

// SetSideAgentDrain installs the run-scoped side bridge's normal shutdown path.
// Run starts it asynchronously once all ordinary run work is finished, then
// finalizes the Store only after it has committed all side terminal responses.
func (o *Orchestrator) SetSideAgentDrain(drain func(context.Context) bool) {
	o.sideMeshDrain = drain
}

func (o *Orchestrator) sideParticipantAvailable(p agentcomm.Participant) agentcomm.Participant {
	if p.Kind == agentcomm.KindSideAgent && !o.sideAgentReachable.Load() {
		p.Reachable = false
		p.UnreachableReason = "Assistant is unavailable"
	}
	return p
}

// SideAgentMeshClient resolves the side identity against the current Mesh URL.
// Token minting remains deferred until the executor starts an invocation.
func (o *Orchestrator) SideAgentMeshClient() *executor.MeshClient {
	return o.meshClientFor(o.SideAgentParticipantID())
}

// SideAgentExplicitReply returns the latest durable, non-automatic reply this
// side participant sent to a source message. The side conversation mirrors it
// when the process produced no separate final assistant text.
func (o *Orchestrator) SideAgentExplicitReply(sourceMessageID string) (string, bool) {
	mesh := o.opts.Store.Snapshot().Assistants.Mesh
	var latest *agentcomm.Message
	for _, msg := range mesh.Messages {
		if msg == nil || msg.Sender != o.SideAgentParticipantID() || msg.AutoResult ||
			msg.InReplyTo == nil || *msg.InReplyTo != agentcomm.MessageID(sourceMessageID) {
			continue
		}
		if latest == nil || msg.Seq > latest.Seq {
			latest = msg
		}
	}
	if latest == nil {
		return "", false
	}
	return latest.Body, true
}

// sideAgentRegisterOperationID is deterministic per run: calling RegisterSideAgent more than once (every
// process start/resume while the capability stays on) is a harmless idempotent replay of the SAME operation,
// never a second participant.
func sideAgentRegisterOperationID(runID string) string {
	return "register-side-agent:" + runID
}

// RegisterSideAgentRequest carries just enough to label the roster row — the configured executor a human sees
// when the Side agent shows up in ListParticipants/Team view.
type RegisterSideAgentRequest struct {
	// DisplayName is the Side agent's configured executor command (e.g. "claude") — the same value every
	// other "configured executor" label in the dashboard already shows, never a client-chosen name.
	DisplayName string
	Actor       state.Actor
}

// RegisterSideAgent registers the bound Side agent's own identity in the SAME mesh every stage agent and
// helper already lives in. It is NOT part of the Service interface (agentcomm.go) — unlike every Service
// method, which any mesh client may call, this is a one-shot run-startup registration only cmd/afm performs,
// exactly once, guarded by its own side-agent-enabled check (dev-plan.md T6.7's "Implement" line: "register
// the side-agent identity when the side-agent capability is enabled"). A run with the capability off never
// calls this, so ListParticipants never synthesizes a side_agent entry — there is nothing here to derive one
// from the way stageParticipant/helperParticipant do.
func (o *Orchestrator) RegisterSideAgent(ctx context.Context, req RegisterSideAgentRequest) (agentcomm.Participant, error) {
	if o.opts.RunID == "" {
		return agentcomm.Participant{}, errors.New("agentcomm: run id is required to register the Assistant")
	}
	// A resumed run keeps the descriptor it saved — alias and display name included. Submitting it again with
	// another configured command would be the same operation id with different effects.
	rs := o.opts.Store.Snapshot()
	if saved, ok := rs.Assistants.Mesh.Participants[o.SideAgentParticipantID()]; ok {
		return *saved, nil
	}
	p := agentcomm.Participant{
		ID: o.SideAgentParticipantID(), Kind: agentcomm.KindSideAgent, Scope: agentcomm.ScopeRun,
		Alias: sideAgentParticipantAlias, DisplayName: req.DisplayName, Reachable: true,
	}
	eff, err := meshParticipantAddedEffect(p)
	if err != nil {
		return agentcomm.Participant{}, err
	}
	op := state.Operation{
		ID: sideAgentRegisterOperationID(o.opts.RunID), Kind: "mesh_register_side_agent", Actor: req.Actor,
		Effects: []state.Effect{eff},
	}
	before := o.liveRoster(rs)
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventMeshParticipant, Data: map[string]any{"participant_id": string(p.ID)}})
	})
	if err != nil {
		return agentcomm.Participant{}, err
	}
	o.warnReassignedAliases(before, o.liveRoster(o.opts.Store.Snapshot()))
	return p, nil
}

// warnReassignedAliases tells the CLI about every participant that answered to a different @name before the
// bound Assistant registered: the user chose to be warned once, when the name is taken away.
func (o *Orchestrator) warnReassignedAliases(before, after []agentcomm.Participant) {
	for _, now := range after {
		i := slices.IndexFunc(before, func(p agentcomm.Participant) bool { return p.ID == now.ID })
		if i >= 0 && before[i].Alias != now.Alias {
			o.writeAssistantAliasWarning(o.aliasHolderLabel(now), now.Alias)
		}
	}
}

// aliasHolderLabel names a participant that lost @assistant the way the user declared it: a team member by its
// team: id, a run agent by its title, a stage by its id.
func (o *Orchestrator) aliasHolderLabel(p agentcomm.Participant) string {
	switch p.Kind {
	case agentcomm.KindStageAgent:
		return fmt.Sprintf("stage %q (%s)", p.StageID, p.ID)
	case agentcomm.KindRunAgent:
		for _, m := range o.opts.Team {
			if meshRunParticipantID(deriveTaskID(teamOperationID(m.ID))) == p.ID {
				return teamMemberHolder(m.ID, p.ID)
			}
		}
		return fmt.Sprintf("run agent %q (%s)", p.DisplayName, p.ID)
	default:
		return fmt.Sprintf("participant %q (%s)", p.DisplayName, p.ID)
	}
}

func teamMemberHolder(memberID string, id agentcomm.ParticipantID) string {
	return fmt.Sprintf("team member %q (%s)", memberID, id)
}

// writeAssistantAliasWarning is the one formatter of the "@assistant is taken" line, for every path that can
// move a participant off the name. Without a writer (no CLI) it prints nothing.
func (o *Orchestrator) writeAssistantAliasWarning(holder, newAlias string) {
	if o.opts.AliasWarning == nil {
		return
	}
	_, _ = fmt.Fprintf(o.opts.AliasWarning, "warning: @%s is reserved for the dashboard Assistant; %s now answers to @%s\n",
		sideAgentParticipantAlias, holder, newAlias)
}
