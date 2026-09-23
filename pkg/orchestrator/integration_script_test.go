package orchestrator_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

func TestIntegration_ScriptStage_HappyPath(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()
	marker := filepath.Join(rootDir, "script-ran.marker")

	stages := []flow.Stage{{
		ID:     "notify",
		Name:   "Notify",
		Script: "touch " + marker + "; echo done-output",
	}}

	ids := []string{"notify"}
	store, err := state.Open(runDir, ids)
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := orch.Run(ctx); err != nil && err != context.DeadlineExceeded {
		t.Fatalf("orch.Run: %v", err)
	}

	if st := orchestrator.StoreFromOrch(orch).Get("notify"); st != state.StatusDone {
		t.Fatalf("expected status done, got %s", st)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("script side effect (marker file) missing: %v", err)
	}

	logData, err := os.ReadFile(filepath.Join(runDir, "notify", "script.log"))
	if err != nil {
		t.Fatalf("script.log missing: %v", err)
	}
	if !strings.Contains(string(logData), "done-output") {
		t.Errorf("script.log missing output: %q", string(logData))
	}
}

// TestIntegration_RetryFailedScriptStage воспроизводит баг: retryStage не
// имела ветки IsScript(), поэтому попадала в "!NeedsPlanning() → искать
// plan.md" — у script-стадии нет ни plan.md, ни stage.Plan-источника, так что
// retry немедленно повторно фейлил стадию с "no plan.md and no plan source
// configured" вместо реального повторного запуска скрипта. Стадия сначала
// исчерпывает встроенные ретраи (script всегда падает, пока не появится
// gateFile), уходит в failed, затем ручной orch.Retry с уже существующим
// gateFile должен успешно перезапустить сам скрипт и довести стадию до done.
func TestIntegration_RetryFailedScriptStage(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()
	gateFile := filepath.Join(rootDir, "gate")
	marker := filepath.Join(rootDir, "script-ran.marker")

	stages := []flow.Stage{{
		ID:     "notify",
		Name:   "Notify",
		Script: "test -f " + gateFile + " && touch " + marker + " || exit 1",
	}}

	store, err := state.Open(runDir, []string{"notify"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		RootDir: rootDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		// DashboardURL непустой — Run() не должен завершиться сразу после
		// того, как стадия стала terminal (failed): нужен живой run-ctx для
		// ручного Retry() ниже, как в реальном прогоне с открытым дашбордом.
		DashboardURL: "http://test.invalid",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	// Первый прогон: gateFile ещё не создан → скрипт всегда падает → стадия
	// исчерпывает встроенные ретраи (3x/1-2-3s) и уходит в failed.
	waitForStatus(t, stateFile, "notify", state.StatusFailed, 20*time.Second)

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("marker should not exist before gate file is created")
	}

	if err := os.WriteFile(gateFile, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := orch.Retry(ctx, "notify"); err != nil {
		t.Fatalf("Retry: %v", err)
	}

	waitForStatus(t, stateFile, "notify", state.StatusDone, 10*time.Second)
	if _, err := os.Stat(marker); err != nil {
		t.Error("marker should exist after retry with gate file present")
	}

	cancel()
}

// TestRunScriptStage_Failure_PublishesScriptFailedNotice (Task 4): a
// script: stage whose script always exits non-zero and writes to stderr
// exhausts runScriptWithRetry, transitions to failed (unchanged FSM path),
// AND publishes a durable script_failed notice carrying the exit error plus
// a stderr tail — so the dashboard feed shows a reason instead of a bare
// "→ failed" row. Before this task no such notice existed.
func TestRunScriptStage_Failure_PublishesScriptFailedNotice(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()

	stages := []flow.Stage{{
		ID:     "notify",
		Name:   "Notify",
		Script: "echo boom >&2; exit 1",
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := orch.Run(ctx); err != nil && err != context.DeadlineExceeded {
		t.Fatalf("orch.Run: %v", err)
	}

	if st := orchestrator.StoreFromOrch(orch).Get("notify"); st != state.StatusFailed {
		t.Fatalf("expected status failed, got %s", st)
	}

	noticesData, err := os.ReadFile(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		t.Fatalf("read notices.jsonl: %v", err)
	}

	type scriptFailedData struct {
		Error      string `json:"error"`
		StderrTail string `json:"stderr_tail"`
		Seq        string `json:"seq"`
	}
	var found *scriptFailedData
	for _, line := range strings.Split(strings.TrimSpace(string(noticesData)), "\n") {
		var entry struct {
			Type    string           `json:"type"`
			StageID string           `json:"stage_id"`
			Data    scriptFailedData `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("invalid notices.jsonl line: %v (%s)", err, line)
		}
		if entry.Type == string(bus.EventScriptFailed) {
			if entry.StageID != "notify" {
				t.Errorf("script_failed notice stage_id = %q, want notify", entry.StageID)
			}
			data := entry.Data
			found = &data
		}
	}
	if found == nil {
		t.Fatalf("notices.jsonl missing a %q entry: %s", bus.EventScriptFailed, string(noticesData))
	}
	if found.Error == "" {
		t.Error("script_failed notice data.error is empty, want the exit error text")
	}
	if !strings.Contains(found.StderrTail, "boom") {
		t.Errorf("script_failed notice data.stderr_tail = %q, want it to contain %q", found.StderrTail, "boom")
	}
	if found.Seq == "" || found.Seq == "0" {
		t.Errorf("script_failed notice data.seq = %q, want a non-empty, non-zero applied-transition seq", found.Seq)
	}
}
