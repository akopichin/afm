package orchestrator

import "github.com/akopichin/afm/pkg/agentcomm"

// Agent Mesh Phase 3, T3.4 (partial — see dev-plan.md's "Review decisions" for the deferred piece): an
// honest, per-participant capability record, never one global "mesh is on" boolean
// (tmp/agent-mesh.md §6.2.2's capability matrix).
type Capability struct {
	// CanSend is true once the agent-mesh HTTP API is reachable at all (the dashboard server is up) — any
	// command on PATH can shell out to `afm agent send`, since it is a plain CLI, not an adapter-specific
	// MCP tool.
	CanSend bool
	// CanReceiveBetweenTurns is ALWAYS false in this release: delivery between turns requires either an
	// afm-owned MCP tool server or a proven adapter hook, neither of which exists yet (see dev-plan.md's
	// "Future note"). Messages reach a participant only at the next turn boundary's <inbox> block (Phase 4)
	// or an explicit `afm agent inbox` call — never injected into an already-running generation.
	CanReceiveBetweenTurns bool
	// CanYieldResume reports whether registering a mesh Wait and ending the turn actually gets the
	// participant resumed with the reply. ALWAYS false today, even though Phase 5 (T5.1-T5.3) delivers a
	// real, crash-recovering yield/resume loop for STAGE agents: this field is not yet participant-kind-
	// aware, helpers have no yield path at all (T5.4), and stage-level waits still lack generation-fencing
	// (a stale reply after Revise/Retry could wake the wrong generation — see dev-plan.md's Review
	// decisions). Reporting true before ALL of that is closed would be exactly the dishonest capability flag
	// §6.2.2 forbids.
	CanYieldResume bool
	// Reason explains why CanSend is false. Empty when CanSend is true.
	Reason string
}

// CapabilityFor computes participant's mesh capability — read by the roster/UI (never a single global flag)
// and consulted before Send treats an executor as mesh-supported (CodeMessagingUnsupported otherwise).
// Today every participant gets the same CanSend/Reason regardless of kind (the `participant` argument is
// not yet consulted) — true per-participant differentiation (e.g. a kind that genuinely cannot run `afm
// agent`) is not needed by anything built so far; the parameter exists so adding it later is not a signature
// change for every caller.
func (o *Orchestrator) CapabilityFor(participant agentcomm.ParticipantID) Capability {
	if o.MeshBaseURL() == "" {
		return Capability{Reason: "messaging is disabled: the dashboard HTTP server is off (server.port: 0)"}
	}
	return Capability{CanSend: true}
}
