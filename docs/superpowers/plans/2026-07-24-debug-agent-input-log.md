# `afm --debug` — лог входа агента — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Флаг `afm --debug` (+ env `AFM_DEBUG`) пишет точный промпт, уходящий в агента, с таймстампом и привязкой stage/phase в `<run>/debug.log` и по-стейджно `<run>/<stage>/<phase>.prompt.log`.

**Architecture:** Точка врезки — `pkg/executor`: `run()` (planning/impl/review/autonomous/reprompt) и `RunJSONQuery()` (supervisor) зовут `logAgentInput(phase, prompt)`. Включение прокидывается флагом через `orchestrator.Options.Debug` и `executor.Config.Debug`+`RunDir`. Best-effort запись — ошибки не валят run. Off by default.

**Tech Stack:** Go, cobra (CLI), существующий executor/orchestrator/docker.

**Спека:** `docs/superpowers/specs/2026-07-24-debug-agent-input-log-design.md`.

## Global Constraints

- Не менять версию go в go.mod. Коммиты на русском, без Co-Authored-By. Не пушить.
- Off by default; логируем только вход (промпт), не выход, не env/секреты.
- Best-effort: ошибка записи debug-лога НЕ прерывает run (только предупреждение в stderr).
- Формат записи (оба файла идентичны):
  ```
  === [<RFC3339Nano UTC>] stage=<id> phase=<phase> cmd=<command> session=<id> resume=<bool> ===
  --- BEGIN PROMPT ---
  <prompt>
  --- END PROMPT ---

  ```
- Пре-коммит хук гоняет make lint+build+test; дерево чистое после коммита. Go-only фича — бандл дашборда не меняется.

## File Structure

- `pkg/executor/executor.go` — `Config.Debug`/`Config.RunDir`; параметр `phase` в `run`; `logAgentInput`/`appendDebug`; вызовы из `run` и `RunJSONQuery`.
- `pkg/orchestrator/orchestrator.go` — `Options.Debug`.
- `pkg/orchestrator/runner_factory.go` — проброс `Debug`/`RunDir` в 3 `executor.New`.
- `cmd/afm/main.go` — persistent `--debug`, `resolveDebug`, резолв в `PersistentPreRunE` + `os.Setenv("AFM_DEBUG","1")`.
- `cmd/afm/run.go` — `Options{Debug: debugEnabled}` + `supervisorRunner` c `Debug`/`RunDir`.
- `pkg/docker/launcher.go` — passthrough `-e AFM_DEBUG`.
- `README.md` — раздел про `--debug`.

---

### Task 1: executor — запись входа агента

**Files:**
- Modify: `pkg/executor/executor.go`
- Test: `pkg/executor/executor_test.go` (или новый `debug_test.go` в пакете `executor`)

**Interfaces:**
- Produces: `executor.Config{Debug bool, RunDir string}`; `run` теперь принимает `phase string`; метод `logAgentInput(phase, prompt string)`.
- Consumes: ничего нового.

- [ ] **Step 1: Добавить поля в `Config`**

В `type Config struct` (после `Dir string`) добавить:

```go
	Debug          bool                      // if true, log the exact agent input (prompt) to debug logs
	RunDir         string                    // run directory root; with Debug, <RunDir>/debug.log gets every agent input
```

- [ ] **Step 2: Добавить `logAgentInput` + `appendDebug`**

Добавить методы (например, в конец файла). Требуются импорты `time`, `path/filepath`, `os`, `fmt` — часть уже есть; недостающие добавить.

```go
// logAgentInput пишет точный промпт, уходящий в агента (stdin), в debug-логи —
// единый <RunDir>/debug.log (хронологически по всем стадиям) и по-стейджно
// <StageDir>/<phase>.prompt.log. Активно только при Config.Debug. Best-effort:
// ошибки записи не прерывают run (debug — вспомогательный тракт).
func (e *Executor) logAgentInput(phase, prompt string) {
	if !e.cfg.Debug {
		return
	}
	stage := ""
	if e.cfg.StageDir != "" {
		stage = filepath.Base(e.cfg.StageDir)
	}
	entry := fmt.Sprintf(
		"=== [%s] stage=%s phase=%s cmd=%s session=%s resume=%t ===\n--- BEGIN PROMPT ---\n%s\n--- END PROMPT ---\n\n",
		time.Now().UTC().Format(time.RFC3339Nano), stage, phase, e.cfg.Command, e.cfg.SessionID, e.cfg.Resume, prompt,
	)
	if e.cfg.RunDir != "" {
		appendDebug(filepath.Join(e.cfg.RunDir, "debug.log"), entry)
	}
	if e.cfg.StageDir != "" {
		appendDebug(filepath.Join(e.cfg.StageDir, phase+".prompt.log"), entry)
	}
}

// appendDebug дописывает строку в файл (создаёт при отсутствии). Ошибки —
// в stderr, без прерывания.
func appendDebug(path, s string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "debug: cannot open %s: %v\n", path, err)
		return
	}
	defer f.Close() //nolint:errcheck
	if _, err := f.WriteString(s); err != nil {
		fmt.Fprintf(os.Stderr, "debug: cannot write %s: %v\n", path, err)
	}
}
```

- [ ] **Step 3: Прокинуть `phase` в `run` и вызвать логгер**

Изменить сигнатуру `run` (добавить `phase string`) и в начале тела вызвать логгер:

```go
func (e *Executor) run(ctx context.Context, prompt, phase string, stderr io.Writer, lineCallback func(string)) error {
	e.logAgentInput(phase, prompt)
	args := append([]string{}, e.cfg.ExtraArgs...)
	// ... остальное без изменений ...
}
```

Обновить оба call-site `run` (они внутри `RunPlanning` и `RunAgent`), вычислив phase из basename `logFile`. В `RunPlanning` (перед вызовом `e.run(...)`):

```go
	phase := strings.TrimSuffix(filepath.Base(logFile), filepath.Ext(logFile))
	runErr := e.run(ctx, prompt, phase, stderr, func(line string) {
		jf.WriteString(line + "\n") //nolint:errcheck
		// ... тело без изменений ...
```

В `RunAgent` — аналогично: перед `e.run(...)` вычислить `phase := strings.TrimSuffix(filepath.Base(logFile), filepath.Ext(logFile))` и передать `e.run(ctx, prompt, phase, stderr, ...)`. (Тело коллбэков не менять.)

`strings` и `path/filepath` уже импортированы в файле (используются в `run`).

- [ ] **Step 4: Логировать вход супервизора в `RunJSONQuery`**

`RunJSONQuery` не проходит через `run`, поэтому вызвать логгер вручную первой строкой тела:

```go
func (e *Executor) RunJSONQuery(ctx context.Context, prompt string) ([]byte, error) {
	e.logAgentInput("supervisor", prompt)
	// ... остальное без изменений ...
}
```

- [ ] **Step 5: Тесты executor**

Добавить тест(ы) в пакет `executor` (файл `debug_test.go`). Не требуется реальный агент — тестируем `logAgentInput` напрямую.

```go
package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogAgentInput_WritesBothFiles(t *testing.T) {
	run := t.TempDir()
	stage := filepath.Join(run, "brainstorm")
	if err := os.Mkdir(stage, 0755); err != nil {
		t.Fatal(err)
	}
	e := &Executor{cfg: Config{Debug: true, Command: "claude", RunDir: run, StageDir: stage, SessionID: "s1"}}

	e.logAgentInput("planning", "HELLO PROMPT")
	e.logAgentInput("planning", "SECOND PROMPT")

	debugLog, err := os.ReadFile(filepath.Join(run, "debug.log"))
	if err != nil {
		t.Fatalf("debug.log: %v", err)
	}
	got := string(debugLog)
	for _, want := range []string{"stage=brainstorm", "phase=planning", "cmd=claude", "session=s1", "--- BEGIN PROMPT ---", "HELLO PROMPT", "SECOND PROMPT"} {
		if !strings.Contains(got, want) {
			t.Errorf("debug.log missing %q; got:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "BEGIN PROMPT"); n != 2 {
		t.Errorf("expected 2 appended entries, got %d", n)
	}

	perStage, err := os.ReadFile(filepath.Join(stage, "planning.prompt.log"))
	if err != nil {
		t.Fatalf("planning.prompt.log: %v", err)
	}
	if !strings.Contains(string(perStage), "HELLO PROMPT") {
		t.Errorf("per-stage log missing prompt; got:\n%s", string(perStage))
	}
}

func TestLogAgentInput_DisabledWritesNothing(t *testing.T) {
	run := t.TempDir()
	e := &Executor{cfg: Config{Debug: false, Command: "claude", RunDir: run}}
	e.logAgentInput("planning", "X")
	if _, err := os.Stat(filepath.Join(run, "debug.log")); !os.IsNotExist(err) {
		t.Errorf("debug.log должен отсутствовать при Debug=false")
	}
}

func TestLogAgentInput_NoStageDirOnlyDebugLog(t *testing.T) {
	run := t.TempDir()
	e := &Executor{cfg: Config{Debug: true, Command: "glm51", RunDir: run}} // StageDir пуст (supervisor)
	e.logAgentInput("supervisor", "SUP PROMPT")
	if _, err := os.Stat(filepath.Join(run, "debug.log")); err != nil {
		t.Errorf("debug.log должен существовать: %v", err)
	}
}
```

(Проверить, что поле `cfg` и тип `Executor` доступны внутри пакета — тест в `package executor`. Если конструктор `New` обязателен по стилю пакета, использовать `New(Config{...})` вместо литерала `&Executor{cfg:...}` — сверить с существующими тестами пакета и выбрать совместимый способ.)

- [ ] **Step 6: Проверка + коммит**

```bash
cd /Users/alexander.kopichin/work/personal/afm && go build ./... && go test ./pkg/executor/... -run 'TestLogAgentInput|TestRun|TestExecutor' -count=1
```
Expected: сборка чистая; новые тесты + существующие тесты executor зелёные.

```bash
git add pkg/executor/executor.go pkg/executor/debug_test.go
git commit -m "feat(executor): лог входа агента в debug-логи (Config.Debug/RunDir)"
```

---

### Task 2: Проводка флага `--debug` → orchestrator → executor

**Files:**
- Modify: `cmd/afm/main.go`, `cmd/afm/run.go`, `pkg/orchestrator/orchestrator.go`, `pkg/orchestrator/runner_factory.go`
- Test: `cmd/afm/main_test.go` (или существующий) — `resolveDebug`

**Interfaces:**
- Consumes: `executor.Config.Debug`/`RunDir` (Task 1).
- Produces: package-var `debugEnabled` (cmd/afm), `orchestrator.Options.Debug`.

- [ ] **Step 1: Флаг `--debug` + резолвер в `cmd/afm/main.go`**

Рядом с `var rootDir string` добавить:
```go
var debugEnabled bool
```
Добавить резолвер (рядом с `resolveRootDir`):
```go
// resolveDebug: флаг --debug важнее env AFM_DEBUG (1/true/yes/on).
func resolveDebug(flag bool, env string) bool {
	if flag {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
```
(Добавить импорт `strings`, если отсутствует.)

В `PersistentPreRunE` (после резолва rootDir):
```go
			rootDir = resolveRootDir(rootDir, os.Getenv("AFM_DIR"))
			debugEnabled = resolveDebug(debugEnabled, os.Getenv("AFM_DEBUG"))
			if debugEnabled {
				// чтобы re-exec внутри Docker тоже логировал (launcher прокидывает AFM_DEBUG)
				_ = os.Setenv("AFM_DEBUG", "1")
			}
			return nil
```
Зарегистрировать флаг рядом с `--dir`:
```go
	root.PersistentFlags().BoolVar(&debugEnabled, "debug", false, "log exact agent input (prompt) to <run>/debug.log and per-stage <phase>.prompt.log (env: AFM_DEBUG)")
```

- [ ] **Step 2: `Options.Debug` в orchestrator**

В `pkg/orchestrator/orchestrator.go`, в `type Options struct`, добавить поле:
```go
	Debug bool // if true, executors log the exact agent input to debug logs
```

- [ ] **Step 3: Проброс в `runner_factory.go` (3 вызова `executor.New`)**

В каждом из трёх `executor.New(executor.Config{...})` (и в ветке, где `cfg` собирается перед `executor.New(cfg)`) добавить:
```go
		Debug:          o.opts.Debug,
		RunDir:         o.opts.RunDir,
```
Для ветки с переменной `cfg` (около строки 44–54): установить `cfg.Debug = o.opts.Debug` и `cfg.RunDir = o.opts.RunDir` там же, где ставится `cfg.StageDir`.

- [ ] **Step 4: `run.go` — Options.Debug + supervisorRunner**

В `cmd/afm/run.go`:
- В `executor.New(executor.Config{...})` для `supervisorRunner` добавить `Debug: debugEnabled, RunDir: runDir`.
- В `orchestrator.New(orchestrator.Options{...})` добавить `Debug: debugEnabled`.

- [ ] **Step 5: Тест резолвера**

В `cmd/afm/main_test.go` (создать, если нет; package совпадает с main.go):
```go
func TestResolveDebug(t *testing.T) {
	cases := []struct {
		flag bool
		env  string
		want bool
	}{
		{true, "", true},
		{true, "0", true},   // флаг важнее env
		{false, "1", true},
		{false, "true", true},
		{false, "ON", true},
		{false, "", false},
		{false, "nope", false},
	}
	for _, c := range cases {
		if got := resolveDebug(c.flag, c.env); got != c.want {
			t.Errorf("resolveDebug(%v,%q)=%v want %v", c.flag, c.env, got, c.want)
		}
	}
}
```

- [ ] **Step 6: Проверка + коммит**

```bash
cd /Users/alexander.kopichin/work/personal/afm && go build ./... && go test ./cmd/... ./pkg/orchestrator/... -count=1
```
Expected: сборка чистая; тесты зелёные (новый TestResolveDebug + существующие).

```bash
git add cmd/afm/main.go cmd/afm/run.go cmd/afm/main_test.go pkg/orchestrator/orchestrator.go pkg/orchestrator/runner_factory.go
git commit -m "feat(cli): флаг --debug (+AFM_DEBUG) прокинут в executor через Options"
```

---

### Task 3: Docker passthrough + README

**Files:**
- Modify: `pkg/docker/launcher.go`, `README.md`

**Interfaces:**
- Consumes: `AFM_DEBUG` env (выставляется в `PersistentPreRunE` при `--debug`).

- [ ] **Step 1: Passthrough `AFM_DEBUG` в контейнер**

В `pkg/docker/launcher.go`, в блоке формирования `args` рядом с `-e AFM_IN_DOCKER=1` (после блока AFM_HOST_UID/GID), добавить проброс значением (AFM_DEBUG — не секрет):
```go
	if os.Getenv("AFM_DEBUG") != "" {
		args = append(args, "-e", "AFM_DEBUG="+os.Getenv("AFM_DEBUG"))
	}
```

- [ ] **Step 2: README — задокументировать флаг**

В `README.md` в раздел про запуск/конфиг добавить краткий пункт (рядом с описанием `--dir`), например:
```markdown
### Debugging: `--debug`

Run with `--debug` (or `AFM_DEBUG=1`) to log the **exact prompt sent to each agent** (stdin), with timestamps and stage/phase tags:
- `.afm/runs/<run>/debug.log` — one chronological log across all stages/phases;
- `.afm/runs/<run>/<stage>/<phase>.prompt.log` — per-stage/phase (appends across retries).

Off by default. The logs contain full project context passed to the agent (not secrets/env) — they live under `.afm/runs/` and aren't committed. Only the input is logged; agent output is already in `<phase>.jsonl`/`.log`.
```
(Вписать в существующую структуру README — при наличии оглавления/TOC добавить пункт.)

- [ ] **Step 3: Проверка + коммит**

```bash
cd /Users/alexander.kopichin/work/personal/afm && go build ./... && go test ./pkg/docker/... -count=1
```
Expected: сборка чистая; тесты docker зелёные.

```bash
git add pkg/docker/launcher.go README.md
git commit -m "feat(docker): проброс AFM_DEBUG в контейнер + README про --debug"
```

---

## Self-Review

**Spec coverage:**
- Флаг `--debug` + env AFM_DEBUG, флаг>env, PersistentPreRunE → Task 2 Steps 1. ✓
- Формат записи (заголовок + BEGIN/END PROMPT) → Task 1 Step 2. ✓
- Оба файла (единый debug.log + по-стейджно) → Task 1 Step 2. ✓
- Точки врезки run + RunJSONQuery → Task 1 Steps 3–4. ✓
- phase из basename logFile; supervisor литерал → Task 1 Steps 3–4. ✓
- Проводка Options.Debug + executor.Config + runner_factory + supervisorRunner → Task 2 Steps 2–4. ✓
- Docker AFM_DEBUG → Task 3 Step 1. ✓
- Best-effort (ошибки не валят run) → Task 1 Step 2 (appendDebug). ✓
- Off by default → флаг default false, env пуст. ✓
- Тесты (executor + resolveDebug) → Task 1 Step 5, Task 2 Step 5. ✓
- README → Task 3 Step 2. ✓

**Placeholder scan:** конкретный код во всех шагах; команды и ожидаемый вывод заданы. Единственное «сверить со стилем пакета» (Task 1 Step 5, конструктор `New` vs литерал) — это явная инструкция выбрать совместимый вариант, не заглушка.

**Type/consistency:** `Config.Debug`/`RunDir` (Task 1) ↔ проброс (Task 2 Steps 3–4); `run(ctx, prompt, phase, stderr, cb)` — сигнатура и оба call-site согласованы (Task 1 Step 3); `logAgentInput(phase, prompt)` зовётся из run и RunJSONQuery; `debugEnabled` (cmd) → `Options.Debug` → `Config.Debug`. Формат заголовка идентичен в спеке и коде.
