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
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// retryOnceRunner выполняет реальный script-runner и форсирует retryable-ошибку после первого RunAgent.
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

func TestIntegration_RetryContextSurvivesTruncation(t *testing.T) {
	origMax := orchestrator.MaxRetries
	orchestrator.MaxRetries = 3
	t.Cleanup(func() { orchestrator.MaxRetries = origMax })
	clk := newFakeClockWithPump(t)

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

	orch, runDir, stateFile := setupOrchestratorWithRunnerAndClock(t, stages, capture, clk)
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

	// Подтверждаем, что truncate_output продолжает делать своё дело для .log (иначе тест доказывал бы не
	// "фикс сработал", а "truncation сломан целиком") — этого файла НЕ должно быть в полном виде.
	logData, err := os.ReadFile(filepath.Join(runDir, "impl", "implementation.log"))
	if err != nil {
		t.Fatalf("read implementation.log: %v", err)
	}
	if strings.Contains(string(logData), longCmd) {
		t.Error(".log unexpectedly contains the full untruncated command — truncate_output isn't being applied to the log as designed")
	}
}

// upCompletesThenDeletesPlanRunner делегирует РЕАЛЬНОМУ Runner'у, создаёт .done после успешного RunAgent,
// и — специально для стадии "Up" — удаляет её plan.md сразу после завершения.
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
		warnings []bus.Event
	)
	collected := make(chan struct{})
	go func() {
		defer close(collected)
		for ev := range events {
			if ev.Type == bus.EventContextWarning {
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
	got := append([]bus.Event{}, warnings...)
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
