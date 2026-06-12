# Stage Completion Contract — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Стадия не получает `done` пока агент явно не подтвердил завершение файлом `.done` и все объявленные артефакты не существуют на диске.

**Architecture:** Проверка completion contract происходит в orchestrator после exit 0 процесса. Три уровня retry: rate limit (существующий), server 500 (новый, те же паттерны), incomplete work (новый, 1 попытка без backoff). Промпт implementation-агента содержит инструкцию создать `.done`.

**Tech Stack:** Go, существующий orchestrator/executor/state стек.

---

## File Structure

| Файл | Действие | Ответственность |
|------|----------|-----------------|
| `pkg/orchestrator/completion.go` | Создать | `checkCompletion`, `checkPlanCompletion`, `isRetryableError` |
| `pkg/orchestrator/completion_test.go` | Создать | Тесты для completion-функций |
| `pkg/orchestrator/orchestrator.go` | Изменить | `runWithRetry` — incomplete retry; `runImplementationAgent` и `runPlanningAgent` — вызов проверок; `buildImplementationPrompt` — инструкция про `.done`; `startPlanningForPending` — resume-логика для `.done` |
| `pkg/orchestrator/orchestrator_test.go` | Изменить | Обновить mock-скрипты для создания `.done` |
| `pkg/orchestrator/integration_test.go` | Изменить | Обновить mock-скрипты; добавить тесты incomplete retry и 500 retry |
| `assets/prompts/implementation.md` | Изменить | Добавить инструкцию про `.done` файл |

---

### Task 1: Функции проверки completion contract

**Files:**
- Create: `pkg/orchestrator/completion.go`
- Test: `pkg/orchestrator/completion_test.go`

- [ ] **Step 1: Написать тест `TestCheckPlanCompletion`**

```go
// completion_test.go
package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckPlanCompletion(t *testing.T) {
	t.Run("plan exists and not empty", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "plan.md"), []byte("# Plan\n- step 1"), 0644)
		if err := checkPlanCompletion(dir); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("plan missing", func(t *testing.T) {
		dir := t.TempDir()
		if err := checkPlanCompletion(dir); err == nil {
			t.Error("expected error for missing plan.md")
		}
	})

	t.Run("plan empty", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "plan.md"), []byte(""), 0644)
		if err := checkPlanCompletion(dir); err == nil {
			t.Error("expected error for empty plan.md")
		}
	})
}
```

- [ ] **Step 2: Запустить тест, убедиться что не компилируется**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -run TestCheckPlanCompletion -v`
Expected: compilation error — `checkPlanCompletion` not defined.

- [ ] **Step 3: Реализовать `checkPlanCompletion`**

```go
// completion.go
package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/flow"
)

// checkPlanCompletion verifies that plan.md exists and is not empty.
func checkPlanCompletion(stageDir string) error {
	data, err := os.ReadFile(filepath.Join(stageDir, "plan.md"))
	if err != nil {
		return fmt.Errorf("missing plan.md: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return fmt.Errorf("plan.md is empty")
	}
	return nil
}
```

- [ ] **Step 4: Запустить тест**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -run TestCheckPlanCompletion -v`
Expected: PASS.

- [ ] **Step 5: Написать тест `TestCheckCompletion`**

```go
func TestCheckCompletion(t *testing.T) {
	t.Run("done exists no artifacts", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, ".done"), []byte("all done"), 0644)
		stage := flow.Stage{ID: "s1"}
		if err := checkCompletion(dir, ".", stage); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("done missing", func(t *testing.T) {
		dir := t.TempDir()
		stage := flow.Stage{ID: "s1"}
		err := checkCompletion(dir, ".", stage)
		if err == nil {
			t.Error("expected error for missing .done")
		}
		if !isIncompleteWorkError(err) {
			t.Errorf("expected incomplete work error, got %v", err)
		}
	})

	t.Run("done empty", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, ".done"), []byte(""), 0644)
		stage := flow.Stage{ID: "s1"}
		err := checkCompletion(dir, ".", stage)
		if err == nil {
			t.Error("expected error for empty .done")
		}
		if !isIncompleteWorkError(err) {
			t.Errorf("expected incomplete work error, got %v", err)
		}
	})

	t.Run("done exists but artifact missing", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, ".done"), []byte("done"), 0644)
		stage := flow.Stage{
			ID: "s1",
			Artifacts: []flow.Artifact{
				{Name: "output", Path: "out.txt", Description: "output file"},
			},
		}
		err := checkCompletion(dir, t.TempDir(), stage)
		if err == nil {
			t.Error("expected error for missing artifact")
		}
		if isIncompleteWorkError(err) {
			t.Error("missing artifact should NOT be incomplete work (no retry)")
		}
	})

	t.Run("done exists and artifacts exist", func(t *testing.T) {
		projectDir := t.TempDir()
		stageDir := t.TempDir()
		os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done"), 0644)
		os.WriteFile(filepath.Join(projectDir, "out.txt"), []byte("data"), 0644)
		stage := flow.Stage{
			ID: "s1",
			Artifacts: []flow.Artifact{
				{Name: "output", Path: "out.txt", Description: "output file"},
			},
		}
		if err := checkCompletion(stageDir, projectDir, stage); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("artifact with stage-relative path", func(t *testing.T) {
		runDir := t.TempDir()
		stageDir := filepath.Join(runDir, "s1")
		os.MkdirAll(stageDir, 0755)
		os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done"), 0644)
		os.WriteFile(filepath.Join(stageDir, "schema.sql"), []byte("CREATE TABLE"), 0644)
		stage := flow.Stage{
			ID: "s1",
			Artifacts: []flow.Artifact{
				{Name: "db", Path: "./schema.sql", Description: "migration"},
			},
		}
		if err := checkCompletion(stageDir, ".", stage); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})
}
```

- [ ] **Step 6: Запустить тест, убедиться что не компилируется**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -run TestCheckCompletion -v`
Expected: compilation error — `checkCompletion`, `isIncompleteWorkError` not defined.

- [ ] **Step 7: Реализовать `checkCompletion`, `incompleteWorkError`, `missingArtifactError`**

```go
// incompleteWorkError signals that the agent exited successfully but didn't
// create the .done marker. This is retryable (1 attempt).
type incompleteWorkError struct {
	reason string
}

func (e *incompleteWorkError) Error() string {
	return "incomplete work: " + e.reason
}

// isIncompleteWorkError checks if err is an incomplete work error (retryable once).
func isIncompleteWorkError(err error) bool {
	if err == nil {
		return false
	}
	var target *incompleteWorkError
	return errors.As(err, &target)
}

// missingArtifactError signals that a declared artifact is missing. Not retryable.
type missingArtifactError struct {
	name string
}

func (e *missingArtifactError) Error() string {
	return "missing artifact: " + e.name
}

// checkCompletion verifies that .done exists and all declared artifacts are present.
// Returns incompleteWorkError if .done is missing (retryable).
// Returns missingArtifactError if an artifact is missing (not retryable).
func checkCompletion(stageDir, projectDir string, stage flow.Stage) error {
	data, err := os.ReadFile(filepath.Join(stageDir, ".done"))
	if err != nil {
		return &incompleteWorkError{reason: "missing .done file"}
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return &incompleteWorkError{reason: ".done file is empty"}
	}

	for _, art := range stage.Artifacts {
		resolved := resolveArtifactPath(projectDir, filepath.Dir(stageDir), stage.ID, art.Path)
		if _, err := os.Stat(resolved); err != nil {
			return &missingArtifactError{name: art.Name}
		}
	}

	return nil
}
```

Добавить `"errors"` в imports completion.go.

- [ ] **Step 8: Запустить тесты**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -run "TestCheckCompletion|TestCheckPlanCompletion" -v`
Expected: all PASS.

- [ ] **Step 9: Коммит**

```bash
git add pkg/orchestrator/completion.go pkg/orchestrator/completion_test.go
git commit -m "feat: функции проверки completion contract (.done + artifacts)"
```

---

### Task 2: Расширить `isRateLimitError` → `isRetryableError` (добавить 500)

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go:668-686` (функция `isRateLimitError`)
- Test: `pkg/orchestrator/completion_test.go`

- [ ] **Step 1: Написать тест `TestIsRetryableError`**

Добавить в `completion_test.go`:

```go
func TestIsRetryableError(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"You've hit your limit", true},
		{"rate limit exceeded", true},
		{"too many requests", true},
		{"overloaded", true},
		{"capacity", true},
		{"500 Internal Server Error", true},
		{"internal server error", true},
		{"something went wrong", false},
		{"", false},
	}
	for _, c := range cases {
		var err error
		if c.msg != "" {
			err = fmt.Errorf("%s", c.msg)
		}
		if got := isRetryableError(err); got != c.want {
			t.Errorf("isRetryableError(%q) = %v, want %v", c.msg, got, c.want)
		}
	}

	if isRetryableError(nil) {
		t.Error("nil should not be retryable")
	}
}
```

Добавить `"fmt"` в imports теста.

- [ ] **Step 2: Запустить тест, убедиться что не компилируется**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -run TestIsRetryableError -v`
Expected: compilation error — `isRetryableError` not defined.

- [ ] **Step 3: Переименовать `isRateLimitError` → `isRetryableError`, добавить паттерны 500**

В `pkg/orchestrator/orchestrator.go` заменить функцию `isRateLimitError`:

```go
// isRetryableError checks if the error is a rate limit or server error (retryable with backoff).
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	patterns := []string{
		"hit your limit",
		"rate limit",
		"too many requests",
		"overloaded",
		"capacity",
		"500",
		"internal server error",
	}
	for _, p := range patterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}
```

Обновить вызов в `runWithRetry`: заменить `isRateLimitError(err)` на `isRetryableError(err)`.

- [ ] **Step 4: Запустить все тесты пакета**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -v`
Expected: all PASS.

- [ ] **Step 5: Коммит**

```bash
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/completion.go pkg/orchestrator/completion_test.go
git commit -m "feat: isRetryableError — добавить retry на 500/internal server error"
```

---

### Task 3: Incomplete retry в `runWithRetry`

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go:728-770` (функция `runWithRetry`)

- [ ] **Step 1: Изменить сигнатуру `runWithRetry` — добавить `completionCheck`**

Текущая сигнатура:
```go
func (o *Orchestrator) runWithRetry(ctx context.Context, s flow.Stage, phase string, agentFn func(retryContext string) error)
```

Новая сигнатура:
```go
func (o *Orchestrator) runWithRetry(ctx context.Context, s flow.Stage, phase string, agentFn func(retryContext string) error, completionCheck func() error)
```

- [ ] **Step 2: Добавить incomplete retry логику после успешного exit**

В `runWithRetry`, после `if err == nil` (строка 737), вместо `return` — вызвать `completionCheck`:

```go
if err == nil {
	if completionCheck == nil {
		return
	}
	checkErr := completionCheck()
	if checkErr == nil {
		return
	}
	// Incomplete work — retry once without backoff
	if isIncompleteWorkError(checkErr) && attempt == 0 {
		o.bus.Publish(Event{
			Type:    EventStageStatusChanged,
			StageID: s.ID,
			Data:    "incomplete work, retrying: " + checkErr.Error(),
		})
		continue
	}
	// Missing artifact or second incomplete attempt — fail
	o.setStatus(s.ID, state.StatusFailed)
	return
}
```

Важно: `attempt == 0` гарантирует ровно 1 retry для incomplete work. При `attempt > 0` мы уже в retry — сразу fail.

- [ ] **Step 3: Обновить вызовы `runWithRetry` в `runPlanningAgent`**

В `runPlanningAgent` (строка 408):

```go
o.runWithRetry(ctx, s, "planning", func(retryContext string) error {
	// ... existing code ...
}, func() error {
	return checkPlanCompletion(stageDir)
})
```

- [ ] **Step 4: Обновить вызовы `runWithRetry` в `runPlanningWithFeedback`**

В `runPlanningWithFeedback` (строка 429):

```go
o.runWithRetry(ctx, s, "planning", func(retryContext string) error {
	// ... existing code ...
}, func() error {
	return checkPlanCompletion(stageDir)
})
```

- [ ] **Step 5: Обновить вызовы `runWithRetry` в `runImplementationAgent`**

В `runImplementationAgent` (строка 458):

```go
o.runWithRetry(ctx, s, "implementation", func(retryContext string) error {
	// ... existing code ...
}, func() error {
	return checkCompletion(stageDir, ".", s)
})
```

- [ ] **Step 6: Запустить все тесты пакета**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -v`
Expected: тесты сломаются — mock-скрипты не создают `.done`. Это ожидаемо, починим в Task 4.

- [ ] **Step 7: Коммит**

```bash
git add pkg/orchestrator/orchestrator.go
git commit -m "feat: incomplete retry в runWithRetry — 1 попытка при отсутствии .done"
```

---

### Task 4: Обновить mock-скрипты в тестах

**Files:**
- Modify: `pkg/orchestrator/integration_test.go:24-33` (mock-скрипты)
- Modify: `pkg/orchestrator/orchestrator_test.go:33-39` (bash-скрипт в TestPlanningPhaseMarksPlanningStatus)

- [ ] **Step 1: Обновить `mockImplementationScript` — создавать `.done`**

В `integration_test.go`:

```go
const mockImplementationScript = `echo '{"type":"assistant","message":{"content":[{"type":"text","text":"implementation done"}]}}'
echo '{"type":"result","subtype":"success"}'
# Create .done marker — stage dir is passed as last part of prompt via stdin
mkdir -p "$(pwd)"
```

Проблема: mock-скрипт не знает stageDir. Но в интеграционных тестах `mockPlanningScript` используется и для planning, и для implementation (один runner). Нужен другой подход.

Реальное решение: mock-скрипты не могут создать `.done` потому что не знают stageDir. Значит для тестов, которые проверяют полный цикл, нужно создавать `.done` **в тестовом коде** после того как implementation runner отработал, но до проверки completion. 

**Правильный подход:** использовать `promptCapturingRunner` pattern — обернуть runner чтобы после `RunAgent` создавать `.done`:

```go
// doneCreatingRunner wraps a Runner and creates .done after successful RunAgent calls.
type doneCreatingRunner struct {
	delegate executor.Runner
	runDir   string
}

func (r *doneCreatingRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *doneCreatingRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	err := r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
	if err != nil {
		return err
	}
	// Extract stage ID from logFile path: {runDir}/{stageID}/implementation.log
	stageDir := filepath.Dir(logFile)
	os.WriteFile(filepath.Join(stageDir, ".done"), []byte("test completion"), 0644)
	return nil
}
```

- [ ] **Step 2: Обернуть runner в существующих интеграционных тестах**

В `TestIntegration_FullSingleStage`:

```go
runner := mockRunner(t, mockPlanningScript)
orch, runDir, stateFile := setupOrchestratorWithRunner(t, stages, &doneCreatingRunner{
	delegate: runner,
	runDir:   runDir,
})
```

Проблема: `runDir` создаётся внутри `setupOrchestratorWithRunner`. Нужно сначала получить runDir, потом обернуть runner.

Проще: создать `setupOrchestratorWithRunner` который принимает функцию-конструктор runner:

Нет, ещё проще — `doneCreatingRunner` не нужен runDir, он берёт stageDir из `filepath.Dir(logFile)`.

Обновить `TestIntegration_FullSingleStage`:

```go
func TestIntegration_FullSingleStage(t *testing.T) {
	stages := []flow.Stage{
		{ID: "backend", Name: "Backend", Description: "implement backend", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	base := mockRunner(t, mockPlanningScript)
	runner := &doneCreatingRunner{delegate: base}
	orch, runDir, stateFile := setupOrchestratorWithRunner(t, stages, runner)
	// ... rest unchanged ...
```

Аналогично обновить: `TestIntegration_TwoParallelStages`, `TestIntegration_SequentialDependencies`, `TestIntegration_PreExistingPlan`, `TestIntegration_WithReviewAgent`.

- [ ] **Step 3: Обновить `TestPlanningPhaseMarksPlanningStatus` в `orchestrator_test.go`**

Этот тест использует свой runner напрямую. Обернуть его:

```go
base := executor.New(executor.Config{
	Command: bashCommand,
	ExtraArgs: []string{"-c",
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"# Plan\n- step 1"}]}}'
echo '{"type":"result","subtype":"success"}'`},
	IdleTimeout: 10 * time.Second,
})
runner := &doneCreatingRunner{delegate: base}
```

- [ ] **Step 4: Обновить retry-тесты**

`TestIntegration_RetryOnRateLimit` — обернуть `rateLimitThenSuccessRunner.delegate`:

Нет, `rateLimitThenSuccessRunner` сам является Runner. Нужно обернуть его вывод. Проще: обернуть весь `rateLimitThenSuccessRunner` в `doneCreatingRunner`:

```go
delegate := mockRunner(t, mockPlanningScript)
rlRunner := &rateLimitThenSuccessRunner{
	delegate:  delegate,
	failCount: 1,
	failMsg:   "You've hit your limit · resets 3pm",
}
runner := &doneCreatingRunner{delegate: rlRunner}
orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)
```

Для `TestIntegration_RetryExhausted` — не нужно, т.к. стадия всегда failed.

- [ ] **Step 5: Запустить все тесты**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -v`
Expected: all PASS.

- [ ] **Step 6: Коммит**

```bash
git add pkg/orchestrator/integration_test.go pkg/orchestrator/orchestrator_test.go
git commit -m "test: обновить mock-скрипты для создания .done в тестах"
```

---

### Task 5: Промпт-инструкция про `.done` файл

**Files:**
- Modify: `assets/prompts/implementation.md`
- Modify: `pkg/orchestrator/orchestrator.go:532-538` (функция `buildImplementationPrompt`)

- [ ] **Step 1: Добавить инструкцию в `assets/prompts/implementation.md`**

Добавить в конец файла:

```markdown

## Completion

When you have completed ALL work for this stage:
1. Verify that all tasks from the plan are implemented
2. Verify that all tests pass
3. Create a `.done` file in the stage directory using the Write tool
4. In the `.done` file, write a brief summary of what you accomplished

The `.done` file is REQUIRED — the stage will not be marked as complete without it.
```

- [ ] **Step 2: Добавить stageDir в `buildImplementationPrompt`**

Текущая сигнатура:
```go
func buildImplementationPrompt(template string, s flow.Stage, plan, dependencyContext string) string {
```

Новая сигнатура:
```go
func buildImplementationPrompt(template string, s flow.Stage, plan, dependencyContext, stageDir string) string {
```

Добавить в конец промпта:

```go
func buildImplementationPrompt(template string, s flow.Stage, plan, dependencyContext, stageDir string) string {
	extra := ""
	if len(s.Skills) > 0 {
		extra = "\n\nUse these skills: " + joinStrings(s.Skills)
	}
	doneInstr := fmt.Sprintf("\n\nStage directory for .done file: %s", stageDir)
	return fmt.Sprintf("%s\n\n## Stage: %s\n%s\n\n## Plan\n\n%s%s%s", template, s.Name, dependencyContext, plan, extra, doneInstr)
}
```

- [ ] **Step 3: Обновить вызов в `runImplementationAgent`**

В `runImplementationAgent` (строка 465):

```go
prompt := buildImplementationPrompt(o.opts.Prompts.Implementation, s, string(planData), depCtx+retryContext, stageDir)
```

- [ ] **Step 4: Запустить все тесты**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -v`
Expected: all PASS.

- [ ] **Step 5: Коммит**

```bash
git add assets/prompts/implementation.md pkg/orchestrator/orchestrator.go
git commit -m "feat: промпт-инструкция для агента создавать .done файл"
```

---

### Task 6: Resume-логика для `.done` при перезапуске

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go:337-372` (функция `startPlanningForPending`)
- Test: `pkg/orchestrator/integration_test.go`

- [ ] **Step 1: Написать интеграционный тест `TestIntegration_ResumeWithDoneFile`**

В `integration_test.go`:

```go
// TestIntegration_ResumeWithDoneFile verifies that on resume, a stage in "running"
// with an existing .done file transitions to "done" without restarting the agent.
func TestIntegration_ResumeWithDoneFile(t *testing.T) {
	stages := []flow.Stage{
		{ID: "s1", Name: "Stage 1", Description: "already done", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "s1")
	os.MkdirAll(stageDir, 0755)
	os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan"), 0644)
	os.WriteFile(filepath.Join(stageDir, ".done"), []byte("completed work summary"), 0644)

	rs := state.NewRunState([]string{"s1"})
	rs.SetStageStatus("s1", state.StatusRunning)
	stateFile := filepath.Join(runDir, "state.json")
	rs.Save(stateFile)

	// Use a failing runner — if the agent runs, the test should fail
	runner := mockRunner(t, mockFailScript)

	cfg := config.Default()
	orch := orchestrator.New(orchestrator.Options{
		RunDir:    runDir,
		Stages:    stages,
		State:     rs,
		StateFile: stateFile,
		Config:    cfg,
		Prompts:   orchestrator.DefaultPrompts(),
		Runner:    runner,
	})

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final, _ := state.Load(stateFile)
	if final.Stages["s1"].Status != state.StatusDone {
		t.Errorf("expected done (from .done file), got %v", final.Stages["s1"].Status)
	}
}
```

- [ ] **Step 2: Запустить тест, убедиться что падает**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -run TestIntegration_ResumeWithDoneFile -v`
Expected: FAIL — сейчас стадия в `running` перезапускает агента (mockFailScript → failed).

- [ ] **Step 3: Добавить resume-логику в `startPlanningForPending`**

В `startPlanningForPending`, в ветке `case state.StatusRunning:` (строка ~351), перед перезапуском агента добавить проверку `.done`:

```go
case state.StatusRunning:
	// Check if .done exists (agent completed but orchestrator missed the event)
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := checkCompletion(stageDir, ".", s); err == nil {
		o.setStatus(s.ID, state.StatusDone)
		continue
	}
	// Interrupted implementation — restart with existing plan
	go func(st flow.Stage) {
		sem := o.semFor(st)
		sem.acquire()
		defer sem.release()
		o.runImplementationAgent(ctx, st)
	}(s)
```

- [ ] **Step 4: Запустить тест**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -run TestIntegration_ResumeWithDoneFile -v`
Expected: PASS.

- [ ] **Step 5: Запустить все тесты**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -v`
Expected: all PASS.

- [ ] **Step 6: Коммит**

```bash
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/integration_test.go
git commit -m "feat: resume-логика — проверять .done при перезапуске стадии в running"
```

---

### Task 7: Интеграционный тест incomplete retry

**Files:**
- Modify: `pkg/orchestrator/integration_test.go`

- [ ] **Step 1: Написать тест `TestIntegration_IncompleteRetry`**

```go
// noDoneRunner wraps a Runner and does NOT create .done, simulating an agent
// that exits successfully without completing work.
// After retryAfter calls it starts creating .done.
type noDoneRunner struct {
	delegate   executor.Runner
	retryAfter int // create .done after this many RunAgent calls
	mu         sync.Mutex
	agentCalls int
}

func (r *noDoneRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *noDoneRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	err := r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.agentCalls++
	calls := r.agentCalls
	r.mu.Unlock()

	if calls > r.retryAfter {
		stageDir := filepath.Dir(logFile)
		os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done after retry"), 0644)
	}
	return nil
}

// TestIntegration_IncompleteRetry verifies that when an agent exits without
// creating .done, the orchestrator retries once, and the retry succeeds.
func TestIntegration_IncompleteRetry(t *testing.T) {
	stages := []flow.Stage{
		{ID: "incomplete", Name: "Incomplete", Description: "test incomplete retry", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	base := mockRunner(t, mockPlanningScript)
	runner := &noDoneRunner{
		delegate:   base,
		retryAfter: 1, // first RunAgent (implementation) — no .done; second (retry) — creates .done
	}
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runner.mu.Lock()
	calls := runner.agentCalls
	runner.mu.Unlock()

	if calls < 2 {
		t.Errorf("expected at least 2 RunAgent calls (1 incomplete + 1 retry), got %d", calls)
	}

	final, _ := state.Load(stateFile)
	if final.Stages["incomplete"].Status != state.StatusDone {
		t.Errorf("expected done after incomplete retry, got %v", final.Stages["incomplete"].Status)
	}
}
```

- [ ] **Step 2: Написать тест `TestIntegration_IncompleteRetryExhausted`**

```go
// TestIntegration_IncompleteRetryExhausted verifies that when an agent never
// creates .done, the stage fails after one retry attempt.
func TestIntegration_IncompleteRetryExhausted(t *testing.T) {
	stages := []flow.Stage{
		{ID: "never-done", Name: "Never Done", Description: "test incomplete exhausted", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	base := mockRunner(t, mockPlanningScript)
	runner := &noDoneRunner{
		delegate:   base,
		retryAfter: 999, // never create .done
	}
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final, _ := state.Load(stateFile)
	if final.Stages["never-done"].Status != state.StatusFailed {
		t.Errorf("expected failed after incomplete retry exhausted, got %v", final.Stages["never-done"].Status)
	}
}
```

- [ ] **Step 3: Запустить тесты**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -run "TestIntegration_Incomplete" -v`
Expected: PASS.

- [ ] **Step 4: Запустить все тесты**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -v`
Expected: all PASS.

- [ ] **Step 5: Коммит**

```bash
git add pkg/orchestrator/integration_test.go
git commit -m "test: интеграционные тесты incomplete retry (успех и исчерпание)"
```

---

### Task 8: Интеграционный тест retry на 500

**Files:**
- Modify: `pkg/orchestrator/integration_test.go`

- [ ] **Step 1: Написать тест `TestIntegration_RetryOnServerError`**

```go
// TestIntegration_RetryOnServerError verifies that 500 errors trigger
// backoff retry, same as rate limit errors.
func TestIntegration_RetryOnServerError(t *testing.T) {
	origBackoff := orchestrator.RetryBackoff
	orchestrator.RetryBackoff = []time.Duration{1 * time.Millisecond, 2 * time.Millisecond, 5 * time.Millisecond}
	t.Cleanup(func() { orchestrator.RetryBackoff = origBackoff })

	stages := []flow.Stage{
		{ID: "server-err", Name: "Server Error", Description: "test 500 retry", Agents: []flow.AgentType{flow.AgentPlanning}},
	}

	delegate := mockRunner(t, mockPlanningScript)
	rlRunner := &rateLimitThenSuccessRunner{
		delegate:  delegate,
		failCount: 1,
		failMsg:   "500 Internal Server Error",
	}
	runner := &doneCreatingRunner{delegate: rlRunner}
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	rlRunner.mu.Lock()
	calls := rlRunner.callCount
	rlRunner.mu.Unlock()
	if calls < 2 {
		t.Errorf("expected at least 2 calls (1 fail + 1 success), got %d", calls)
	}

	final, _ := state.Load(stateFile)
	if final.Stages["server-err"].Status != state.StatusDone {
		t.Errorf("expected done after 500 retry, got %v", final.Stages["server-err"].Status)
	}
}
```

- [ ] **Step 2: Запустить тест**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -run TestIntegration_RetryOnServerError -v`
Expected: PASS.

- [ ] **Step 3: Коммит**

```bash
git add pkg/orchestrator/integration_test.go
git commit -m "test: интеграционный тест retry на 500 Internal Server Error"
```

---

### Task 9: Линтер и финальная проверка

**Files:**
- All modified files

- [ ] **Step 1: Запустить линтер**

Run: `cd /Users/alexander.kopichin/work/flowManager && make lint`
Expected: no errors.

- [ ] **Step 2: Запустить все тесты проекта**

Run: `cd /Users/alexander.kopichin/work/flowManager && make test`
Expected: all PASS.

- [ ] **Step 3: Исправить ошибки линтера/тестов если есть**

- [ ] **Step 4: Коммит если были исправления**

```bash
git add -A
git commit -m "fix: исправления по результатам линтера"
```
