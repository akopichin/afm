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

// TestRecovery_StartupCascadeStartsSuccessorOnce — после рестарта у автономной стадии work уже лежит
// execution_summary.md, её завершение каскадом стартует зависимую script-стадию. Тот же проход startup-цикла
// не должен запускать этот скрипт повторно: статус running выставлен каскадом, а не до падения.
func TestRecovery_StartupCascadeStartsSuccessorOnce(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()
	runsLog := filepath.Join(rootDir, "successor.log")

	stages := []flow.Stage{
		{ID: "work", Name: "Work", Agents: []flow.AgentType{flow.AgentAuto}},
		{ID: "after", Name: "After", Script: "echo ran >> " + runsLog, DependsOn: []string{"work"}},
	}
	ids := []string{"work", "after"}

	store, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatal(err)
	}
	// Падение afm посреди работы агента: work остался running, summary появился уже после падения.
	if err := store.Apply(&state.Transition{StageID: "work", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if err := os.MkdirAll(filepath.Join(runDir, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "work", "execution_summary.md"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store2, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store2.Close() })
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, RootDir: rootDir, Stages: stages, Store: store2,
		Config: config.Default(), Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, filepath.Join(runDir, "state.json"), "after", state.StatusDone, 20*time.Second)
	// Startup queues any duplicate synchronously. Drain those executors before checking the side effect.
	orchestrator.WaitAgentsForTest(orch)

	data, err := os.ReadFile(runsLog)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "ran"); got != 1 {
		t.Errorf("successor script ran %d times, want exactly 1", got)
	}
}

// TestRecovery_StartupCascadeStartsAutonomousSuccessorOnce — тот же инвариант для зависимой агентной стадии:
// каскад стартует её один раз, повторный проход цикла не создаёт второго раннера.
func TestRecovery_StartupCascadeStartsAutonomousSuccessorOnce(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{
		{ID: "work", Name: "Work", Agents: []flow.AgentType{flow.AgentAuto}},
		{ID: "after", Name: "After", Agents: []flow.AgentType{flow.AgentAuto}, DependsOn: []string{"work"}},
	}
	ids := []string{"work", "after"}

	store, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "work", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if err := os.MkdirAll(filepath.Join(runDir, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "work", "execution_summary.md"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store2, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store2.Close() })
	runner := &summaryRecoveryRunner{
		calls: map[string]int{}, prompts: map[string][]string{},
		writeOnCall: map[string]int{"after": 1},
	}
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store2, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(), Runner: runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, filepath.Join(runDir, "state.json"), "after", state.StatusDone, 20*time.Second)
	orchestrator.WaitAgentsForTest(orch)

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if got := runner.calls["after"]; got != 1 {
		t.Errorf("successor agent calls = %d, want exactly 1", got)
	}
	if got := runner.calls["work"]; got != 0 {
		t.Errorf("work agent calls = %d, want 0 (summary already recovered)", got)
	}
}
