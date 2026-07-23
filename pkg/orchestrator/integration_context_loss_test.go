package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// retryOnceRunner делегирует РЕАЛЬНОМУ Runner'у (который реально исполняет
// синтетический bash-агент и пишет настоящие .jsonl/.log), затем на первом
// вызове RunAgent форсирует retryable-ошибку ПОСЛЕ того как скрипт уже
// отработал — имитирует агента, который выполнил часть работы (записал
// действия в поток) и затем словил, например, 529 overloaded.
type retryOnceRunner struct {
	delegate executor.Runner
	mu       sync.Mutex
	calls    int
}

func (r *retryOnceRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *retryOnceRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	r.mu.Lock()
	r.calls++
	n := r.calls
	r.mu.Unlock()

	if err := r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile); err != nil {
		return err
	}
	if n == 1 {
		return errors.New("529 overloaded") // retryable — см. Classify/isRetryableError
	}
	stageDir := filepath.Dir(logFile)
	return os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done"), 0644)
}

func (r *retryOnceRunner) RunJSONQuery(ctx context.Context, prompt string) ([]byte, error) {
	return r.delegate.RunJSONQuery(ctx, prompt)
}

// promptCapturingImplRunner записывает каждый implementation-промпт, который
// видит нижестоящий Runner.
type promptCapturingImplRunner struct {
	delegate executor.Runner
	mu       sync.Mutex
	prompts  []string
}

func (r *promptCapturingImplRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *promptCapturingImplRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	r.mu.Lock()
	r.prompts = append(r.prompts, prompt)
	r.mu.Unlock()
	return r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
}

func (r *promptCapturingImplRunner) RunJSONQuery(ctx context.Context, prompt string) ([]byte, error) {
	return r.delegate.RunJSONQuery(ctx, prompt)
}

// TestIntegration_RetryContextSurvivesTruncation — регрессионный e2e-тест на
// Фикс #1 из docs/superpowers/specs/2026-07-23-context-loss-audit-design.md:
// buildRetryContext раньше читал truncated .log (executor.Config.TruncateOutput),
// из-за чего ретраящийся агент видел обрезанную версию собственных прошлых
// действий вместо полной. Гоняет РЕАЛЬНЫЙ executor.New с синтетическим
// bash-агентом, который эмитит длинную Bash-команду через настоящий
// stream-json, при TruncateOutput=10 (намного меньше длины команды — как
// продовый конфиг, урезающий лог/дашборд ради читаемости). Проверяет, что:
// (a) .log реально обрезан (фича truncate_output продолжает работать для
// дашборда), но (b) вторая (retry) попытка получает в промпте ПОЛНУЮ,
// неурезанную команду первой попытки.
func TestIntegration_RetryContextSurvivesTruncation(t *testing.T) {
	origBackoff := orchestrator.RetryBackoff
	origMax := orchestrator.MaxRetries
	orchestrator.RetryBackoff = 1 * time.Millisecond
	orchestrator.MaxRetries = 3
	t.Cleanup(func() { orchestrator.RetryBackoff = origBackoff; orchestrator.MaxRetries = origMax })

	longCmd := strings.Repeat("echo-marker-", 30) // 360 chars, far above TruncateOutput below
	script := fmt.Sprintf(`echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"%s"}}]}}'
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"## Tasks\n\n- [ ] step 1\n\n## Assumptions\n\n- none\n\n## Acceptance Criteria\n\n- [ ] done\n"}]}}'
echo '{"type":"result","subtype":"success"}'`, longCmd)

	// TruncateOutput=10: реалистичный "маленький" продовый конфиг, урезающий
	// .log/agent_action ради дашборда — именно он раньше ломал retry-context.
	realRunner := executor.New(executor.Config{
		Command:        bashCommand,
		ExtraArgs:      []string{"-c", script},
		IdleTimeout:    10 * time.Second,
		TruncateOutput: 10,
	})

	capture := &promptCapturingImplRunner{delegate: &retryOnceRunner{delegate: realRunner}}

	stages := []flow.Stage{
		{ID: "impl", Name: "Impl", Description: "test retry context survives truncation",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	orch, runDir, stateFile := setupOrchestratorWithRunner(t, stages, capture)
	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["impl"].Status != state.StatusDone {
		t.Fatalf("expected done after retry, got %v", final.Stages["impl"].Status)
	}

	capture.mu.Lock()
	prompts := append([]string{}, capture.prompts...)
	capture.mu.Unlock()
	if len(prompts) != 2 {
		t.Fatalf("expected 2 RunAgent calls (initial + retry), got %d", len(prompts))
	}
	if !strings.Contains(prompts[1], longCmd) {
		t.Errorf("retry prompt should contain the FULL untruncated command from the first attempt, got:\n%s", prompts[1])
	}

	// Подтверждаем, что truncate_output продолжает делать своё дело для
	// .log (иначе тест доказывал бы не "фикс сработал", а "truncation
	// сломан целиком") — этого файла НЕ должно быть в полном виде.
	logData, err := os.ReadFile(filepath.Join(runDir, "impl", "implementation.log"))
	if err != nil {
		t.Fatalf("read implementation.log: %v", err)
	}
	if strings.Contains(string(logData), longCmd) {
		t.Error(".log unexpectedly contains the full untruncated command — truncate_output isn't being applied to the log as designed")
	}
}

// upCompletesThenDeletesPlanRunner делегирует РЕАЛЬНОМУ Runner'у, создаёт
// .done после успешного RunAgent (как doneCreatingRunner), и — специально
// для стадии "Up" — удаляет её plan.md сразу после завершения. Симулирует
// сценарий из аудита (Находка #2/#5): план зависимости пропадает/пустеет к
// моменту, когда следующая стадия начинает собственное планирование
// (например, гонка версионирования или диск). Порядок гарантирован: RunAgent
// синхронно возвращается ДО того, как оркестратор помечает "Up" завершённой
// и планирует "Down" — поэтому удаление всегда происходит раньше, чем "Down"
// читает файл через CollectDependencyPlans.
type upCompletesThenDeletesPlanRunner struct {
	delegate executor.Runner
}

func (r *upCompletesThenDeletesPlanRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *upCompletesThenDeletesPlanRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	if err := r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile); err != nil {
		return err
	}
	stageDir := filepath.Dir(logFile)
	if err := os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done"), 0644); err != nil {
		return err
	}
	if stageName == "Up" {
		_ = os.Remove(filepath.Join(stageDir, "plan.md"))
	}
	return nil
}

func (r *upCompletesThenDeletesPlanRunner) RunJSONQuery(ctx context.Context, prompt string) ([]byte, error) {
	return r.delegate.RunJSONQuery(ctx, prompt)
}

// TestIntegration_MissingDependencyPlanWarns — регрессионный e2e-тест на
// Фикс #2 из docs/superpowers/specs/2026-07-23-context-loss-audit-design.md:
// CollectDependencyPlans раньше молча деградировала до "(plan not available)"
// без какого-либо сигнала наружу. Гоняет РЕАЛЬНЫЙ двухстадийный flow
// (up → down) через orchestrator.Run с реальным executor+synth-агентом; к
// моменту начала планирования "down" её зависимость "up" лишается plan.md.
// Проверяет, что в event-фиде (o.ui) появляется EventContextWarning,
// называющий именно стадию "up", и что "down" всё равно доводится до конца.
func TestIntegration_MissingDependencyPlanWarns(t *testing.T) {
	stages := []flow.Stage{
		{ID: "up", Name: "Up", Description: "dependency stage", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
		{ID: "down", Name: "Down", Description: "depends on up", DependsOn: []string{"up"}, Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	base := mockRunner(t, mockPlanningScript)
	runner := &upCompletesThenDeletesPlanRunner{delegate: base}

	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	subID, events := orch.UIBus().Subscribe(64)
	var (
		mu       sync.Mutex
		warnings []orchestrator.Event
	)
	collected := make(chan struct{})
	go func() {
		defer close(collected)
		for ev := range events {
			if ev.Type == orchestrator.EventContextWarning {
				mu.Lock()
				warnings = append(warnings, ev)
				mu.Unlock()
			}
		}
	}()

	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	orch.UIBus().Unsubscribe(subID)
	<-collected

	mu.Lock()
	got := append([]orchestrator.Event{}, warnings...)
	mu.Unlock()

	if len(got) == 0 {
		t.Fatal("expected at least one EventContextWarning, got none")
	}
	found := false
	for _, ev := range got {
		if ev.StageID != "down" {
			continue
		}
		if msg, ok := ev.Data.(string); ok && strings.Contains(msg, "up") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a context_warning for stage 'down' naming dependency 'up', got: %+v", got)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["down"].Status != state.StatusDone {
		t.Errorf("expected 'down' to complete despite degraded context, got %v", final.Stages["down"].Status)
	}
}
