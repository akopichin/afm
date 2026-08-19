package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// TestIntegration_NonInteractiveStageAutoAnswersQuestion — полный e2e
// сценарий: non-interactive стадия (interactive не указан — дефолт false)
// запускает агента, который использует файловый диалоговый протокол (пишет
// question.json и ждёт answer.json в цикле — так же, как настоящий
// интерактивный агент). Стадия не должна ни разу побывать в
// awaiting_user_input, а должна дойти до done, получив ответ от afm.
func TestIntegration_NonInteractiveStageAutoAnswersQuestion(t *testing.T) {
	dir := t.TempDir()

	agentScript := filepath.Join(dir, "mock-agent.sh")
	script := "#!/bin/bash\n" +
		"STAGE_DIR=\"$AFM_STAGE_DIR\"\n" +
		"if [ -z \"$STAGE_DIR\" ]; then echo 'no AFM_STAGE_DIR' >&2; exit 1; fi\n" +
		`printf '{"id":"q1","question":"which option?","options":["Вариант A","Вариант B (recommended)"],"allow_custom":true}' > "$STAGE_DIR/implementation.q1.question.json"` + "\n" +
		"for i in $(seq 1 20); do\n" +
		"  if [ -f \"$STAGE_DIR/implementation.q1.answer.json\" ]; then break; fi\n" +
		"  sleep 0.5\n" +
		"done\n" +
		"if [ ! -f \"$STAGE_DIR/implementation.q1.answer.json\" ]; then echo 'timeout' >&2; exit 1; fi\n" +
		"echo done > \"$STAGE_DIR/.done\"\n" +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}}'\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\"}'\n"
	if err := os.WriteFile(agentScript, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID: "discovery", Name: "Discovery", Description: "non-interactive stage whose agent still asks a question",
		Agents:  []flow.AgentType{flow.AgentImplementation},
		Command: agentScript,
		// Interactive left unset (false) — exactly the scope this feature covers.
	}}

	stageDir := filepath.Join(dir, "discovery")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# plan"), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(dir, []string{"discovery"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_ = store.Apply(&state.Transition{StageID: "discovery", From: state.StatusPending, To: state.StatusReady, Event: "test_setup"})
	stateFile := filepath.Join(dir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  dir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	subID, events := orch.UIBus().Subscribe(64)

	var mu sync.Mutex
	var autoAnsweredCount int
	sawAwaitingUserInput := false
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		for ev := range events {
			switch ev.Type {
			case bus.EventAutoAnswered:
				mu.Lock()
				autoAnsweredCount++
				mu.Unlock()
			case bus.EventStageStatusChanged:
				if status, _ := ev.Data.(string); status == string(state.StatusAwaitingUserInput) {
					sawAwaitingUserInput = true
				}
			default:
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "discovery", state.StatusDone, 15*time.Second)
	orch.UIBus().Unsubscribe(subID)
	<-watchDone

	if sawAwaitingUserInput {
		t.Error("non-interactive stage transitioned to awaiting_user_input; question should have been auto-answered")
	}
	mu.Lock()
	got := autoAnsweredCount
	mu.Unlock()
	if got != 1 {
		t.Fatalf("want 1 EventAutoAnswered, got %d", got)
	}

	dialogPath := filepath.Join(stageDir, "implementation.dialog.jsonl")
	entries, err := mcp.ReadDialog(dialogPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Answer == nil || *entries[0].Answer != "Вариант B" || !entries[0].AutoAnswered {
		t.Fatalf("dialog entry mismatch: %+v", entries)
	}
}
