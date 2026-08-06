package orchestrator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// TestRunnerForAttributesStageIDForDefaultCommand воспроизводит косяк №1:
// у стадии без собственного command (использует дефолтный клиент) события
// agent_action уходили с пустым StageID — дашборд не рисовал бейдж стадии.
// Причина: runnerFor отдавал разделяемый o.runner, чей OnAction был привязан
// к пустому stageID (uiActionPublisher(ui, "")). После фикла каждая стадия
// получает per-stage runner с корректным stageID.
func TestRunnerForAttributesStageIDForDefaultCommand(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Backend"}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	// Дефолтный клиент — sh-скрипт, эмитящий одно tool_use событие в stream-json,
	// чтобы сработал OnAction (по образцу executor_test).
	script := `printf '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}\n'
printf '{"type":"result","subtype":"success"}\n'`
	cfg := config.Default()
	cfg.Client.Command = "sh"
	cfg.Client.ExtraArgs = []string{"-c", script}
	cfg.Executor.IdleTimeout = 5 * time.Second

	o := New(Options{
		RunDir: runDir,
		Stages: []flow.Stage{stage},
		Store:  store,
		Config: cfg,
		// Runner: nil — production-путь (реальный executor).
	})

	subID, ch := o.ui.Subscribe(64)
	defer o.ui.Unsubscribe(subID)

	runner := o.runnerFor(stage, phaseImplementation)
	logFile := filepath.Join(runDir, "impl.log")
	if err := runner.RunAgent(context.Background(), phaseImplementation, stage.Name, "do work", logFile); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	got := waitForAgentAction(t, ch)
	if got.StageID != stage.ID {
		t.Errorf("agent_action StageID = %q, want %q", got.StageID, stage.ID)
	}
}

func waitForAgentAction(t *testing.T, ch <-chan bus.Event) bus.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == bus.EventAgentAction {
				return ev
			}
		case <-deadline:
			t.Fatal("timed out waiting for agent_action event")
			return bus.Event{}
		}
	}
}
