package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// newOrchWithSupervisorRunner строит Orchestrator с заданным supervisor runner
// (nil = supervisor отключён). Используется тестами DetermineStagePhases.
func newOrchWithSupervisorRunner(t *testing.T, stages []flow.Stage, supervisorRunner executor.Runner) *Orchestrator {
	t.Helper()
	runDir := t.TempDir()
	ids := make([]string, len(stages))
	for i, s := range stages {
		ids[i] = s.ID
	}
	store, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return New(Options{
		RunDir:           runDir,
		Stages:           stages,
		Store:            store,
		Config:           config.Default(),
		Prompts:          DefaultPrompts(),
		SupervisorRunner: supervisorRunner,
	})
}

// TestDetermineStagePhases_Disabled: stage.Supervisor=false → базовые фазы,
// supervisor не вызывается (передан nil runner, но он и не нужен).
func TestDetermineStagePhases_Disabled(t *testing.T) {
	stage := flow.Stage{
		ID:         "s1",
		Supervisor: false,
		Agents:     []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
	}
	orch := newOrchWithSupervisorRunner(t, []flow.Stage{stage}, nil)
	phases := orch.DetermineStagePhases(context.Background(), stage)
	if len(phases) != 2 || phases[0] != "planning" {
		t.Errorf("expected base phases, got %v", phases)
	}
}

// TestDetermineStagePhases_InlineArtifactGuard: даже если supervisor вернул
// autonomous_execution, наличие inline-артефакта форсирует базовые фазы
// (planning пропускать нельзя).
func TestDetermineStagePhases_InlineArtifactGuard(t *testing.T) {
	inlineTrue := true
	stage := flow.Stage{
		ID:         "s1",
		Supervisor: true,
		Agents:     []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
		Artifacts:  []flow.Artifact{{Name: "spec", Path: "./spec.md", Inline: &inlineTrue}},
	}
	// Runner вернул бы автономное решение, но guard должен его блокировать.
	runner := &mockJSONRunner{
		response: []byte(`{"can_execute_autonomously":true,"reason":"x","recommended_phases":["autonomous_execution"]}`),
	}
	orch := newOrchWithSupervisorRunner(t, []flow.Stage{stage}, runner)
	phases := orch.DetermineStagePhases(context.Background(), stage)
	if len(phases) != 2 || phases[0] != "planning" {
		t.Errorf("inline guard failed: expected base phases, got %v", phases)
	}
}

// TestDetermineStagePhases_Autonomous: supervisor одобряет автономный трек →
// возвращается ["autonomous_execution"], пишется supervisor.jsonl.
func TestDetermineStagePhases_Autonomous(t *testing.T) {
	stage := flow.Stage{
		ID:         "s1",
		Supervisor: true,
		Agents:     []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
		Skills:     []string{"goga:apply"},
	}
	runner := &mockJSONRunner{
		response: []byte(`{"can_execute_autonomously":true,"reason":"skill handles it","recommended_phases":["autonomous_execution"]}`),
	}
	orch := newOrchWithSupervisorRunner(t, []flow.Stage{stage}, runner)
	phases := orch.DetermineStagePhases(context.Background(), stage)
	if len(phases) != 1 || phases[0] != "autonomous_execution" {
		t.Errorf("expected autonomous, got %v", phases)
	}
	// supervisor.jsonl должен появиться.
	logPath := filepath.Join(orch.opts.RunDir, "supervisor.jsonl")
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("supervisor.jsonl not written: %v", err)
	}
}

// TestDetermineStagePhases_SupervisorError_Fallback: любая ошибка LLM →
// фолбэк на базовые фазы.
func TestDetermineStagePhases_SupervisorError_Fallback(t *testing.T) {
	stage := flow.Stage{
		ID:         "s1",
		Supervisor: true,
		Agents:     []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
	}
	runner := &mockJSONRunner{err: os.ErrNotExist}
	orch := newOrchWithSupervisorRunner(t, []flow.Stage{stage}, runner)
	phases := orch.DetermineStagePhases(context.Background(), stage)
	if len(phases) != 2 || phases[0] != "planning" {
		t.Errorf("expected fallback to base phases, got %v", phases)
	}
}
