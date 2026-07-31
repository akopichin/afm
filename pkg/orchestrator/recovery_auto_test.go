package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// TestAutoRecover_ResumesFailedStageWithoutManualRetry simulates the exact
// scenario the feature targets: a stage genuinely failed (e.g. the process
// was killed mid-run, leaving reason "context canceled"), then a fresh
// Orchestrator.Run resumes the same run dir. With auto_recover enabled
// (the default), the stage must actually re-run and reach done — no manual
// `afm retry` call anywhere in this test.
func TestAutoRecover_ResumesFailedStageWithoutManualRetry(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()
	marker := filepath.Join(rootDir, "ran.marker")

	stages := []flow.Stage{{ID: "a", Name: "A", Script: "touch " + marker}}

	store, err := state.Open(runDir, []string{"a"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if err := store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "a", From: state.StatusRunning, To: state.StatusFailed, Event: "fail", Reason: "context canceled"}); err != nil {
		t.Fatal(err)
	}
	store.Close() // simulate process exit

	store2, err := state.Open(runDir, []string{"a"})
	if err != nil {
		t.Fatalf("state.Open (reopen): %v", err)
	}
	t.Cleanup(func() { store2.Close() })

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		RootDir: rootDir,
		Stages:  stages,
		Store:   store2,
		Config:  config.Default(), // AutoRecover defaults to true
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	stateFile := filepath.Join(runDir, "state.json")
	waitForStatus(t, stateFile, "a", state.StatusDone, 20*time.Second)
	if _, err := os.Stat(marker); err != nil {
		t.Error("expected the stage to have actually re-run after auto-recover, not just flipped status")
	}

	cancel()
	<-runDone
}

// TestAutoRecover_Disabled_LeavesFailedStageUntouched is the regression guard:
// an explicit auto_recover: false must reproduce today's behavior exactly —
// a failed stage stays failed until a manual retry.
func TestAutoRecover_Disabled_LeavesFailedStageUntouched(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()

	stages := []flow.Stage{{ID: "a", Name: "A", Script: "true"}}

	store, err := state.Open(runDir, []string{"a"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if err := store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "a", From: state.StatusRunning, To: state.StatusFailed, Event: "fail", Reason: "context canceled"}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store2, err := state.Open(runDir, []string{"a"})
	if err != nil {
		t.Fatalf("state.Open (reopen): %v", err)
	}
	t.Cleanup(func() { store2.Close() })

	cfg := config.Default()
	disabled := false
	cfg.AutoRecover = &disabled

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		RootDir: rootDir,
		Stages:  stages,
		Store:   store2,
		Config:  cfg,
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	time.Sleep(500 * time.Millisecond)
	if got := orchestrator.StoreFromOrch(orch).Get("a"); got != state.StatusFailed {
		t.Fatalf("status = %v, want failed (auto_recover: false must not touch it)", got)
	}

	cancel()
	<-runDone
}
