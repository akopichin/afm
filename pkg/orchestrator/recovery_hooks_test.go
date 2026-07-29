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

func TestRecovery_ResumesHookFailed(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()
	mainMarker := filepath.Join(rootDir, "main-ran.marker")

	stages := []flow.Stage{{
		ID:           "notify",
		Name:         "Notify",
		Script:       "touch " + mainMarker,
		ScriptBefore: "exit 1",
	}}

	// Simulate a prior crash: stage already in hook_failed with a pending
	// before-hook recorded on disk (as runBeforeHook would have left it).
	store, err := state.Open(runDir, []string{"notify"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if err := store.Apply(&state.Transition{StageID: "notify", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "notify", From: state.StatusRunning, To: state.StatusHookFailed, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Join(runDir, "notify")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	// runBeforeHook always writes hook_pending.json BEFORE transitioning to
	// hook_failed (hooks.go) — reproduce that invariant here so resume finds
	// the same on-disk record a real crash would have left. Written raw
	// (matching hookPending's JSON shape) since this is an external test
	// package and can't call the unexported writeHookPending.
	hookPendingJSON := `{"hook":"before","script":"exit 1","timeout":0}`
	if err := os.WriteFile(filepath.Join(stageDir, "hook_pending.json"), []byte(hookPendingJSON), 0644); err != nil {
		t.Fatal(err)
	}
	store.Close() // simulate process exit

	// Reopen (simulating `afm run` restart) and re-run with a fresh Orchestrator.
	store2, err := state.Open(runDir, []string{"notify"})
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

	// The stage should still be waiting in hook_failed (not silently retried).
	time.Sleep(500 * time.Millisecond)
	if got := orchestrator.StoreFromOrch(orch).Get("notify"); got != state.StatusHookFailed {
		t.Fatalf("status = %v, want hook_failed (resumed, not auto-retried)", got)
	}

	if err := orch.SkipHook("notify"); err != nil {
		t.Fatalf("SkipHook: %v", err)
	}

	stateFile := filepath.Join(runDir, "state.json")
	waitForStatus(t, stateFile, "notify", state.StatusDone, 20*time.Second)
	if _, err := os.Stat(mainMarker); err != nil {
		t.Error("main script should have run after skip")
	}

	cancel()
	<-runDone
}
