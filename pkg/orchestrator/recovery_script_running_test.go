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

// TestRecovery_ResumesScriptStageStuckRunning reproduces a real bug found via
// a Docker-mode kill/resume test: a script stage killed hard (SIGKILL, no
// chance for graceful shutdown) is left in StatusRunning with no .done file.
// The StatusRunning branch of startPlanningForPending did not check
// s.IsScript() before falling through to runImplementationAgent, which
// unconditionally looks for plan.md — a file script stages never write —
// and immediately failed the stage with a spurious
// "plan.md: no such file or directory" instead of simply restarting the
// script (the same thing retryStage already does correctly for a manually
// retried *failed* script stage — this is the missing symmetric case for a
// stage recovered directly from StatusRunning).
func TestRecovery_ResumesScriptStageStuckRunning(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()
	marker := filepath.Join(rootDir, "ran.marker")

	stages := []flow.Stage{{ID: "a", Name: "A", Script: "touch " + marker}}

	store, err := state.Open(runDir, []string{"a"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	// Simulate a hard kill mid-script: Pending -> Running, then the process
	// dies (SIGKILL) before the script ever writes .done or the FSM ever
	// reaches a terminal state. No plan.md exists for this stage (script
	// stages never write one).
	if err := store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	store.Close() // simulate process exit (SIGKILL, no graceful shutdown)

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
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	stateFile := filepath.Join(runDir, "state.json")
	waitForStatus(t, stateFile, "a", state.StatusDone, 20*time.Second)
	if _, err := os.Stat(marker); err != nil {
		t.Error("expected the script to have actually restarted and run, not failed on a missing plan.md")
	}

	cancel()
	<-runDone
}
