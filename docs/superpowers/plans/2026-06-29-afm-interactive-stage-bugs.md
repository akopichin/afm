# Fix: интерактивные стадии flowManager (afm bug-1 + bug-2) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Заставить интерактивные стадии `flowmanager` стабильно запускаться и не зависать на диалоге — исправить afm bug-1 (отсутствие `--verbose`, потеря stderr, фантомный `session_id`) и bug-2 (агент пишет `question.json` мимо `$FLOWMANAGER_STAGE_DIR`).

**Architecture:** Точечные правки в `pkg/executor` (claude-флаги + stderr + сессия) и `pkg/orchestrator`/`pkg/prompts` (контракт диалога, очистка сессии при отказе, детектор нарушения протокола). Переиспользуются существующие хелперы: `executor.WrittenFiles`, `sessionFile`. Новых подсистем нет.

**Tech Stack:** Go (stdlib), пакеты `pkg/executor`, `pkg/orchestrator`, `pkg/prompts`, `pkg/state`. Тесты — стандартный `testing` + существующие integration-helpers (`mockRunner`, `waitForStatus`, `loadStateJSON`, `config.Default()`).

## Global Constraints

(Из спеки `docs/superpowers/specs/2026-06-29-afm-interactive-stage-bugs-design.md` и `CLAUDE.md`. Касаются каждой задачи.)

- Коммиты — **на русском**, **БЕЗ** `Co-Authored-By`.
- **НЕ менять** версию `go` в `go.mod`.
- После правок в каждой задаче: `go build ./...`, `go vet ./...`, `go test ./...` — без ошибок. Если в репо настроен `golangci-lint` — тоже прогнать.
- Код максимально простой, без устаревших конструкций (CLAUDE.md «Always Prefer Simplicity»): убрать код ← добавить код; константа ← переменная; функция ← метод.
- Дублирования быть не должно: обязательные claude-флаги — в одном месте (`executor.DefaultClaudeArgs()`).

## Контекст для исполнителя (важно)

- **Интерактивные стадии (`stage.Interactive=true`) игнорируют внедрённый `Runner`** — `runnerFor` (`pkg/orchestrator/orchestrator.go:215`) всегда строит реальный `executor.New(...)` со `stage.Command`. Поэтому тесты A3/B2 запускают реальный bash-скрипт через `stage.Command` (как `TestFullDialogCycle`), а не фейковый runner.
- `state.StageState` (`pkg/state/state.go:29`) хранит только `Status` — **поля `Reason` в state.json нет**. Причина отказа пишется в `<runDir>/events.jsonl` (поле `reason` у `Transition`, см. `pkg/state/store.go:13-21`).
- Stream-json лог агента лежит в `<stageDir>/<phase>.jsonl`; `executor.WrittenFiles(jsonlPath)` возвращает все пути, записанные агентом через `Write` tool.

---

## Task 1: Добавить `--verbose` в дефолтные claude-флаги и убрать дублирование (afm #1.1)

**Files:**
- Modify: `pkg/executor/executor.go` (новые экспортируемые хелперы + `New`)
- Modify: `pkg/orchestrator/orchestrator.go:239-240` (`runnerFor`)
- Modify: `config.example.yaml:19` (комментарий дефолта)
- Test: `pkg/executor/executor_test.go`

**Interfaces:**
- Produces: `executor.DefaultClaudeArgs() []string`, `executor.ResolveArgs(extra []string) []string` — используются в `Task 1` же (`New`, `runnerFor`) и больше нигде.

**Почему:** Дефолтные claude-флаги продублированы в `executor.go:53` и `orchestrator.go:239`, и в обоих нет `--verbose`. Claude Code 2.1.x на ряде версий падает с `When using --print, --output-format=stream-json requires --verbose` ещё до создания conversation. `--verbose` безопасен на любой версии и даёт executor'у полный stream-json с `tool_use`-событиями.

- [x] **Step 1: Написать проваливающиеся тесты в `pkg/executor/executor_test.go`**

Добавить в конец файла:

```go
// argContains reports whether s is present in slice.
func argContains(slice []string, s string) bool {
	for _, x := range slice {
		if x == s {
			return true
		}
	}
	return false
}

func TestDefaultClaudeArgsContainsVerbose(t *testing.T) {
	args := executor.DefaultClaudeArgs()
	want := []string{"--print", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"}
	if len(args) != len(want) {
		t.Fatalf("DefaultClaudeArgs=%v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("DefaultClaudeArgs[%d]=%q, want %q", i, args[i], want[i])
		}
	}
}

func TestResolveArgsDedupsVerbose(t *testing.T) {
	got := executor.ResolveArgs([]string{"--verbose", "--model", "foo"})
	count := 0
	for _, a := range got {
		if a == "--verbose" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("--verbose count=%d, want 1; args=%v", count, got)
	}
	if !argContains(got, "--model") || !argContains(got, "foo") {
		t.Errorf("user args lost: %v", got)
	}
}

func TestResolveArgsEmptyReturnsDefaults(t *testing.T) {
	got := executor.ResolveArgs(nil)
	if len(got) == 0 || !argContains(got, "--verbose") {
		t.Errorf("ResolveArgs(nil)=%v, want defaults with --verbose", got)
	}
}

// TestNewAppliesDefaultArgs проверяет, что New без ExtraArgs подставляет дефолт,
// содержащий --verbose (afm bug #1.1).
func TestNewAppliesDefaultArgs(t *testing.T) {
	dir := t.TempDir()
	argsOut := filepath.Join(dir, "args.txt")
	scriptPath := filepath.Join(dir, "echoargs.sh")
	script := "#!/bin/bash\necho \"$@\" > " + argsOut + "\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(dir, "impl.log")

	// ExtraArgs не задан → New применяет DefaultClaudeArgs (с --verbose).
	ex := executor.New(executor.Config{
		Command:     scriptPath,
		IdleTimeout: 5 * time.Second,
	})

	if err := ex.RunAgent(context.Background(), "implementation", "s1", "do work", logFile); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	data, err := os.ReadFile(argsOut)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	if !strings.Contains(string(data), "--verbose") {
		t.Errorf("default args missing --verbose: %q", string(data))
	}
}
```

- [x] **Step 2: Запустить тесты — убедиться, что они падают**

Run: `go test ./pkg/executor/ -run 'TestDefaultClaudeArgs|TestResolveArgs|TestNewAppliesDefaultArgs' -v`
Expected: FAIL (`DefaultClaudeArgs undefined and cannot be used as ... value` / compile error — функции ещё нет).

- [x] **Step 3: Реализовать хелперы и применить дефолт в `pkg/executor/executor.go`**

Вставить после блока `const (...)` (после строки с `toolNameWrite = "Write"`, ~строка 36):

```go
// DefaultClaudeArgs returns the standard claude stream-json invocation flags.
//
// --verbose is required by Claude Code 2.1.x when --print is combined with
// --output-format=stream-json, and is harmless on versions where it is
// optional. It also makes the stream contain tool_use events, which the
// executor's parser relies on.
func DefaultClaudeArgs() []string {
	return []string{"--print", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"}
}

// ResolveArgs prepends DefaultClaudeArgs to extra and drops exact duplicates.
// Used for interactive stages, which always need the claude flags regardless of
// user config; dedup avoids passing --verbose twice when the user also sets it.
func ResolveArgs(extra []string) []string {
	merged := append(append([]string{}, DefaultClaudeArgs()...), extra...)
	seen := make(map[string]bool, len(merged))
	out := make([]string, 0, len(merged))
	for _, a := range merged {
		if seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}
```

Заменить в `New` (~строки 52-54) блок:
```go
	if len(cfg.ExtraArgs) == 0 {
		cfg.ExtraArgs = []string{"--print", "--output-format", "stream-json", "--dangerously-skip-permissions"}
	}
```
на:
```go
	if len(cfg.ExtraArgs) == 0 {
		cfg.ExtraArgs = DefaultClaudeArgs()
	}
```

- [x] **Step 4: Применить общий хелпер в `runnerFor` — `pkg/orchestrator/orchestrator.go`**

Заменить строки 239-240:
```go
	requiredArgs := []string{"--print", "--output-format", "stream-json", "--dangerously-skip-permissions"}
	extraArgs := append(requiredArgs, o.opts.Config.Client.ExtraArgs...)
```
на:
```go
	// Interactive stages always need the claude stream-json flags (incl. --verbose,
	// afm bug #1.1). ResolveArgs prepends defaults and dedups user overrides.
	extraArgs := executor.ResolveArgs(o.opts.Config.Client.ExtraArgs)
```
(`executor` уже импортирован в `orchestrator.go`.)

- [x] **Step 5: Обновить комментарий дефолта в `config.example.yaml`**

В `config.example.yaml` заменить строку 19:
```yaml
  # Default: ["--print", "--output-format", "stream-json", "--dangerously-skip-permissions"]
```
на:
```yaml
  # Default: ["--print", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"]
```

- [x] **Step 6: Собрать, проверить vet, прогнать тесты**

Run:
```bash
go build ./...
go vet ./...
go test ./pkg/executor/ ./pkg/orchestrator/ -count=1
```
Expected: build/vet чисто; все тесты PASS (в т.ч. новые и существующие `TestSessionIDPassedAsArg` и т.д.).

- [x] **Step 7: Коммит**

```bash
git add pkg/executor/executor.go pkg/executor/executor_test.go pkg/orchestrator/orchestrator.go config.example.yaml
git commit -m "fix: добавить --verbose в дефолтные claude-флаги и убрать дублирование"
```

---

## Task 2: Писать stderr агента в `<phase>.stderr.log` вместо `io.Discard` (afm #1.2)

**Files:**
- Modify: `pkg/executor/executor.go` (`run` сигнатура + 2 call-сайта `RunPlanning`, `RunAgent`; убрать `cmd.Stderr = io.Discard`)
- Test: `pkg/executor/executor_test.go`

**Interfaces:**
- Consumes: ничего из соседних задач.
- Produces: побочный файл `<stageDir>/<phase>.stderr.log` (никаких новых сигнатур наружу).

**Почему:** `cmd.Stderr = io.Discard` (`executor.go:370`) теряет реальную диагностику claude (например ту самую «requires --verbose»). В логе остаётся голый `exit status 1`. Направление stderr в файл превращает отладку в 30-секундную задачу.

- [x] **Step 1: Написать проваливающийся тест в `pkg/executor/executor_test.go`**

Добавить:

```go
// TestRunAgentCapturesStderr проверяет, что stderr подпроцесса пишется в
// <logBase>.stderr.log (afm bug #1.2) — раньше он уходил в io.Discard.
func TestRunAgentCapturesStderr(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "impl.log")
	// stdout: результат (RunAgent завершается без ошибки); stderr: диагностика.
	script := `echo '{"type":"result","subtype":"success"}'; echo 'INTERNAL: boom' >&2`

	ex := executor.New(executor.Config{
		Command:     testCmdShell,
		ExtraArgs:   []string{testFlagC, script},
		IdleTimeout: 5 * time.Second,
	})

	if err := ex.RunAgent(context.Background(), "implementation", "s1", "do work", logFile); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	stderrFile := strings.TrimSuffix(logFile, ".log") + ".stderr.log"
	data, err := os.ReadFile(stderrFile)
	if err != nil {
		t.Fatalf("read stderr log: %v", err)
	}
	if !strings.Contains(string(data), "INTERNAL: boom") {
		t.Errorf("stderr not captured to file: %q", string(data))
	}
}
```

- [x] **Step 2: Запустить тест — убедиться, что падает**

Run: `go test ./pkg/executor/ -run TestRunAgentCapturesStderr -v`
Expected: FAIL (`read stderr log: open ... no such file` — файла нет, т.к. stderr сбрасывается).

- [x] **Step 3: Изменить сигнатуру `run` и убрать `io.Discard` — `pkg/executor/executor.go`**

Заменить сигнатуру `run` (~строка 340):
```go
func (e *Executor) run(ctx context.Context, prompt string, lineCallback func(string)) error {
```
на:
```go
func (e *Executor) run(ctx context.Context, prompt string, stderr io.Writer, lineCallback func(string)) error {
```

Заменить строку `cmd.Stderr = io.Discard` (~строка 370) на:
```go
	cmd.Stderr = stderr
```

- [x] **Step 4: Открыть stderr-файл и передать в `run` из `RunPlanning`**

В `RunPlanning`, сразу после блока открытия `jsonlFile`/`defer jf.Close()` и ДО `lg.LogStart(...)`, вставить:
```go
	// Capture the agent's stderr to a sibling .stderr.log so diagnostics (e.g.
	// claude "requires --verbose") are not lost. stderr is diagnostic only — if
	// the file cannot be opened, fall back to discarding rather than failing.
	var stderr io.Writer = io.Discard
	if sf, err := os.OpenFile(strings.TrimSuffix(logFile, ".log")+".stderr.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		stderr = sf
		defer sf.Close()
	}
```
И в вызове `e.run(...)` (внутри `runErr := e.run(...)`) добавить `stderr` вторым аргументом после `prompt`:
```go
	runErr := e.run(ctx, prompt, stderr, func(line string) {
```

- [x] **Step 5: То же самое в `RunAgent`**

В `RunAgent` сразу после `defer jf.Close()` и до `lg.LogStart(...)`, вставить тот же блок:
```go
	var stderr io.Writer = io.Discard
	if sf, err := os.OpenFile(strings.TrimSuffix(logFile, ".log")+".stderr.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		stderr = sf
		defer sf.Close()
	}
```
И в вызове `e.run(...)` добавить `stderr`:
```go
	runErr := e.run(ctx, prompt, stderr, func(line string) {
```

- [x] **Step 6: Собрать, vet, тесты**

Run:
```bash
go build ./...
go vet ./...
go test ./pkg/executor/ -count=1
```
Expected: build/vet чисто; все тесты PASS (включая новые; существующие не должны ломаться — у них просто появится пустой `.stderr.log`).

- [x] **Step 7: Коммит**

```bash
git add pkg/executor/executor.go pkg/executor/executor_test.go
git commit -m "fix: писать stderr агента в <phase>.stderr.log вместо io.Discard"
```

---

## Task 3: Сбрасывать фантомный `session_id` при non-retryable ошибке (afm #1.3)

**Files:**
- Modify: `pkg/orchestrator/retry.go:100-104` (non-retryable ветка)
- Modify: `pkg/orchestrator/orchestrator.go` (`onManualRetry` — страховка)
- Test: `pkg/orchestrator/integration_interactive_test.go`

**Interfaces:**
- Consumes: `sessionFile(stageDir, phase)` (уже есть в `session.go`), константы `phasePlanning/phaseImplementation/phaseReview`.
- Produces: ничего нового наружу.

**Почему:** `loadOrCreateSession` пишет UUID в `<phase>.session.json` ДО запуска claude. При падении первой попытки файл содержит ID несуществующего conversation; retry с `--resume <phantom>` → `No conversation found` → бесконечный цикл. Удаление session-файла при retryable-ошибке уже есть (`retry.go:110`, коммит `3cc3a0c`) — распространяем тот же паттерн на non-retryable.

- [x] **Step 1: Написать проваливающийся тест в `pkg/orchestrator/integration_interactive_test.go`**

Добавить (импорты `config`, `flow`, `state`, `orchestrator`, `time`, `os`, `filepath`, `context` уже есть в этом файле):

```go
// TestIntegration_InteractiveFailureClearsSession: интерактивная стадия падает
// на non-retryable ошибке — фантомный planning.session.json должен быть удалён,
// иначе retry упадёт с "No conversation found" (afm bug #1.3).
func TestIntegration_InteractiveFailureClearsSession(t *testing.T) {
	dir := t.TempDir()

	failScript := filepath.Join(dir, "fail.sh")
	script := "#!/bin/bash\necho 'fatal: exit status 1' >&2\nexit 1\n"
	if err := os.WriteFile(failScript, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID:          "propose",
		Name:        "Propose",
		Description: "interactive planning that fails",
		Agents:      []flow.AgentType{flow.AgentPlanning},
		Interactive: true,
		Command:     failScript,
	}}

	store, err := state.Open(dir, []string{"propose"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(dir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  dir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	waitForStatus(t, stateFile, "propose", state.StatusFailed, 10*time.Second)

	// loadOrCreateSession успел создать planning.session.json до падения;
	// после фикса non-retryable-ветка обязана его удалить.
	sessionPath := filepath.Join(dir, "propose", "planning.session.json")
	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Errorf("planning.session.json should be removed after non-retryable failure; stat err=%v", err)
	}
}
```

- [x] **Step 2: Запустить тест — убедиться, что падает**

Run: `go test ./pkg/orchestrator/ -run TestIntegration_InteractiveFailureClearsSession -v`
Expected: FAIL — `planning.session.json should be removed ...` (файл остаётся, т.к. non-retryable-ветка его не чистит).

- [x] **Step 3: Удалить session-файл в non-retryable ветке — `pkg/orchestrator/retry.go`**

Заменить строки 100-104:
```go
		if !isRetryableError(err) {
			o.Trigger(s.ID, EvFail, GuardCtx{}, err.Error())
			o.failBlockedStages()
			return
		}
```
на:
```go
		if !isRetryableError(err) {
			// Drop the session file so a later retry starts a fresh Claude session
			// instead of resuming a conversation that was never created (e.g. the
			// process died before claude created it). Mirrors the retryable branch.
			_ = os.Remove(sessionFile(stageDir, phase))
			o.Trigger(s.ID, EvFail, GuardCtx{}, err.Error())
			o.failBlockedStages()
			return
		}
```
(`os` уже импортирован в `retry.go`; `sessionFile` — в том же пакете, `session.go`.)

- [x] **Step 4: Страховка в `onManualRetry` — `pkg/orchestrator/orchestrator.go`**

В `onManualRetry` сразу после `stage := o.graph.Stage(stageID)` и проверки `if stage == nil { return nil }` (~строки 591-594), вставить:
```go
	// Manual retry of an interactive stage must start a fresh Claude session:
	// a leftover <phase>.session.json may reference a conversation that was
	// never created (phantom), which makes claude fail with "No conversation
	// found". Clear all phase sessions for this stage.
	if stage.Interactive {
		stageDir := filepath.Join(o.opts.RunDir, stageID)
		for _, ph := range []string{phasePlanning, phaseImplementation, phaseReview} {
			_ = os.Remove(sessionFile(stageDir, ph))
		}
	}
```
(`filepath`, `os` импортированы; константы и `sessionFile` в том же пакете.)

- [x] **Step 5: Собрать, vet, тесты**

Run:
```bash
go build ./...
go vet ./...
go test ./pkg/orchestrator/ -count=1
```
Expected: build/vet чисто; все тесты PASS (включая новый и существующие retry-тесты).

- [x] **Step 6: Коммит**

```bash
git add pkg/orchestrator/retry.go pkg/orchestrator/orchestrator.go pkg/orchestrator/integration_interactive_test.go
git commit -m "fix: сбрасывать фантомный session_id при non-retryable ошибке"
```

---

## Task 4: Усилить контракт диалога — абсолютный путь + «nowhere else» (afm bug-2, часть 1)

**Files:**
- Modify: `pkg/prompts/builder.go` (блок `interactive_rules` + импорт `path/filepath`)
- Modify: `pkg/orchestrator/orchestrator.go` (передать `StageDir` в `prompts.Build` в 4 местах)
- Test: `pkg/prompts/builder_test.go`

**Interfaces:**
- Consumes: поле `Inputs.StageDir` (уже объявлено в `builder.go:28`, но не используется).
- Produces: ничего нового.

**Почему:** Контракт упоминает `$FLOWMANAGER_STAGE_DIR` один раз вверху, и к моменту записи `question.json` LLM может придумать путь вроде `.flowManager/stages/...`. Поля `Inputs.StageDir` нет в сборке промпта. Печатаем агенту **буквальный абсолютный путь** и явное «nowhere else».

- [x] **Step 1: Написать проваливающийся тест в `pkg/prompts/builder_test.go`**

Добавить (`filepath` импортировать; если его нет в импортах — добавить `"path/filepath"`):

```go
func TestBuild_InteractivePrintsAbsolutePathAndNowhereElse(t *testing.T) {
	stageDir := t.TempDir()
	in := Inputs{
		Template:    "RULES",
		Stage:       flow.Stage{ID: "propose", Name: "Propose", Description: "ask user"},
		PhaseAgent:  AgentPlanning,
		StageDir:    stageDir,
		Interactive: true,
	}
	out := Build(in)

	// Абсолютный путь stage-директории должен быть напечатан явно.
	if !strings.Contains(out, stageDir) {
		t.Errorf("interactive prompt should contain absolute stage dir %q:\n%s", stageDir, out)
	}
	// Явный запрет писать куда-либо ещё.
	if !strings.Contains(out, "nowhere else") && !strings.Contains(out, "NOWHERE ELSE") {
		t.Errorf("interactive prompt should say 'nowhere else':\n%s", out)
	}
	// Путь для записи вопроса.
	if !strings.Contains(out, "planning.q<N>.question.json") {
		t.Errorf("interactive prompt should show question file path:\n%s", out)
	}
}
```

- [x] **Step 2: Запустить тест — убедиться, что падает**

Run: `go test ./pkg/prompts/ -run TestBuild_InteractivePrintsAbsolutePathAndNowhereElse -v`
Expected: FAIL — промпт не содержит `stageDir` и «nowhere else».

- [x] **Step 3: Пополнить импорт и переписать `interactive_rules` — `pkg/prompts/builder.go`**

В импортах добавить `path/filepath` (после `"fmt"`):
```go
import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/flow"
)
```

Заменить весь блок `if in.Interactive { ... }` (строки 43-59) на:
```go
	if in.Interactive {
		// Печатаем буквальный абсолютный путь stage-директории, чтобы у агента
		// не было причин придумывать путь (afm bug-2: агент писал question.json
		// в .flowManager/stages/... и ломал диалог).
		stageDir := in.StageDir
		if abs, err := filepath.Abs(in.StageDir); err == nil && abs != "" {
			stageDir = abs
		}
		sb.WriteString("\n\n<interactive_rules>\n")
		sb.WriteString("Use the file-based dialog protocol to ask the user questions.\n")
		fmt.Fprintf(&sb, "Your stage directory is FLOWMANAGER_STAGE_DIR=%q\n", stageDir)
		sb.WriteString("Assign sequential IDs: q1, q2, … (never reuse an ID within a phase).\n\n")
		sb.WriteString("For each question:\n")
		sb.WriteString("1. Write the question file using the Write tool:\n")
		fmt.Fprintf(&sb, "   Path: %s/%s.q<N>.question.json  (== $FLOWMANAGER_STAGE_DIR/%s.q<N>.question.json)\n", stageDir, in.PhaseAgent, in.PhaseAgent)
		sb.WriteString("   Write the file to this path and NOWHERE ELSE. Do NOT invent paths like .flowManager/stages/... — always use $FLOWMANAGER_STAGE_DIR.\n")
		sb.WriteString("   Content: {\"id\":\"qN\",\"question\":\"## Full context here\\n\\nYour question?\",\"options\":[\"A\",\"B\"],\"allow_custom\":true}\n")
		sb.WriteString("   Put ALL context in 'question': descriptions, trade-offs, examples. Use markdown freely.\n")
		sb.WriteString("2. Wait for the answer via Bash:\n")
		fmt.Fprintf(&sb, "   while [ ! -f \"$FLOWMANAGER_STAGE_DIR/%s.qN.answer.json\" ]; do sleep 30; done && cat \"$FLOWMANAGER_STAGE_DIR/%s.qN.answer.json\"\n", in.PhaseAgent, in.PhaseAgent)
		sb.WriteString("3. If bash times out (10 min) without the file: run the exact same bash command again.\n")
		sb.WriteString("   NEVER give up waiting — keep retrying the bash loop until the file appears.\n")
		sb.WriteString("Ask ONE question at a time.\n")
		sb.WriteString("</interactive_rules>\n")
	}
```

- [x] **Step 4: Передать `StageDir` в `prompts.Build` — `pkg/orchestrator/orchestrator.go`**

В каждой из 4 сборок промпта добавить поле `StageDir: stageDir` (переменная `stageDir` уже вычислена в каждой функции как `filepath.Join(o.opts.RunDir, s.ID)`):

1. `runPlanningAgent` — в `prompts.Build(prompts.Inputs{...})` (~строка 800) добавить:
   ```go
			StageDir:         stageDir,
   ```
   (внутрь литерала `Inputs{}`, рядом с другими полями).

2. `runPlanningWithFeedback` (~строка 892) — добавить `StageDir: stageDir,`.

3. `runImplementationAgent` (~строка 968) — добавить `StageDir: stageDir,`.

4. `runReviewAgent` (~строка 1012) — добавить `StageDir: stageDir,`.

- [x] **Step 5: Собрать, vet, тесты (включая golden)**

Run:
```bash
go build ./...
go vet ./...
go test ./pkg/prompts/ ./pkg/orchestrator/ -count=1
```
Expected: build/vet чисто; новый тест PASS; `TestBuild_Golden_PlanningSimple` PASS (interactive=false — блок не меняется, golden не затронут); все orchestrator-тесты PASS.

- [x] **Step 6: Коммит**

```bash
git add pkg/prompts/builder.go pkg/prompts/builder_test.go pkg/orchestrator/orchestrator.go
git commit -m "fix: усилить контракт диалога — абсолютный путь + nowhere else"
```

---

## Task 5: Детектор нарушения протокола диалога — fail-fast вместо зависания (afm bug-2, часть 2)

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go` (новые функции `detectDialogViolation`, `pathInside`; вызов из `pollQuestions`)
- Test: `pkg/orchestrator/integration_interactive_test.go`

**Interfaces:**
- Consumes: `executor.WrittenFiles(jsonlPath)` (Task ничего не добавляет в executor); константы фаз.
- Produces: ничего наружу.

**Почему:** Если агент всё же написал `question.json` вне `$FLOWMANAGER_STAGE_DIR`, poller/dashboard его не увидят — стадия зависает навсегда (худший режим отказа). Детектор сканирует stream-json лог через существующий `WrittenFiles`: найден `Write` в `*.question.json` вне stageDir → `FailStage` с понятной причиной.

- [x] **Step 1: Написать проваливающийся тест в `pkg/orchestrator/integration_interactive_test.go`**

Добавить (`fmt` и `strings` уже импортированы в пакете `orchestrator_test` через `integration_test.go`):

```go
// TestIntegration_DialogViolationDetected: интерактивный агент пишет
// question.json ВНЕ stageDir — стадия должна перейти в failed с причиной
// "dialog protocol violation" вместо вечного зависания (afm bug-2).
func TestIntegration_DialogViolationDetected(t *testing.T) {
	dir := t.TempDir()

	// «Неправильная» директория, куда агент по ошибке кладёт вопрос.
	wrongDir := filepath.Join(dir, "wrong-stages", "propose")
	if err := os.MkdirAll(wrongDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wrongQuestion := filepath.Join(wrongDir, "planning.q1.question.json")

	// Скрипт эмитит Write tool_use с неверным путём в stream-json, затем ждёт
	// (имитируя зависший bash-loop), пока poller не детектит нарушение.
	scriptPath := filepath.Join(dir, "badagent.sh")
	script := "#!/bin/bash\n" +
		fmt.Sprintf(`echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":%q,"content":"..."}}]}}'`+"\n", wrongQuestion) +
		"sleep 30\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID:          "propose",
		Name:        "Propose",
		Description: "interactive planning that violates dialog contract",
		Agents:      []flow.AgentType{flow.AgentPlanning},
		Interactive: true,
		Command:     scriptPath,
	}}

	store, err := state.Open(dir, []string{"propose"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(dir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  dir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	waitForStatus(t, stateFile, "propose", state.StatusFailed, 15*time.Second)

	// Причина отказа зафиксирована в events.jsonl.
	eventsData, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if !strings.Contains(string(eventsData), "dialog protocol violation") {
		t.Errorf("events.jsonl missing violation reason: %q", string(eventsData))
	}
}
```

- [x] **Step 2: Запустить тест — убедиться, что падает**

Run: `go test ./pkg/orchestrator/ -run TestIntegration_DialogViolationDetected -v`
Expected: FAIL — стадия не доходит до `failed` за 15с (детектора нет), `waitForStatus` фейлится по таймауту.

- [x] **Step 3: Добавить хелперы `detectDialogViolation` и `pathInside` — `pkg/orchestrator/orchestrator.go`**

Добавить в конец файла (рядом с `hasOpenQuestion`/`pollQuestions` логически уместно — можно сразу после `pollQuestions`):

```go
// detectDialogViolation scans the agent's stream-json logs (<phase>.jsonl) for a
// Write of a *.question.json file OUTSIDE the stage directory. Such a write
// violates the file-based dialog contract: the poller and dashboard only look
// inside stageDir, so a misplaced question hangs the stage forever. Returns a
// human-readable reason when a violation is found.
func detectDialogViolation(stageDir string) (string, bool) {
	for _, phase := range []string{phasePlanning, phaseImplementation, phaseReview} {
		jsonlPath := filepath.Join(stageDir, phase+".jsonl")
		for _, f := range executor.WrittenFiles(jsonlPath) {
			if !strings.HasSuffix(filepath.Base(f), ".question.json") {
				continue
			}
			if !pathInside(f, stageDir) {
				return fmt.Sprintf("dialog protocol violation: question written to %s, expected %s", f, stageDir), true
			}
		}
	}
	return "", false
}

// pathInside reports whether file is located inside dir (both resolved to
// absolute paths).
func pathInside(file, dir string) bool {
	absFile, err := filepath.Abs(file)
	if err != nil {
		absFile = filepath.Clean(file)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = filepath.Clean(dir)
	}
	if absDir != string(filepath.Separator) {
		absDir += string(filepath.Separator)
	}
	return strings.HasPrefix(absFile+string(filepath.Separator), absDir)
}
```

- [x] **Step 4: Вызвать детектор из `pollQuestions`**

В `pollQuestions` сразу после цикла `for _, q := range questions { ... }` (перед закрывающей скобкой `for stageID, st := range snap.Stages {`), добавить:
```go
			// No open question in stageDir: if this is an interactive stage, check
			// whether the agent wrote one elsewhere (dialog contract violation).
			// Fail fast with a clear reason instead of hanging forever (afm bug-2).
			if len(questions) == 0 {
				if stage := o.graph.Stage(stageID); stage != nil && stage.Interactive {
					if reason, ok := detectDialogViolation(stageDir); ok {
						o.FailStage(stageID, reason)
					}
				}
			}
```

- [x] **Step 5: Собрать, vet, тесты (всё покрытие интерактива)**

Run:
```bash
go build ./...
go vet ./...
go test ./pkg/orchestrator/ -count=1
```
Expected: build/vet чисто; новый тест PASS; существующие `TestFullDialogCycle`, `TestIntegration_PlanningWithOpenQuestionWaits` PASS (они пишут вопрос ВНУТРИ stageDir и/или не через Write-событие — ложных срабатываний нет).

- [x] **Step 6: Коммит**

```bash
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/integration_interactive_test.go
git commit -m "fix: детектор нарушения протокола диалога (fail-fast)"
```

---

## Финальная проверка (после всех задач)

- [x] **Полный прогон**

```bash
go build ./...
go vet ./...
go test ./... -count=1
```
Expected: всё чисто, все тесты PASS.

- [x] **Линт (если настроен)**

```bash
golangci-lint run ./... 2>/dev/null || echo "golangci-lint не установлен — пропускаем"
```
Expected: либо чисто, либо явное «не установлен».

- [x] **Smoke-проверка сценария из бага (вручную, опционально)**

В реальном проекте с интерактивной стадией:
1. `flowmanager run` — стадия доходит до интерактивной фазы, `planning.log`/`implementation.log` показывают прогресс, а не `exit status 1`.
2. При ошибке claude —诊断 видна в `<stageDir>/<phase>.stderr.log`.
3. `flowmanager retry <stage>` после отказа — стартует чистая сессия (нет «No conversation found»).
4. В `<runDir>/events.jsonl` при нарушении протокола — `dialog protocol violation`.

---

## Self-Review (проведён автором плана)

**Spec coverage:**
- A1 (`--verbose` + дедуп): Task 1. ✅
- A2 (stderr → файл): Task 2. ✅
- A3 (фантомный session — non-retryable ветка + onManualRetry страховка): Task 3. ✅
- B1 (усиление промпта: абсолютный путь + «nowhere else», заполнить `StageDir`): Task 4. ✅
- B2 (детектор-нарушения, fail-fast, guard `Interactive`, `pathInside`): Task 5. ✅
- config.example.yaml обновлён: Task 1, Step 5. ✅
- Навыки `goga-*` — вне репо (non-goal), в плане не трогаются. ✅

**Placeholder scan:** плейсхолдеров/TODO/TBD нет; в каждом код-степе — полный код.

**Type consistency:** `executor.DefaultClaudeArgs() []string` и `executor.ResolveArgs(extra []string) []string` определены в Task 1 и использованы там же (`New`, `runnerFor`); `detectDialogViolation(stageDir string) (string, bool)` и `pathInside(file, dir string) bool` — в Task 5; сигнатура `run(ctx, prompt, stderr io.Writer, lineCallback)` в Task 2 и оба call-сайта обновлены. Имена констант фаз (`phasePlanning/phaseImplementation/phaseReview`) совпадают с существующими в `orchestrator.go`.

**Замеченные трейд-оффы (из спеки):** A3 чистит сессию на любой non-retryable неудаче — принято (упавшая стадия реетраится чисто; resume-после-ответа использует сессию, пока стадия не failed). B2 читает `.jsonl` целиком каждый тик для активных интерактивных стадий без открытого вопроса — для типичных прогонов допустимо; оптимизация по byte-offset вынесена за рамки.
