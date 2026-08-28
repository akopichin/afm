package orchestrator

import (
	"context"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// newTestOrchestrator строит минимальный *Orchestrator поверх реального
// *state.Store на временном run-dir — идиома этого пакета (см.
// setupMalformedTestOrch в dialog_poller_test.go), только без стадии/событий:
// этому тесту нужен лишь сам факт дефолтации seam-поля в New().
func newTestOrchestrator(t *testing.T) *Orchestrator {
	t.Helper()
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Stage", Agents: []flow.AgentType{flow.AgentImplementation}}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	return New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
}

// Verifies the seam field exists and is defaulted (non-nil) by New via the
// package's existing test constructor. If your package has a newTestOrch
// helper, reuse it; otherwise construct Options minimally.
func TestRunMemoryAgent_SeamDefaulted(t *testing.T) {
	o := newTestOrchestrator(t) // existing test helper in this package
	if o.runMemoryAgent == nil {
		t.Fatal("runMemoryAgent must be defaulted by New")
	}
	// Override with a stub and confirm it is invoked (no real process).
	called := false
	o.runMemoryAgent = func(ctx context.Context, spec memoryAgentSpec) error {
		called = true
		return nil
	}
	_ = o.runMemoryAgent(context.Background(), memoryAgentSpec{kind: "reflect"})
	if !called {
		t.Fatal("stub not invoked")
	}
}
