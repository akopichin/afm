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

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// sideAgentParticipantAlias is the short @-mention name the bound Side agent answers to. Fixed (not derived
// from anything per-run) because, unlike a run agent — one per attached profile — a run has at most one Side
// agent, so there is nothing to disambiguate.
const sideAgentParticipantAlias = "side"

// SideAgentParticipantID returns the canonical, run-scoped mesh identity for the bound Side agent —
// "side:<runID>" — computed from the orchestrator's own run id so no caller hardcodes the prefix.
func (o *Orchestrator) SideAgentParticipantID() agentcomm.ParticipantID {
	return agentcomm.ParticipantID("side:" + o.opts.RunID)
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
		return agentcomm.Participant{}, errors.New("agentcomm: run id is required to register the side agent")
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
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventMeshParticipant, Data: map[string]any{"participant_id": string(p.ID)}})
	})
	if err != nil {
		return agentcomm.Participant{}, err
	}
	return p, nil
}
