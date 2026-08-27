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

// TestButton_DeliversPromptViaRevise — сквозной сценарий: у running-стадии
// объявлена кнопка «Run linter»; orch.Button резолвит её prompt из флоу и
// доставляет его живому агенту тем же путём, что и свободная заметка (Revise):
// FSM уходит в revising, feedback.md получает prompt кнопки, блокирующийся
// RunAgent перезапускается с фидбеком и стадия доходит до done. Переиспользует
// blockingThenFeedbackRunner из agent_suggest_test.go.
func TestButton_DeliversPromptViaRevise(t *testing.T) {
	runDir := t.TempDir()
	const buttonPrompt = "Запусти golangci-lint и почини все замечания"
	stages := []flow.Stage{{
		ID: "impl", Name: "Impl",
		Agents:  []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
		Buttons: flow.Buttons{{Label: "Run linter", Prompt: buttonPrompt}},
	}}

	store, err := state.Open(runDir, []string{"impl"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	runner := &blockingThenFeedbackRunner{stageID: "impl"}
	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})
	runner.orch = orch

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "impl", state.StatusRunning, 10*time.Second)

	if err := orch.Button(ctx, "impl", "Run linter"); err != nil {
		t.Fatalf("Button: %v", err)
	}

	waitForStatus(t, stateFile, "impl", state.StatusDone, 15*time.Second)

	data, err := os.ReadFile(filepath.Join(runDir, "impl", "feedback.md"))
	if err != nil {
		t.Fatalf("feedback.md not found: %v", err)
	}
	if !strings.Contains(string(data), buttonPrompt) {
		t.Errorf("feedback.md = %q, want it to contain the button prompt %q", data, buttonPrompt)
	}
	if runner.calls != 2 {
		t.Errorf("expected exactly 2 RunAgent calls (initial + button-driven feedback restart), got %d", runner.calls)
	}
}

// TestButton_UnknownNameIsNoOp — неизвестное имя кнопки: Button возвращает nil
// и ничего не пишет (feedback.md не создаётся), Revise не вызывается.
func TestButton_UnknownNameIsNoOp(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{
		ID: "impl", Name: "Impl",
		Agents:  []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
		Buttons: flow.Buttons{{Label: "Run linter", Prompt: "lint"}},
	}}

	store, err := state.Open(runDir, []string{"impl"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	if err := orch.Button(context.Background(), "impl", "No Such Button"); err != nil {
		t.Fatalf("Button(unknown) should be a no-op, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "impl", "feedback.md")); !os.IsNotExist(err) {
		t.Errorf("unknown button must not write feedback.md, stat err=%v", err)
	}
}
