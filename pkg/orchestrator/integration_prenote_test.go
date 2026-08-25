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

// TestIntegration_PreNoteReachesFreshPrompt проверяет, что заметка, прикреплённая
// к стадии до старта (prenote.md), вклеивается в контекст агента на первом
// (свежем) запуске — тот же механизм, что уже проверен для GlobalPrompt.
func TestIntegration_PreNoteReachesFreshPrompt(t *testing.T) {
	stages := []flow.Stage{
		{ID: "s1", Name: "S1", Description: "do thing", Agents: []flow.AgentType{flow.AgentPlanning}},
	}

	runDir := t.TempDir()
	// Пишем pre-note ДО старта — так пользователь дописал бы её, пока стадия
	// pending. MkdirAll внутри runPlanningAgent файл не тронет.
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := state.SavePreNote(stageDir, "Не забудь про rate limits провайдера"); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	var capturedPrompt string
	base := mockRunner(t, mockPlanningScript)
	runner := &promptCapturingRunner{
		delegate:   base,
		onPlanning: func(prompt string) { capturedPrompt = prompt },
	}

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})

	cancel := autoApprove(orch)
	defer cancel()

	ctx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(capturedPrompt, "added before this stage started") {
		t.Errorf("assembled prompt missing pre-note header:\n%s", capturedPrompt)
	}
	if !strings.Contains(capturedPrompt, "Не забудь про rate limits провайдера") {
		t.Errorf("assembled prompt missing pre-note content:\n%s", capturedPrompt)
	}
}
