package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
)

// TestRunnerKind_SetGetClear проверяет базовый контракт реестра runnerKind
// (Task 6 фичи "review notes"): пусто по умолчанию, set/get, clear возвращает
// в пустое состояние. o := &Orchestrator{} без New() — runnerKind это голое
// sync.Map с нулевым значением, отдельной инициализации не требует.
func TestRunnerKind_SetGetClear(t *testing.T) {
	o := &Orchestrator{}
	if o.runnerKindOf("s1") != "" {
		t.Fatal("empty default")
	}
	o.setRunnerKind("s1", kindImplementation)
	if o.runnerKindOf("s1") != kindImplementation {
		t.Fatal("set/get")
	}
	o.clearRunnerKind("s1")
	if o.runnerKindOf("s1") != "" {
		t.Fatal("clear")
	}
}

// TestRunWithRetry_Attempt0ResumeContext закрывает Task 7 фичи "review notes"
// (decision A′): резюмируемый non-interactive стадии агент должен получить
// replay-контекст "previously completed actions" уже на ПЕРВОЙ попытке, а не
// только начиная со второй (attempt > 0) — иначе Continue после паузы
// перезапускает агента с чистого листа, будто предыдущей работы не было.
// armResumeContext — однократный флаг (stageID -> взведён), consumed внутри
// runWithRetry через LoadAndDelete: используется агентом отладки этого
// вызова, ко второму вызову той же стадии без повторного arm флаг уже снят.
//
// Переиспользует существующий тест-конструктор setupHookOrch (hooks_test.go)
// вместо изобретения параллельного newTestOrchestrator — тот уже собирает
// Orchestrator с реальными RunDir/Store/critical bus, которых достаточно,
// чтобы runWithRetry дошёл до success-ветки (agentFn возвращает nil,
// completionCheck nil → сразу EventAgentCompleted, без обращения к FSM).
func TestRunWithRetry_Attempt0ResumeContext(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Минимальная строка в форме реального stream-json лога агента (см.
	// buildRetryContext/executor.RenderActions и аналогичный
	// TestBuildRetryContext_FullActionNotTruncated в retry_test.go) — иначе
	// buildRetryContext вернёт "" для любого attempt и тест не отличит
	// "context пуст потому что не собирается" от "собирается, но пуст".
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"echo prior work"}}]}}`
	if err := os.WriteFile(filepath.Join(stageDir, "implementation.jsonl"), []byte(line+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Baseline: без arm первая (и единственная — agentFn успешен) попытка НЕ
	// получает retry-контекст.
	var unarmedCtx string
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(retryContext string) error { unarmedCtx = retryContext; return nil },
		nil, func() {})
	if unarmedCtx != "" {
		t.Fatalf("unarmed attempt-0 context should be empty, got %q", unarmedCtx)
	}

	o.armResumeContext("s1")

	var armedCtx string
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(retryContext string) error { armedCtx = retryContext; return nil },
		nil, func() {})
	if armedCtx == "" {
		t.Fatal("armed attempt-0 context should be non-empty")
	}

	// Флаг однократный: следующий вызов той же стадии без повторного arm
	// снова не должен получить контекст на attempt 0.
	var secondCtx string
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(retryContext string) error { secondCtx = retryContext; return nil },
		nil, func() {})
	if secondCtx != "" {
		t.Fatalf("resume-context flag must be consumed after first use, got %q on next call", secondCtx)
	}
}
