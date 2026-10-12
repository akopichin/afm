package orchestrator_test

import (
	"bytes"
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

func TestAutoRecover_RespectsDependsOnOrder(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()
	bStarted := filepath.Join(rootDir, "b-started.marker")

	stages := []flow.Stage{
		{ID: "a", Name: "A", Script: "sleep 1"},
		{ID: "b", Name: "B", Script: "touch " + bStarted, DependsOn: []string{"a"}},
	}

	store, err := state.Open(runDir, []string{"a", "b"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	// a: genuine failure (e.g. context canceled from a killed process).
	if err := store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "a", From: state.StatusRunning, To: state.StatusFailed, Event: "fail", Reason: "context canceled"}); err != nil {
		t.Fatal(err)
	}
	// b: cascade failure, exactly as failBlockedStages (scheduling.go) leaves it.
	if err := store.Apply(&state.Transition{StageID: "b", From: state.StatusPending, To: state.StatusFailed, Event: "blocked_by_dep", Reason: "dep failed"}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store2, err := state.Open(runDir, []string{"a", "b"})
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

	// Midway through a's 1s sleep, b must not have started yet.
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(bStarted); err == nil {
		t.Fatal("stage b started before its dependency a finished")
	}
	if got := orchestrator.StoreFromOrch(orch).Get("b"); got == state.StatusDone {
		t.Fatal("stage b reached done before a finished")
	}

	stateFile := filepath.Join(runDir, "state.json")
	waitForStatus(t, stateFile, "a", state.StatusDone, 20*time.Second)
	waitForStatus(t, stateFile, "b", state.StatusDone, 20*time.Second)
	if _, err := os.Stat(bStarted); err != nil {
		t.Error("expected stage b to have run after auto-recover once a completed")
	}

	cancel()
	<-runDone
}

func TestAutoRecover_OnlyTouchesFailedStages(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()

	stages := []flow.Stage{
		{ID: "done-stage", Name: "Done", Script: "true"},
		{ID: "failed-stage", Name: "Failed", Script: "true"},
	}

	store, err := state.Open(runDir, []string{"done-stage", "failed-stage"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	// done-stage: already completed successfully before this run started.
	if err := store.Apply(&state.Transition{StageID: "done-stage", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "done-stage", From: state.StatusRunning, To: state.StatusDone, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	// failed-stage: genuine failure, same shape as the other auto-recover tests.
	if err := store.Apply(&state.Transition{StageID: "failed-stage", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "failed-stage", From: state.StatusRunning, To: state.StatusFailed, Event: "fail", Reason: "context canceled"}); err != nil {
		t.Fatal(err)
	}
	store.Close() // simulate process exit

	store2, err := state.Open(runDir, []string{"done-stage", "failed-stage"})
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
	waitForStatus(t, stateFile, "failed-stage", state.StatusDone, 20*time.Second)

	if got := orchestrator.StoreFromOrch(orch).Get("done-stage"); got != state.StatusDone {
		t.Fatalf("done-stage status = %v, want done (auto_recover must not touch a non-failed stage)", got)
	}

	cancel()
	<-runDone
}

func TestAutoRecover_ClearsStaleInteractiveSessionBeforeRetry(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()

	stages := []flow.Stage{{
		ID:          "review",
		Name:        "Review",
		Interactive: true,
		Command:     "true",
	}}

	store, err := state.Open(runDir, []string{"review"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if err := store.Apply(&state.Transition{StageID: "review", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "review", From: state.StatusRunning, To: state.StatusFailed, Event: "fail", Reason: "context canceled"}); err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Join(runDir, "review")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	staleSession := filepath.Join(stageDir, "implementation.session.json")
	if err := os.WriteFile(staleSession, []byte(`{"session_id":"stale-phantom"}`), 0644); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store2, err := state.Open(runDir, []string{"review"})
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	// Assert stale session content disappears, not file absence: a legitimate retry may already replace it.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(staleSession)
		if os.IsNotExist(err) || (err == nil && !bytes.Contains(data, []byte("stale-phantom"))) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if data, err := os.ReadFile(staleSession); err == nil && bytes.Contains(data, []byte("stale-phantom")) {
		t.Error("expected stale session.json content to be cleared by auto-recover before retry")
	}

	cancel()
	<-runDone
}
