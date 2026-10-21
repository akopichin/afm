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

// A project artifact (a path without "./") is the agents' file in the project root — a worktree — so recovery
// finds it there and completes the stage, even though afm's own working directory has no such file.
func TestRecovery_ProjectArtifactIsCheckedInProjectRoot(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()
	stages := []flow.Stage{{
		ID:        "build",
		Name:      "Build",
		Artifacts: []flow.Artifact{{Name: "out", Path: "result/out.md"}},
	}}
	if err := os.MkdirAll(filepath.Join(rootDir, "result"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "result", "out.md"), []byte("built"), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{"build"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if err := store.Apply(&state.Transition{StageID: "build", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Join(runDir, "build")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done\n"), 0644); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store2, err := state.Open(runDir, []string{"build"})
	if err != nil {
		t.Fatalf("state.Open (reopen): %v", err)
	}
	t.Cleanup(func() { store2.Close() })
	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		RootDir: rootDir,
		Stages:  stages,
		Store:   store2,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()
	waitForStatus(t, filepath.Join(runDir, "state.json"), "build", state.StatusDone, 20*time.Second)
	cancel()
	<-runDone
}
