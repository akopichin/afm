package orchestrator_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

func TestIntegration_AutoRunGatesScriptStage(t *testing.T) {
	runDir := t.TempDir()
	disabled := false
	stages := []flow.Stage{
		{ID: "s1", Name: "S1", Script: "true", AutoRun: &disabled},
	}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	waitForStatus(t, stateFile, "s1", state.StatusPaused, 3*time.Second)

	final := loadStateJSON(t, stateFile)
	if final.Stages["s1"].PausedFrom != state.StatusPending {
		t.Errorf("PausedFrom = %q, want %q", final.Stages["s1"].PausedFrom, state.StatusPending)
	}
}

func TestIntegration_AutoRunGatesRegularStage(t *testing.T) {
	runDir := t.TempDir()
	disabled := false
	stages := []flow.Stage{
		{ID: "s1", Name: "S1", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, AutoRun: &disabled},
	}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	// No Runner injected: the gate must fire before any agent would ever be
	// spawned. If it doesn't, orch.Run would hang on a nil Runner and this
	// test would time out instead of failing cleanly on the wrong status.
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	waitForStatus(t, stateFile, "s1", state.StatusPaused, 3*time.Second)
}

// TestIntegration_AutoRunGateFiresOnceOnly locks in the "только при первой
// активации" requirement: a stage that already completed one pause/continue
// cycle (PausedFrom permanently non-empty, see state.SetStageStatusAt) must
// not re-pause when a later failure sends it back through Pending.
func TestIntegration_AutoRunGateFiresOnceOnly(t *testing.T) {
	runDir := t.TempDir()
	disabled := false
	stages := []flow.Stage{
		{ID: "s1", Name: "S1", Script: "true", AutoRun: &disabled},
	}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusPending, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusPaused, To: state.StatusPending, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(runDir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["s1"].Status != state.StatusDone {
		t.Errorf("expected done (gate must not re-fire on second pending pass), got %v", final.Stages["s1"].Status)
	}
}
