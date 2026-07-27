package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// blockingThenFeedbackRunner: RunPlanning сразу пишет минимально валидный
// plan.md (нужные секции для prompts.ValidatePlan/checkPlanCompletionFor),
// чтобы стадия дошла до awaiting_approval — без DashboardURL в тесте
// орхестратор авто-подтверждает план сам (headless-ветка в orchestrator.go,
// RequireApproval=false по умолчанию), и стадия проходит approve → ready →
// running без участия теста.
//
// Первый вызов RunAgent (implementation) блокируется и ждёт РЕАЛЬНЫЙ канал
// прерывания стадии — тот же самый chan struct{}, в который Revise() пишет
// для running-стадии (interruptChans в control_api.go). Мок читает его через
// orchestrator.InterruptChanForTest (test-only accessor, см. orchestrator.go)
// — это единственный способ для инъектированного мока увидеть сигнал без
// прохождения через настоящий executor.Config.InterruptCh (тот работает
// только для команд, запускаемых как реальный субпроцесс). Получив сигнал,
// мок возвращает executor.ErrUserInterrupted — так же, как это сделал бы
// настоящий executor после SIGINT — чтобы runWithRetry прошёл через ветку
// onUserInterrupted (а не просто зафейлил стадию на context.Canceled).
// Второй вызов (перезапуск с фидбеком) сразу читает feedback.md и завершает
// стадию.
//
// Используется в TestAgentSuggest_InterruptRestartsWithFeedback (Task 5,
// добавляется вместе с обработкой Revise для статуса running) — здесь только
// тип, чтобы Task 4 компилировался без готового e2e-теста.
type blockingThenFeedbackRunner struct {
	calls   int
	orch    *orchestrator.Orchestrator // выставляется тестом после New(), до Run()
	stageID string
}

func (r *blockingThenFeedbackRunner) RunPlanning(_ context.Context, _, _, outFile, _ string) error {
	plan := "## Tasks\n\n- [ ] implement feature\n\n## Assumptions\n\n- none\n\n## Acceptance Criteria\n\n- [ ] feature works\n"
	return os.WriteFile(outFile, []byte(plan), 0644)
}

func (r *blockingThenFeedbackRunner) RunAgent(ctx context.Context, _, _, _, logFile string) error {
	r.calls++
	stageDir := filepath.Dir(logFile)
	if r.calls == 1 {
		// runWithRetry регистрирует interruptCh в реестре ДО вызова agentFn
		// (следовательно, до этого RunAgent), поэтому Load здесь не гонится
		// с Revise()/Store() — канал уже на месте.
		ch, ok := orchestrator.InterruptChanForTest(r.orch, r.stageID)
		if !ok {
			<-ctx.Done()
			return context.Canceled
		}
		select {
		case <-ch:
			return executor.ErrUserInterrupted
		case <-ctx.Done():
			return context.Canceled
		}
	}
	feedback, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	if len(feedback) == 0 {
		return errors.New("expected feedback.md to be readable on the feedback restart")
	}
	// checkCompletion (completionCheck for runImplementationWithFeedback)
	// требует .done, а не execution_summary.md — тот пишет только автономный
	// трек (runAutonomousAgent), не задействованный в этом тесте.
	return os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done\n"), 0644)
}

func (r *blockingThenFeedbackRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("not used in this test")
}

var _ executor.Runner = (*blockingThenFeedbackRunner)(nil)

// TestAgentSuggest_InterruptRestartsWithFeedback — сквозной сценарий:
// стадия running → Revise с фидбеком → FSM сразу уходит в revising и
// сохраняет feedback.md → сигнал доставляется в текущий (блокирующийся)
// RunAgent через реальный interruptChans-реестр → тот возвращает
// executor.ErrUserInterrupted → runWithRetry.onUserInterrupted перезапускает
// runImplementationWithFeedback, который читает уже записанный feedback.md,
// и стадия доходит до done.
//
// Примечание: этот тест проверяет ОРКЕСТРАЦИОННУЮ часть (Revise → реестр →
// перезапуск с фидбеком на диске), а не сам SIGINT — SIGINT на реальном
// subprocess'е уже отдельно покрыт TestRunAgent_InterruptSendsSIGINTNotKill
// (Task 3, pkg/executor). Здесь мок сам слушает interruptChans через
// InterruptChanForTest и переводит сигнал в ErrUserInterrupted, эмулируя то,
// что в проде делает executor.Executor.Run вокруг настоящего subprocess'а.
func TestAgentSuggest_InterruptRestartsWithFeedback(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{
		ID: "impl", Name: "Impl",
		Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
	}}

	store, err := state.Open(runDir, []string{"impl"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	runner := &blockingThenFeedbackRunner{stageID: "impl"}
	cfg := config.Default()
	trueVal := true
	cfg.Experimental.AgentSuggest = &trueVal

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  cfg,
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})
	runner.orch = orch

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	waitForStatus(t, stateFile, "impl", state.StatusRunning, 10*time.Second)

	// planning уже должно было пройти (agents: [planning, implementation]),
	// implementation блокируется в blockingThenFeedbackRunner.RunAgent.
	if err := orch.Revise(ctx, "impl", "please add extra logging"); err != nil {
		t.Fatalf("Revise: %v", err)
	}

	waitForStatus(t, stateFile, "impl", state.StatusDone, 15*time.Second)

	feedbackPath := filepath.Join(runDir, "impl", "feedback.md")
	data, err := os.ReadFile(feedbackPath)
	if err != nil {
		t.Fatalf("feedback.md not found: %v", err)
	}
	// state.SaveFeedback prepends a "--- revision N | timestamp ---" header
	// (see pkg/state/state.go) — the phrase itself must still be present.
	if !strings.Contains(string(data), "please add extra logging") {
		t.Errorf("feedback.md = %q, want it to contain %q", data, "please add extra logging")
	}
	if runner.calls != 2 {
		t.Errorf("expected exactly 2 RunAgent calls (initial + feedback restart), got %d", runner.calls)
	}
}
