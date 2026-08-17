package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// TestIntegration_ResumeLeavesPausedStageUntouched — afm restarting must not
// auto-resume a paused stage; only an explicit Continue may.
func TestIntegration_ResumeLeavesPausedStageUntouched(t *testing.T) {
	stages := []flow.Stage{
		{ID: "paused-stage", Name: "Paused", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"paused-stage"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: "paused-stage", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "paused-stage", From: state.StatusRunning, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(runDir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = orch.Run(ctx) // expected to idle out on the ctx deadline — nothing else to complete

	final := loadStateJSON(t, stateFile)
	if final.Stages["paused-stage"].Status != state.StatusPaused {
		t.Errorf("expected paused stage to remain untouched by recovery, got %v", final.Stages["paused-stage"].Status)
	}
}

// TestContinue_FromPending_StartsScriptStage covers cases 1/3: a stage
// gated by auto_run:false (never actually started — PausedFrom=pending) is
// resumed by Continue exactly like a normal first activation.
func TestContinue_FromPending_StartsScriptStage(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{ID: "s1", Name: "S1", Script: "true"}}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusPending, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(runDir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	// The gate never fired live (status was seeded directly into paused) —
	// nothing to wait for before calling Continue.
	if err := orch.Continue(ctx, "s1"); err != nil {
		t.Fatalf("Continue: %v", err)
	}

	waitForStatus(t, stateFile, "s1", state.StatusDone, 3*time.Second)
}

// TestContinue_FromRevising_ResumesWithFeedback covers case 2: a stage
// paused mid-revision resumes via resumeStageAtStatus, exactly like an afm
// restart would.
func TestContinue_FromRevising_ResumesWithFeedback(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "revise-stuck")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.v1.md"), []byte("# Plan v1\n\nold content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("please add error handling for edge case X"), 0644); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{
		{ID: "revise-stuck", Name: "Revise Stuck", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}
	store, err := state.Open(runDir, []string{"revise-stuck"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: "revise-stuck", From: state.StatusPending, To: state.StatusRevising, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "revise-stuck", From: state.StatusRevising, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(runDir, "state.json")

	capture := &capturingPlanningRunner{delegate: mockRunner(t, mockPlanningScript)}
	runner := &doneCreatingRunner{delegate: capture}

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(), Runner: runner,
	})
	cancel := autoApprove(orch)
	defer cancel()

	ctx, ctxCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer ctxCancel()
	go func() { _ = orch.Run(ctx) }()

	if err := orch.Continue(ctx, "revise-stuck"); err != nil {
		t.Fatalf("Continue: %v", err)
	}

	waitForStatus(t, stateFile, "revise-stuck", state.StatusDone, 8*time.Second)

	capture.mu.Lock()
	prompts := append([]string{}, capture.prompts...)
	capture.mu.Unlock()
	if len(prompts) == 0 || !strings.Contains(prompts[0], "please add error handling for edge case X") {
		t.Fatalf("expected planning prompt to include feedback.md content, got: %v", prompts)
	}
}

func TestContinue_NotPaused_IsNoOp(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{ID: "s1", Name: "S1", Script: "true"}}
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

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	waitForStatus(t, stateFile, "s1", state.StatusDone, 3*time.Second)

	if err := orch.Continue(ctx, "s1"); err != nil {
		t.Fatalf("Continue on a non-paused stage should be a no-op, got error: %v", err)
	}
	final := loadStateJSON(t, stateFile)
	if final.Stages["s1"].Status != state.StatusDone {
		t.Errorf("Continue on a done stage must not change its status, got %v", final.Stages["s1"].Status)
	}
}
