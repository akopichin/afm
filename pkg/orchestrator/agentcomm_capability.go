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
	// participant resumed with the reply. False until Phase 5's awaiting_peer FSM exists — reporting true
	// before that would be exactly the dishonest capability flag §6.2.2 forbids.
	CanYieldResume bool
	// Reason explains why CanSend is false. Empty when CanSend is true.
	Reason string
}

// CapabilityFor computes participant's mesh capability — read by the roster/UI (never a single global flag)
// and consulted before Send treats an executor as mesh-supported (CodeMessagingUnsupported otherwise).
func (o *Orchestrator) CapabilityFor(participant agentcomm.ParticipantID) Capability {
	if o.MeshBaseURL() == "" {
		return Capability{Reason: "messaging is disabled: the dashboard HTTP server is off (server.port: 0)"}
	}
	return Capability{CanSend: true}
}
