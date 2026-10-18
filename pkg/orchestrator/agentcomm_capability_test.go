package orchestrator

import "testing"

// Agent Mesh Phase 3, T3.4 (partial): the per-participant capability record is honest about what this
// release actually proves, not a single global "mesh is on" flag.

func TestMesh_CapabilityOffWhenDashboardDisabled(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	capability := o.CapabilityFor("helper:abc")
	if capability.CanSend {
		t.Fatal("CanSend must be false when the dashboard HTTP server is disabled")
	}
	if capability.Reason == "" {
		t.Fatal("a false CanSend must always carry a reason")
	}
}

func TestMesh_CapabilityOnNeverClaimsUnprovenGuarantees(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	o.opts.DashboardURL = "http://127.0.0.1:9876"
	capability := o.CapabilityFor("helper:abc")
	if !capability.CanSend {
		t.Fatal("CanSend must be true once the dashboard is up")
	}
	if capability.Reason != "" {
		t.Fatalf("a true CanSend must not carry a reason, got %q", capability.Reason)
	}
	// This release never claims mid-turn delivery or a working yield/resume path — both require
	// capabilities not yet built (an MCP tool server / Phase 5's awaiting_peer FSM).
	if capability.CanReceiveBetweenTurns {
		t.Fatal("CanReceiveBetweenTurns must stay false — no afm-owned MCP tool server exists yet")
	}
	if capability.CanYieldResume {
		t.Fatal("CanYieldResume must stay false until Phase 5's awaiting_peer FSM lands")
	}
}
