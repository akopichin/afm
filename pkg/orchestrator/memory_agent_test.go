package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/memorypipeline"
	"github.com/akopichin/afm/pkg/state"
)

// newTestOrchestrator создаёт минимальный Orchestrator с реальным временным Store для проверки defaults.
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

func TestRunMemoryAgent_SeamDefaulted(t *testing.T) {
	o := newTestOrchestrator(t) // existing test helper in this package
	if o.mem == nil {
		t.Fatal("mem pipeline must be defaulted by New")
	}

	// Override with a stub runner and confirm it is invoked (no real process) via an actual Pipeline call —
	// CaptureStage on a fresh dataset always calls the reflect step of the injected runner.
	called := false
	o.mem = memorypipeline.New(memorypipeline.Prompts{}, memorypipeline.AgentConfig{},
		memorypipeline.WithRunner(func(_ context.Context, spec memorypipeline.AgentSpec) error {
			called = true
			return os.WriteFile(spec.DatasetOut, []byte("project_level: []\nsession_level: []\n"), 0o644)
		}))

	stageDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("a plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dataset := filepath.Join(stageDir, "reflect_dataset.yaml")
	if _, err := o.mem.CaptureStage(context.Background(), flow.Stage{ID: "s1", Name: "Stage"}, stageDir, dataset, stageDir, false); err != nil {
		t.Fatalf("CaptureStage: %v", err)
	}
	if !called {
		t.Fatal("stub not invoked")
	}
}
