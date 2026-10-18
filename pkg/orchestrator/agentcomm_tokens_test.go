package orchestrator

import "testing"

// Agent Mesh Phase 3, T3.1/T3.3: per-invocation token lifecycle and the capability-off gate.

func TestMesh_TokenMintResolveRevoke(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)

	if got := o.meshClientFor("stage:impl"); got != nil {
		t.Fatalf("meshClientFor must be nil when DashboardURL is unset, got %+v", got)
	}

	o.opts.DashboardURL = "http://127.0.0.1:9876"
	client := o.meshClientFor("stage:impl")
	if client == nil {
		t.Fatal("meshClientFor must be non-nil once DashboardURL is set")
	}
	if client.ParticipantID != "stage:impl" {
		t.Fatalf("ParticipantID = %q, want stage:impl", client.ParticipantID)
	}

	url, token, revoke := client.Mint()
	if url != "http://127.0.0.1:9876/api/agent-mesh" {
		t.Fatalf("mesh base url = %q", url)
	}
	if token == "" {
		t.Fatal("Mint must produce a non-empty token")
	}
	if got, ok := o.ResolveMeshToken(token); !ok || got != "stage:impl" {
		t.Fatalf("ResolveMeshToken(%q) = (%q, %v), want (stage:impl, true)", token, got, ok)
	}

	// Each Mint call issues a DIFFERENT token — tokens are per-invocation, never reused.
	_, token2, revoke2 := client.Mint()
	if token2 == token {
		t.Fatal("two Mint calls must not produce the same token")
	}

	revoke()
	if _, ok := o.ResolveMeshToken(token); ok {
		t.Fatal("a revoked token must no longer resolve")
	}
	if _, ok := o.ResolveMeshToken(token2); !ok {
		t.Fatal("revoking one invocation's token must not revoke a different invocation's token")
	}
	revoke2()
}

func TestMesh_ResolveUnknownTokenFails(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	if _, ok := o.ResolveMeshToken("never-issued"); ok {
		t.Fatal("an unknown token must never resolve to a participant")
	}
	if _, ok := o.ResolveMeshToken(""); ok {
		t.Fatal("an empty token must never resolve")
	}
}
