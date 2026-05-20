# Agent Logging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Каждый агент пишет два файла: human-readable `.log` (старт/действия/финиш) и сырой `.jsonl` (stream-json от claude).

**Architecture:** Расширяем `progress.Logger` методами `LogStart/LogAction/LogEnd`. В `executor.RunAgent` и `RunPlanning` парсим stream-json события реального времени — tool_use блоки превращаем в читаемые строки действий. Сигнатуры `RunAgent`/`RunPlanning` получают `agentType` и `stageName`.

**Tech Stack:** Go 1.26, стандартная библиотека, без новых зависимостей.

---

## File Map

| Файл | Что меняем |
|---|---|
| `pkg/progress/progress.go` | + поле `startTime`, + методы `LogStart`, `LogAction`, `LogEnd` |
| `pkg/progress/progress_test.go` | + тесты на новые методы |
| `pkg/executor/executor.go` | + поля `Name`/`Input` в `streamContent`, + `toolInput` struct, + `parseToolAction()`, обновить `RunAgent`/`RunPlanning` сигнатуры и реализацию |
| `pkg/executor/executor_test.go` | обновить вызовы, + тесты на парсинг tool_use и наличие `.jsonl` |
| `pkg/orchestrator/orchestrator.go` | обновить 4 вызова `RunAgent`/`RunPlanning` |

---

### Task 1: Расширить `progress.Logger` — LogStart/LogAction/LogEnd

**Files:**
- Modify: `pkg/progress/progress.go`
- Modify: `pkg/progress/progress_test.go`

- [x] **Step 1: Написать падающие тесты**

Добавить в `pkg/progress/progress_test.go`:

```go
func TestLoggerStartEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	lg, err := progress.NewLogger(path)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	lg.LogStart("implementation", "auth-module")
	lg.LogAction("Write", "pkg/auth/jwt.go")
	lg.LogAction("Bash", "git commit -m \"feat: jwt\"")
	lg.LogEnd(nil)
	lg.Close()

	data, _ := os.ReadFile(path)
	content := string(data)

	if !strings.Contains(content, "implementation agent") {
		t.Errorf("missing agent type: %q", content)
	}
	if !strings.Contains(content, "auth-module") {
		t.Errorf("missing stage name: %q", content)
	}
	if !strings.Contains(content, "started:") {
		t.Errorf("missing start timestamp: %q", content)
	}
	if !strings.Contains(content, "Write") {
		t.Errorf("missing tool action: %q", content)
	}
	if !strings.Contains(content, "pkg/auth/jwt.go") {
		t.Errorf("missing action detail: %q", content)
	}
	if !strings.Contains(content, "completed") {
		t.Errorf("missing completion marker: %q", content)
	}
	if !strings.Contains(content, "duration:") {
		t.Errorf("missing duration: %q", content)
	}
}

func TestLoggerEndWithError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	lg, _ := progress.NewLogger(path)
	lg.LogStart("review", "auth-module")
	lg.LogEnd(errors.New("idle timeout after 30m"))
	lg.Close()

	data, _ := os.ReadFile(path)
	content := string(data)

	if !strings.Contains(content, "FAILED") {
		t.Errorf("missing FAILED marker: %q", content)
	}
	if !strings.Contains(content, "idle timeout after 30m") {
		t.Errorf("missing error message: %q", content)
	}
}
```

Добавить `"errors"` в импорты теста.

- [x] **Step 2: Убедиться что тесты падают**

```bash
cd /Users/alexander.kopichin/work/flowManager
go test ./pkg/progress/... -run TestLoggerStart -v
```

Ожидаем: FAIL — `lg.LogStart undefined`

- [x] **Step 3: Реализовать LogStart/LogAction/LogEnd в progress.go**

Заменить `pkg/progress/progress.go` целиком:

```go
package progress

import (
	"fmt"
	"os"
	"time"
)

// Logger writes timestamped messages to a file (append-only) and stdout.
type Logger struct {
	f         *os.File
	startTime time.Time
}

// NewLogger opens (or creates) a log file. If the file exists without a
// completion footer, a restart separator is appended.
func NewLogger(path string) (*Logger, error) {
	existing, _ := os.ReadFile(path)
	needsSeparator := len(existing) > 0

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	lg := &Logger{f: f, startTime: time.Now()}

	if needsSeparator {
		sep := fmt.Sprintf("\n--- restarted at %s ---\n", time.Now().Format("06-01-02 15:04:05"))
		lg.write(sep)
	}
	return lg, nil
}

// Log writes a timestamped line to the log file and stdout.
func (l *Logger) Log(msg string) {
	line := fmt.Sprintf("%s  %s\n", time.Now().Format("06-01-02 15:04:05"), msg)
	l.write(line)
	fmt.Print(line)
}

// LogStart writes a start banner with agent type, stage name, and timestamp.
func (l *Logger) LogStart(agentType, stageName string) {
	line := fmt.Sprintf("=== %s agent | stage: %s | started: %s ===\n",
		agentType, stageName, l.startTime.Format("2006-01-02 15:04:05"))
	l.write(line)
	fmt.Print(line)
}

// LogAction writes a timestamped action line (tool name + detail).
func (l *Logger) LogAction(toolName, detail string) {
	line := fmt.Sprintf("%s  %-6s  %s\n",
		time.Now().Format("15:04:05"), toolName, detail)
	l.write(line)
	fmt.Print(line)
}

// LogEnd writes a completion or failure banner with elapsed duration.
func (l *Logger) LogEnd(err error) {
	duration := time.Since(l.startTime).Round(time.Second)
	var line string
	if err == nil {
		line = fmt.Sprintf("=== completed | %s | duration: %s ===\n",
			time.Now().Format("2006-01-02 15:04:05"), duration)
	} else {
		line = fmt.Sprintf("=== FAILED | %s | duration: %s | %s ===\n",
			time.Now().Format("2006-01-02 15:04:05"), duration, err)
	}
	l.write(line)
	fmt.Print(line)
}

func (l *Logger) write(s string) {
	l.f.WriteString(s) //nolint:errcheck
}

// Close closes the underlying file.
func (l *Logger) Close() error {
	return l.f.Close()
}

// AppendRaw appends raw bytes to the log (used for streaming agent output).
func (l *Logger) AppendRaw(data []byte) {
	l.f.Write(data) //nolint:errcheck
}

// Lock is a file-based exclusive lock.
type Lock struct {
	path string
	f    *os.File
}

// NewLock creates a Lock handle for the given path (does not acquire).
func NewLock(path string) (*Lock, error) {
	return &Lock{path: path}, nil
}

// IsLocked returns true if another process holds the lock.
func IsLocked(path string) bool {
	l := &Lock{path: path}
	err := l.TryLock()
	if err != nil {
		return true
	}
	l.Unlock()
	return false
}
```

- [x] **Step 4: Прогнать тесты**

```bash
go test ./pkg/progress/... -v
```

Ожидаем: все тесты PASS

- [x] **Step 5: Коммит**

```bash
git add pkg/progress/progress.go pkg/progress/progress_test.go
git commit -m "feat: добавить LogStart/LogAction/LogEnd в progress.Logger"
```

---

### Task 2: Расширить парсинг stream-json в executor — поддержка tool_use

**Files:**
- Modify: `pkg/executor/executor.go`

- [x] **Step 1: Написать падающий тест на parseToolAction**

Добавить в `pkg/executor/executor_test.go`:

```go
func TestParseToolAction(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantTool   string
		wantDetail string
		wantOK     bool
	}{
		{
			name: "write tool",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"pkg/auth/jwt.go","content":"..."}}]}}`,
			wantTool:   "Write",
			wantDetail: "pkg/auth/jwt.go",
			wantOK:     true,
		},
		{
			name: "bash tool",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git commit -m \"feat: jwt\""}}]}}`,
			wantTool:   "Bash",
			wantDetail: "git commit -m \"feat: jwt\"",
			wantOK:     true,
		},
		{
			name: "read tool",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"pkg/flow/flow.go"}}]}}`,
			wantTool:   "Read",
			wantDetail: "pkg/flow/flow.go",
			wantOK:     true,
		},
		{
			name: "text content",
			line: `{"type":"assistant","message":{"content":[{"type":"text","text":"I will implement the auth module now"}]}}`,
			wantTool:   "text",
			wantDetail: "I will implement the auth module now",
			wantOK:     true,
		},
		{
			name:   "result event ignored",
			line:   `{"type":"result","subtype":"success"}`,
			wantOK: false,
		},
		{
			name:   "non-json ignored",
			line:   "not json",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tool, detail, ok := executor.ParseToolAction(tc.line)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if tool != tc.wantTool {
				t.Errorf("tool=%q, want %q", tool, tc.wantTool)
			}
			if !strings.Contains(detail, tc.wantDetail) {
				t.Errorf("detail=%q, want to contain %q", detail, tc.wantDetail)
			}
		})
	}
}
```

- [x] **Step 2: Убедиться что тест падает**

```bash
go test ./pkg/executor/... -run TestParseToolAction -v
```

Ожидаем: FAIL — `executor.ParseToolAction undefined`

- [x] **Step 3: Расширить структуры и добавить ParseToolAction в executor.go**

Заменить блок типов в `pkg/executor/executor.go` (строки 45–59):

```go
type streamContent struct {
	Type  string          `json:"type"`  // "text" or "tool_use"
	Text  string          `json:"text"`  // for type=="text"
	Name  string          `json:"name"`  // for type=="tool_use"
	Input json.RawMessage `json:"input"` // for type=="tool_use"
}

type streamMessage struct {
	Content []streamContent `json:"content"`
}

// streamEvent is a minimal representation of a claude stream-json event.
type streamEvent struct {
	Type    string         `json:"type"`
	Subtype string         `json:"subtype"`
	Message *streamMessage `json:"message"`
}

// toolInput holds the subset of tool call input fields we care about.
type toolInput struct {
	FilePath string `json:"file_path"`
	Path     string `json:"path"`
	Command  string `json:"command"`
}
```

Добавить функцию `ParseToolAction` после блока типов:

```go
// ParseToolAction parses a single stream-json line and returns a human-readable
// tool name and detail. Returns ok=false for events we don't log (result, system, etc.).
func ParseToolAction(line string) (toolName, detail string, ok bool) {
	var ev streamEvent
	if json.Unmarshal([]byte(line), &ev) != nil {
		return "", "", false
	}
	if ev.Type != "assistant" || ev.Message == nil {
		return "", "", false
	}
	for _, c := range ev.Message.Content {
		switch c.Type {
		case "text":
			if c.Text == "" {
				continue
			}
			d := c.Text
			if len(d) > 100 {
				d = d[:100] + "..."
			}
			return "text", d, true
		case "tool_use":
			var inp toolInput
			json.Unmarshal(c.Input, &inp) //nolint:errcheck
			fp := inp.FilePath
			if fp == "" {
				fp = inp.Path
			}
			switch c.Name {
			case "Write", "Edit", "Read", "Glob", "Grep":
				return c.Name, fp, true
			case "Bash":
				cmd := inp.Command
				if len(cmd) > 80 {
					cmd = cmd[:80] + "..."
				}
				return "Bash", cmd, true
			default:
				d := fp
				if d == "" {
					d = inp.Command
				}
				if d == "" {
					d = string(c.Input)
					if len(d) > 80 {
						d = d[:80] + "..."
					}
				}
				return c.Name, d, true
			}
		}
	}
	return "", "", false
}
```

- [x] **Step 4: Прогнать тест**

```bash
go test ./pkg/executor/... -run TestParseToolAction -v
```

Ожидаем: PASS

- [x] **Step 5: Коммит**

```bash
git add pkg/executor/executor.go pkg/executor/executor_test.go
git commit -m "feat: ParseToolAction — парсинг tool_use из claude stream-json"
```

---

### Task 3: Обновить RunAgent — новая сигнатура + двойное логирование

**Files:**
- Modify: `pkg/executor/executor.go`
- Modify: `pkg/executor/executor_test.go`

- [x] **Step 1: Написать падающий тест на новую сигнатуру RunAgent**

Заменить `TestRunAgentLogsOutput` в `pkg/executor/executor_test.go`:

```go
func TestRunAgentLogsOutput(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "impl.log")

	ex := executor.New(executor.Config{
		Command: "bash",
		ExtraArgs: []string{"-c",
			`echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"pkg/foo.go"}}]}}'
echo '{"type":"result","subtype":"success"}'`},
		IdleTimeout: 5 * time.Second,
	})

	err := ex.RunAgent(context.Background(), "implementation", "auth-module", "implement the plan", logFile)
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	// human-readable log
	data, _ := os.ReadFile(logFile)
	content := string(data)
	if !strings.Contains(content, "implementation agent") {
		t.Errorf("log missing agent type: %q", content)
	}
	if !strings.Contains(content, "auth-module") {
		t.Errorf("log missing stage name: %q", content)
	}
	if !strings.Contains(content, "Write") {
		t.Errorf("log missing tool action: %q", content)
	}
	if !strings.Contains(content, "pkg/foo.go") {
		t.Errorf("log missing file detail: %q", content)
	}
	if !strings.Contains(content, "completed") {
		t.Errorf("log missing completion: %q", content)
	}

	// raw jsonl
	jsonlFile := strings.TrimSuffix(logFile, ".log") + ".jsonl"
	raw, _ := os.ReadFile(jsonlFile)
	if !strings.Contains(string(raw), `"type":"assistant"`) {
		t.Errorf("jsonl missing raw events: %q", string(raw))
	}
}
```

Обновить `TestIdleTimeout` (новая сигнатура):

```go
func TestIdleTimeout(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "impl.log")

	ex := executor.New(executor.Config{
		Command:     "bash",
		ExtraArgs:   []string{"-c", "sleep 10"},
		IdleTimeout: 100 * time.Millisecond,
	})

	err := ex.RunAgent(context.Background(), "implementation", "test-stage", "do work", logFile)
	if err == nil {
		t.Error("expected timeout error, got nil")
	}

	// log должен содержать FAILED
	data, _ := os.ReadFile(logFile)
	if !strings.Contains(string(data), "FAILED") {
		t.Errorf("log should contain FAILED on timeout: %q", string(data))
	}
}
```

- [x] **Step 2: Убедиться что тесты падают**

```bash
go test ./pkg/executor/... -run "TestRunAgent|TestIdleTimeout" -v
```

Ожидаем: FAIL — компиляция не прошла (неверная сигнатура)

- [x] **Step 3: Обновить RunAgent в executor.go**

Заменить метод `RunAgent` (строки 93–103):

```go
// RunAgent runs the AI client with prompt via stdin, writing human-readable
// actions to logFile and raw stream-json to logFile with .jsonl extension.
func (e *Executor) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	lg, err := progress.NewLogger(logFile)
	if err != nil {
		return err
	}
	defer lg.Close()

	jsonlFile := strings.TrimSuffix(logFile, ".log") + ".jsonl"
	jf, err := os.OpenFile(jsonlFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open jsonl file: %w", err)
	}
	defer jf.Close()

	lg.LogStart(agentType, stageName)

	runErr := e.run(ctx, prompt, func(line string) {
		jf.WriteString(line + "\n") //nolint:errcheck
		if tool, detail, ok := ParseToolAction(line); ok {
			lg.LogAction(tool, detail)
		}
	})

	lg.LogEnd(runErr)
	return runErr
}
```

- [x] **Step 4: Прогнать тесты**

```bash
go test ./pkg/executor/... -v
```

Ожидаем: все тесты PASS (кроме тех, что ещё используют старую сигнатуру RunPlanning — они тронуты в Task 4)

- [x] **Step 5: Коммит**

```bash
git add pkg/executor/executor.go pkg/executor/executor_test.go
git commit -m "feat: RunAgent — двойное логирование (.log + .jsonl) с парсингом tool_use"
```

---

### Task 4: Обновить RunPlanning — новая сигнатура + двойное логирование

**Files:**
- Modify: `pkg/executor/executor.go`
- Modify: `pkg/executor/executor_test.go`

- [x] **Step 1: Написать падающий тест**

Заменить `TestRunPlanningCapturesOutput` в `pkg/executor/executor_test.go`:

```go
func TestRunPlanningCapturesOutput(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "plan.md")
	logFile := filepath.Join(dir, "planning.log")

	ex := executor.New(executor.Config{
		Command: "bash",
		ExtraArgs: []string{"-c",
			`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"# Plan\n\nstep 1"}]}}'
echo '{"type":"result","subtype":"success"}'`},
		IdleTimeout: 5 * time.Second,
	})

	err := ex.RunPlanning(context.Background(), "auth-module", "generate a plan", outFile, logFile)
	if err != nil {
		t.Fatalf("RunPlanning: %v", err)
	}

	// plan.md содержит текст
	data, _ := os.ReadFile(outFile)
	if !strings.Contains(string(data), "Plan") {
		t.Errorf("plan output missing: %q", string(data))
	}

	// .log содержит баннер
	logData, _ := os.ReadFile(logFile)
	logContent := string(logData)
	if !strings.Contains(logContent, "planning agent") {
		t.Errorf("log missing planning agent: %q", logContent)
	}
	if !strings.Contains(logContent, "auth-module") {
		t.Errorf("log missing stage name: %q", logContent)
	}
	if !strings.Contains(logContent, "completed") {
		t.Errorf("log missing completion: %q", logContent)
	}

	// .jsonl существует
	jsonlFile := strings.TrimSuffix(logFile, ".log") + ".jsonl"
	raw, _ := os.ReadFile(jsonlFile)
	if len(raw) == 0 {
		t.Error("jsonl file should not be empty")
	}
}
```

- [x] **Step 2: Убедиться что тест падает**

```bash
go test ./pkg/executor/... -run TestRunPlanning -v
```

Ожидаем: FAIL — неверная сигнатура

- [x] **Step 3: Обновить RunPlanning в executor.go**

Заменить метод `RunPlanning` (строки 62–90):

```go
// RunPlanning runs the AI client with prompt via stdin, collects text output
// into outFile, writes human-readable log to logFile, and raw stream to logFile+".jsonl".
func (e *Executor) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	lg, err := progress.NewLogger(logFile)
	if err != nil {
		return err
	}
	defer lg.Close()

	jsonlFile := strings.TrimSuffix(logFile, ".log") + ".jsonl"
	jf, err := os.OpenFile(jsonlFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open jsonl file: %w", err)
	}
	defer jf.Close()

	lg.LogStart("planning", stageName)

	var textBuf strings.Builder
	runErr := e.run(ctx, prompt, func(line string) {
		jf.WriteString(line + "\n") //nolint:errcheck
		var ev streamEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			return
		}
		if ev.Type == "assistant" && ev.Message != nil {
			for _, c := range ev.Message.Content {
				if c.Type == "text" {
					textBuf.WriteString(c.Text)
				}
			}
		}
		if tool, detail, ok := ParseToolAction(line); ok {
			lg.LogAction(tool, detail)
		}
	})

	lg.LogEnd(runErr)
	if runErr != nil {
		return runErr
	}
	return os.WriteFile(outFile, []byte(textBuf.String()), 0644)
}
```

- [x] **Step 4: Прогнать все тесты executor**

```bash
go test ./pkg/executor/... -v
```

Ожидаем: все PASS

- [x] **Step 5: Коммит**

```bash
git add pkg/executor/executor.go pkg/executor/executor_test.go
git commit -m "feat: RunPlanning — двойное логирование (.log + .jsonl)"
```

---

### Task 5: Обновить вызовы в orchestrator.go

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go`

- [x] **Step 1: Проверить что orchestrator не компилируется**

```bash
go build ./pkg/orchestrator/...
```

Ожидаем: ошибки компиляции — неверные сигнатуры RunAgent/RunPlanning

- [x] **Step 2: Обновить runPlanningAgent**

В `pkg/orchestrator/orchestrator.go` строка 114, заменить:

```go
if err := o.exec.RunPlanning(ctx, prompt, outFile, logFile); err != nil {
```

на:

```go
if err := o.exec.RunPlanning(ctx, s.Name, prompt, outFile, logFile); err != nil {
```

- [x] **Step 3: Обновить runImplementationAgent**

Строка 230, заменить:

```go
if err := o.exec.RunAgent(ctx, prompt, logFile); err != nil {
```

на:

```go
if err := o.exec.RunAgent(ctx, "implementation", s.Name, prompt, logFile); err != nil {
```

Строка 235, заменить:

```go
if err := o.exec.RunAgent(ctx, reviewPrompt, reviewLog); err != nil {
```

на:

```go
if err := o.exec.RunAgent(ctx, "review", s.Name, reviewPrompt, reviewLog); err != nil {
```

- [x] **Step 4: Обновить SummaryPhase**

Строка 248, заменить:

```go
return o.exec.RunAgent(ctx, prompt, logFile)
```

на:

```go
return o.exec.RunAgent(ctx, "summary", "all stages", prompt, logFile)
```

- [x] **Step 5: Убедиться что всё компилируется и тесты проходят**

```bash
go build ./...
go test ./...
```

Ожидаем: компиляция OK, все тесты PASS

- [x] **Step 6: Финальный коммит**

```bash
git add pkg/orchestrator/orchestrator.go
git commit -m "feat: обновить orchestrator — передавать agentType и stageName в executor"
```
