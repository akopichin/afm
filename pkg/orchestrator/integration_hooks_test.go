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

// TestIntegration_ScriptAfter_FailsThenSkip_StageStaysDone воспроизводит
// требование Task 13: script_after — это "fire and forget" хук ПОСЛЕ того,
// как стадия уже дошла до done. Его провал никогда не должен откатывать
// стадию обратно — только завести hook_pending.json и ждать ручного решения
// (RetryHook/SkipHook), пока стадия остаётся done для остальных потребителей
// графа (зависимые стадии не блокируются неудачным after-hook).
func TestIntegration_ScriptAfter_FailsThenSkip_StageStaysDone(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()

	stages := []flow.Stage{{
		ID:          "notify",
		Name:        "Notify",
		Script:      "echo main-ok",
		ScriptAfter: "exit 1",
	}}

	store, err := state.Open(runDir, []string{"notify"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		RootDir: rootDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	stateFile := filepath.Join(runDir, "state.json")
	// The stage reaches done immediately (after-hook failure never blocks it).
	waitForStatus(t, stateFile, "notify", state.StatusDone, 20*time.Second)

	// Give the async after-hook goroutine time to fail and write its pending marker.
	stageDir := filepath.Join(runDir, "notify")
	deadline := time.Now().Add(20 * time.Second)
	pendingPath := filepath.Join(stageDir, "hook_pending.json")
	for time.Now().Before(deadline) {
		if _, err := os.Stat(pendingPath); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(pendingPath); err != nil {
		t.Fatal("expected hook_pending.json for the failed after-hook")
	}

	if err := orch.SkipHook("notify"); err != nil {
		t.Fatalf("SkipHook: %v", err)
	}
	// Status must remain done throughout.
	if st := orchestrator.StoreFromOrch(orch).Get("notify"); st != state.StatusDone {
		t.Errorf("status = %v, want done", st)
	}

	cancel()
	<-runDone
}

// TestIntegration_PlanningOnlyStage_RunsScriptAfterOnApprove воспроизводит
// код-ревью Finding 1: approveStage (control_api.go) для планировочной
// стадии без implementation-агента триггерит EvComplete НАПРЯМУЮ, минуя
// onAgentCompleted/completeStage целиком. script_after разрешён на любой
// стадии (flow.go), так что этот путь тоже обязан запускать
// maybeRunAfterHook — иначе хук молча никогда бы не выполнился для
// планировочных стадий. Без DashboardURL планирование само уходит в
// headless auto-approve (onAgentCompleted's phasePlanning case), который и
// вызывает approveStage — тот самый путь.
func TestIntegration_PlanningOnlyStage_RunsScriptAfterOnApprove(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()
	marker := filepath.Join(rootDir, "after-ran.marker")

	stages := []flow.Stage{{
		ID:          "s1",
		Name:        "S1",
		Description: "planning-only stage",
		Agents:      []flow.AgentType{flow.AgentPlanning},
		ScriptAfter: "touch " + marker,
	}}

	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		RootDir: rootDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  mockRunner(t, mockPlanningScript),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	stateFile := filepath.Join(runDir, "state.json")
	waitForStatus(t, stateFile, "s1", state.StatusDone, 20*time.Second)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("expected script_after to run for a planning-only stage approved to completion via approveStage")
	}

	cancel()
	<-runDone
}
