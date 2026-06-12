# Interactive Stage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Реализовать диалоговый стейдж в flowManager — флаг `interactive: true` на стейдже включает MCP-tool `ask_user`, агент может задавать вопросы пользователю через веб-UI, диалог переживает рестарт через replay из `<phase>.dialog.jsonl`.

**Architecture:** Встроенный HTTP MCP-сервер внутри dashboard-сервера, один tool `ask_user` с idempotent-replay по id, per-phase persistence в `.flowManager/runs/<run>/<stage>/{<phase>.dialog.jsonl, <phase>.session.json}`, новый статус `awaiting_user_input` в FSM, resume агентов через `claude --resume <session-id>`, секция «Диалог» в detail-panel UI.

**Tech Stack:** Go 1.x (текущая версия — НЕ менять в go.mod), стандартная библиотека + `gorilla/websocket` (уже подключён), `gopkg.in/yaml.v3` (уже подключён). MCP HTTP-transport — минимальная ручная имплементация JSON-RPC, либо `github.com/mark3labs/mcp-go` если стабилен (см. Task 1). Frontend: vanilla JS, никаких npm-зависимостей.

**Reference spec:** `docs/superpowers/specs/2026-06-08-interactive-stage-design.md`

---

## Pre-implementation: Spike

### Task 1: Проверить CLI claude и выбрать MCP-библиотеку

**Files:** (none — manual research)

- [x] **Step 1: Проверить флаги claude CLI**

Запустить локально:
```bash
claude --help | grep -E "(session|resume|mcp)" -A 2
```

Зафиксировать в комментарии к Task 7:
- Точное имя флага для задания session-id (`--session-id`? `--session`?)
- Точное имя флага для резюме (`--resume`? `-c`? `--continue`?)
- Совместимость с `--print` (headless) режимом — обязательно, иначе fallback на текстовый replay
- Точное имя/формат флага для MCP конфига (`--mcp-config`? `--mcp`?)

- [x] **Step 2: Проверить формат MCP HTTP-config для claude**

Создать тестовый файл `/tmp/test-mcp.json`:
```json
{
  "mcpServers": {
    "test": {
      "type": "http",
      "url": "http://127.0.0.1:9999/mcp"
    }
  }
}
```

В отдельном терминале поднять простейший python http-сервер на 9999 для логирования запросов:
```bash
python3 -c "
from http.server import HTTPServer, BaseHTTPRequestHandler
class H(BaseHTTPRequestHandler):
    def do_POST(self):
        l = int(self.headers.get('Content-Length','0'))
        body = self.rfile.read(l).decode()
        print('POST', self.path, body)
        self.send_response(200); self.send_header('Content-Type','application/json'); self.end_headers()
        self.wfile.write(b'{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}')
    def do_GET(self):
        print('GET', self.path)
        self.send_response(200); self.end_headers()
HTTPServer(('127.0.0.1',9999), H).serve_forever()
"
```

Запустить:
```bash
echo "hi" | claude --print --mcp-config /tmp/test-mcp.json
```

Зафиксировать в комментарии:
- Какие endpoints вызывает claude (POST /mcp? GET /mcp? SSE?)
- Какой формат JSON-RPC (методы: initialize, tools/list, tools/call)
- Использует ли claude `Mcp-Session-Id` заголовок

- [x] **Step 3: Выбрать MCP-библиотеку**

Проверить состояние двух библиотек:
```bash
curl -s https://api.github.com/repos/mark3labs/mcp-go | grep -E '"stargazers_count"|"updated_at"'
curl -s https://api.github.com/repos/modelcontextprotocol/go-sdk | grep -E '"stargazers_count"|"updated_at"'
```

Решение:
- Если есть подходящая стабильная (последний коммит < 30 дней, > 500 звёзд) — добавить через `go get` (НЕ менять версию go в go.mod, только добавить новую зависимость).
- Если нет — ручная имплементация JSON-RPC (для нашего use-case достаточно 3-х методов).

**Записать решение в начале pkg/mcp/server.go комментарием.**

- [x] **Step 4: Commit spike-результаты**

```bash
git add -A
git commit -m "Spike: проверка флагов claude и MCP HTTP transport

Зафиксированы флаги для session/resume/mcp-config и выбранная
библиотека для MCP-протокола (см. комментарии в коде)."
```

Если spike не привёл к изменениям в файлах — пропустить коммит, просто записать результаты в локальный блокнот для следующих задач.

---

## Stage 1: Data model

### Task 2: Добавить флаг Interactive в flow.Stage

**Files:**
- Modify: `pkg/flow/flow.go`
- Test: `pkg/flow/flow_test.go`

- [x] **Step 1: Написать падающий тест**

Добавить в `pkg/flow/flow_test.go` в конец файла:

```go
const interactiveYAML = `
name: interactive-flow
description: "test interactive"
stages:
  - id: discovery
    name: "Discovery"
    description: "ask user"
    agents: [planning]
    interactive: true
`

func TestParseInteractive(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, interactiveYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.Stages[0].Interactive {
		t.Error("Interactive should be true")
	}
}

func TestInteractiveDefaultsFalse(t *testing.T) {
	// validYAML doesn't set interactive — should be false
	f, err := flow.ParseFile(writeTemp(t, validYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, s := range f.Stages {
		if s.Interactive {
			t.Errorf("stage %q: Interactive should default to false", s.ID)
		}
	}
}
```

- [x] **Step 2: Запустить тест — должен упасть**

```bash
cd /Users/alexander.kopichin/work/flowManager
go test ./pkg/flow/ -run TestParseInteractive -v
```

Ожидается: FAIL (`f.Stages[0].Interactive undefined`).

- [x] **Step 3: Добавить поле Interactive в Stage**

В `pkg/flow/flow.go`, в struct `Stage`, после поля `Inputs`:

```go
type Stage struct {
	// … существующие поля без изменений …
	Artifacts   []Artifact `yaml:"artifacts"`
	Inputs      []Input    `yaml:"inputs"`
	// Interactive enables the ask_user MCP tool for all agents of this stage.
	Interactive bool `yaml:"interactive"`
}
```

- [x] **Step 4: Запустить тесты — должны пройти**

```bash
go test ./pkg/flow/ -v
```

Ожидается: PASS (включая существующие тесты и два новых).

- [x] **Step 5: Запустить линтер**

```bash
make lint
```

Ожидается: без ошибок.

- [x] **Step 6: Commit**

```bash
git add pkg/flow/flow.go pkg/flow/flow_test.go
git commit -m "flow: добавить флаг Interactive в Stage

Булевый флаг interactive: true на уровне стейджа, по умолчанию false.
Обратно совместимо: существующие flow.yaml парсятся без изменений."
```

---

### Task 3: Добавить StatusAwaitingUserInput

**Files:**
- Modify: `pkg/state/state.go`
- Test: `pkg/state/state_test.go`
- Modify: `pkg/orchestrator/fsm.go`
- Test: `pkg/orchestrator/fsm_test.go`

- [x] **Step 1: Написать падающие тесты**

Добавить в `pkg/state/state_test.go`:

```go
func TestAwaitingUserInputStatus(t *testing.T) {
	s := state.NewRunState([]string{"a"})
	s.SetStageStatus("a", state.StatusAwaitingUserInput)
	if s.Stages["a"].Status != state.StatusAwaitingUserInput {
		t.Errorf("expected awaiting_user_input, got %q", s.Stages["a"].Status)
	}
	if s.AllDone() {
		t.Error("awaiting_user_input must not count as done")
	}
}
```

Добавить в `pkg/orchestrator/fsm_test.go`:

```go
func TestAwaitingUserInputTransitions(t *testing.T) {
	cases := []struct {
		from, to state.StageStatus
		want     bool
	}{
		{state.StatusRunning, state.StatusAwaitingUserInput, true},
		{state.StatusPlanning, state.StatusAwaitingUserInput, true},
		{state.StatusAwaitingUserInput, state.StatusRunning, true},
		{state.StatusAwaitingUserInput, state.StatusPlanning, true},
		{state.StatusAwaitingUserInput, state.StatusFailed, true},
		{state.StatusAwaitingUserInput, state.StatusDone, false},
	}
	for _, c := range cases {
		got := orchestrator.ValidTransition(c.from, c.to)
		if got != c.want {
			t.Errorf("ValidTransition(%s, %s): got %v, want %v",
				c.from, c.to, got, c.want)
		}
	}
	if orchestrator.IsTerminal(state.StatusAwaitingUserInput) {
		t.Error("awaiting_user_input must not be terminal")
	}
}
```

- [x] **Step 2: Запустить — должны упасть**

```bash
go test ./pkg/state/ ./pkg/orchestrator/ -run "AwaitingUserInput" -v
```

Ожидается: FAIL (`StatusAwaitingUserInput undefined`).

- [x] **Step 3: Добавить константу статуса**

В `pkg/state/state.go`, в блок констант после `StatusRetrying`:

```go
const (
	StatusPending          StageStatus = "pending"
	StatusPlanning         StageStatus = "planning"
	StatusAwaitingApproval StageStatus = "awaiting_approval"
	StatusRevising         StageStatus = "revising"
	StatusReady            StageStatus = "ready"
	StatusRunning          StageStatus = "running"
	StatusRetrying         StageStatus = "retrying"
	StatusAwaitingUserInput StageStatus = "awaiting_user_input"
	StatusDone             StageStatus = "done"
	StatusFailed           StageStatus = "failed"
)
```

- [x] **Step 4: Добавить FSM-переходы**

В `pkg/orchestrator/fsm.go` в map `validTransitions`:

```go
var validTransitions = map[state.StageStatus][]state.StageStatus{
	state.StatusPending:          {state.StatusPlanning, state.StatusReady, state.StatusFailed},
	state.StatusPlanning:         {state.StatusAwaitingApproval, state.StatusFailed, state.StatusRetrying, state.StatusAwaitingUserInput},
	state.StatusAwaitingApproval: {state.StatusReady, state.StatusRevising},
	state.StatusRevising:         {state.StatusPlanning},
	state.StatusReady:            {state.StatusRunning},
	state.StatusRunning:          {state.StatusDone, state.StatusFailed, state.StatusRetrying, state.StatusAwaitingUserInput},
	state.StatusRetrying:         {state.StatusRunning, state.StatusPlanning, state.StatusFailed, state.StatusDone, state.StatusAwaitingApproval},
	state.StatusAwaitingUserInput: {state.StatusRunning, state.StatusPlanning, state.StatusFailed},
	state.StatusFailed:           {state.StatusPending},
}
```

`IsTerminal` править не нужно — `awaiting_user_input` не в списке.

- [x] **Step 5: Прогнать тесты**

```bash
go test ./pkg/state/ ./pkg/orchestrator/ -v
make lint
```

Ожидается: PASS, без warning.

- [x] **Step 6: Commit**

```bash
git add pkg/state/state.go pkg/state/state_test.go pkg/orchestrator/fsm.go pkg/orchestrator/fsm_test.go
git commit -m "state: добавить статус awaiting_user_input

Новый нетерминальный статус для стейджа, который ждёт ответ
пользователя в диалоге. Дополнены FSM-переходы: planning/running
могут перейти в awaiting_user_input и обратно."
```

---

## Stage 2: Persistence layer

### Task 4: pkg/mcp/dialog.go — чтение и запись dialog.jsonl

**Files:**
- Create: `pkg/mcp/dialog.go`
- Create: `pkg/mcp/dialog_test.go`

- [x] **Step 1: Написать падающий тест**

Создать `pkg/mcp/dialog_test.go`:

```go
package mcp_test

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/mcp"
)

func TestAppendAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "implementation.dialog.jsonl")

	if err := mcp.AppendQuestion(path, mcp.Question{
		ID: "q1", Question: "do X?", Options: []string{"yes", "no"}, AllowCustom: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(path, mcp.Answer{
		ID: "q1", Answer: "yes", FromOptions: true,
	}); err != nil {
		t.Fatal(err)
	}

	entries, err := mcp.ReadDialog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.ID != "q1" || e.Question != "do X?" || e.Answer == nil || *e.Answer != "yes" || !e.FromOptions {
		t.Errorf("entry mismatch: %+v", e)
	}
}

func TestReadOpenQuestion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "d.jsonl")
	mcp.AppendQuestion(path, mcp.Question{ID: "q1", Question: "x?"})

	entries, _ := mcp.ReadDialog(path)
	if len(entries) != 1 || entries[0].Answer != nil {
		t.Errorf("open question should have nil Answer: %+v", entries)
	}
}

func TestFindEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "d.jsonl")
	mcp.AppendQuestion(path, mcp.Question{ID: "q1", Question: "x?"})
	mcp.AppendAnswer(path, mcp.Answer{ID: "q1", Answer: "yes"})
	mcp.AppendQuestion(path, mcp.Question{ID: "q2", Question: "y?"})

	got, err := mcp.FindEntry(path, "q1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Answer == nil || *got.Answer != "yes" {
		t.Errorf("q1 should have answer 'yes': %+v", got)
	}

	got2, _ := mcp.FindEntry(path, "q2")
	if got2 == nil || got2.Answer != nil {
		t.Errorf("q2 should be found and open: %+v", got2)
	}

	notFound, _ := mcp.FindEntry(path, "q-nope")
	if notFound != nil {
		t.Errorf("nonexistent should return nil: %+v", notFound)
	}
}

func TestConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "d.jsonl")

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				mcp.AppendQuestion(path, mcp.Question{
					ID: "q-" + string(rune('a'+id)) + "-" + string(rune('0'+j%10)),
					Question: "?",
				})
			}
		}(i)
	}
	wg.Wait()

	entries, err := mcp.ReadDialog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 500 {
		t.Errorf("expected 500 entries, got %d (some appends were corrupted)", len(entries))
	}
}
```

- [x] **Step 2: Запустить — должен упасть на отсутствии пакета**

```bash
go test ./pkg/mcp/ -v
```

Ожидается: FAIL (no such package).

- [x] **Step 3: Создать pkg/mcp/dialog.go**

```go
// Package mcp implements the MCP (Model Context Protocol) HTTP server
// for the flowManager ask_user tool, plus per-phase dialog persistence.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Question is the record written when an agent calls ask_user.
type Question struct {
	ID          string   `json:"id"`
	TS          string   `json:"ts"`
	Question    string   `json:"question"`
	Options     []string `json:"options,omitempty"`
	AllowCustom bool     `json:"allow_custom"`
}

// Answer is the record written when the user replies.
type Answer struct {
	ID          string `json:"id"`
	TS          string `json:"ts"`
	Answer      string `json:"answer"`
	FromOptions bool   `json:"from_options"`
}

// Entry is a grouped Q/A pair for reading. Answer is nil when the question
// is still open.
type Entry struct {
	ID          string
	TS          string
	Question    string
	Options     []string
	AllowCustom bool
	Answer      *string
	AnswerTS    string
	FromOptions bool
}

// AppendQuestion writes a question record as a single JSON line.
func AppendQuestion(path string, q Question) error {
	if q.TS == "" {
		q.TS = time.Now().UTC().Format(time.RFC3339)
	}
	return appendLine(path, q)
}

// AppendAnswer writes an answer record as a single JSON line.
func AppendAnswer(path string, a Answer) error {
	if a.TS == "" {
		a.TS = time.Now().UTC().Format(time.RFC3339)
	}
	return appendLine(path, a)
}

// ReadDialog reads all records and groups them by ID into Entries in
// chronological order of the first question.
func ReadDialog(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open dialog: %w", err)
	}
	defer f.Close()

	byID := map[string]*Entry{}
	var order []string

	sc := bufio.NewScanner(f)
	// JSON-lines can be long if a question has many options; allow up to 1MB.
	sc.Buffer(make([]byte, 0, 4096), 1<<20)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// Try to detect record type by presence of "answer" field.
		var probe struct {
			ID     string `json:"id"`
			Answer *string `json:"answer"`
		}
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			continue // skip malformed (partial write would only happen >PIPE_BUF)
		}
		if probe.Answer != nil {
			var a Answer
			if err := json.Unmarshal([]byte(line), &a); err != nil {
				continue
			}
			e, ok := byID[a.ID]
			if !ok {
				// Answer without earlier question — create stub.
				e = &Entry{ID: a.ID}
				byID[a.ID] = e
				order = append(order, a.ID)
			}
			ans := a.Answer
			e.Answer = &ans
			e.AnswerTS = a.TS
			e.FromOptions = a.FromOptions
		} else {
			var q Question
			if err := json.Unmarshal([]byte(line), &q); err != nil {
				continue
			}
			if _, ok := byID[q.ID]; !ok {
				byID[q.ID] = &Entry{
					ID: q.ID, TS: q.TS, Question: q.Question,
					Options: q.Options, AllowCustom: q.AllowCustom,
				}
				order = append(order, q.ID)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan dialog: %w", err)
	}

	out := make([]Entry, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

// FindEntry returns the entry with the given id, or nil if not present.
func FindEntry(path, id string) (*Entry, error) {
	entries, err := ReadDialog(path)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].ID == id {
			return &entries[i], nil
		}
	}
	return nil, nil
}

// appendLine opens the file with O_APPEND and writes one JSON record + \n.
// POSIX guarantees atomic appends up to PIPE_BUF (4096); our records fit.
func appendLine(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	data = append(data, '\n')
	if len(data) > 4096 {
		return fmt.Errorf("dialog record too large (%d bytes > PIPE_BUF)", len(data))
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open append: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}
```

- [x] **Step 4: Запустить тесты — должны пройти**

```bash
go test ./pkg/mcp/ -v -race
```

Ожидается: PASS (включая TestConcurrentAppend, который проверяет, что О_APPEND даёт корректный результат под нагрузкой).

- [x] **Step 5: Linter**

```bash
make lint
```

Ожидается: без ошибок.

- [x] **Step 6: Commit**

```bash
git add pkg/mcp/dialog.go pkg/mcp/dialog_test.go
git commit -m "mcp: append-only лог диалога с группировкой по id

Реализован формат <phase>.dialog.jsonl: append-only пары
question/answer, чтение группирует записи по id. O_APPEND
обеспечивает атомарность под конкуретными записями (записи
помещаются в PIPE_BUF = 4096 байт)."
```

---

## Stage 3: MCP HTTP server

### Task 5: pkg/mcp/server.go — JSON-RPC сервер с tool ask_user

**Files:**
- Create: `pkg/mcp/server.go`
- Create: `pkg/mcp/server_test.go`

> **Зависит от Task 1** — формат HTTP MCP-протокола должен быть зафиксирован. Если выбрана внешняя библиотека — используется её API; если ручная реализация — следует протоколу из Task 1.

- [x] **Step 1: Написать падающий тест**

Создать `pkg/mcp/server_test.go`. Тесты обращаются к серверу как HTTP-handler, без поднятия реального listener.

```go
package mcp_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/mcp"
)

func newTestServer(t *testing.T) (*mcp.Server, string) {
	t.Helper()
	runDir := t.TempDir()
	// Stage dir for "stage-1"
	stageDir := filepath.Join(runDir, "stage-1")
	if err := makeDir(stageDir); err != nil {
		t.Fatal(err)
	}
	s := mcp.NewServer(runDir, nil) // nil EventBus — tests don't need it
	return s, runDir
}

func makeDir(p string) error {
	return mcp.MkdirAll(p, 0755) // exported test helper, see step 3
}

// rpc sends a JSON-RPC request to the server and returns the JSON body.
func rpc(t *testing.T, s *mcp.Server, urlPath string, method string, params any, id int) map[string]any {
	t.Helper()
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	body, _ := json.Marshal(req)
	r := httptest.NewRequest("POST", urlPath, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("rpc %s: HTTP %d, body %s", method, w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v, body: %s", err, w.Body.String())
	}
	return out
}

func TestToolsList(t *testing.T) {
	s, _ := newTestServer(t)
	resp := rpc(t, s, "/mcp/stage-1/implementation", "tools/list", nil, 1)
	result, _ := resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "ask_user" {
		t.Errorf("tool name: got %v", tool["name"])
	}
}

func TestToolsCallReplay(t *testing.T) {
	// Pre-populate dialog with an already-answered q1
	s, runDir := newTestServer(t)
	dialogPath := filepath.Join(runDir, "stage-1", "implementation.dialog.jsonl")
	mcp.AppendQuestion(dialogPath, mcp.Question{ID: "q1", Question: "x?"})
	mcp.AppendAnswer(dialogPath, mcp.Answer{ID: "q1", Answer: "yes", FromOptions: true})

	resp := rpc(t, s, "/mcp/stage-1/implementation", "tools/call",
		map[string]any{
			"name": "ask_user",
			"arguments": map[string]any{
				"id":       "q1",
				"question": "x?",
			},
		}, 2)

	result, _ := resp["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected 1 content item, got %d", len(content))
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	var payload struct{ Answer string `json:"answer"`; FromOptions bool `json:"from_options"` }
	json.Unmarshal([]byte(text), &payload)
	if payload.Answer != "yes" || !payload.FromOptions {
		t.Errorf("replay wrong: %+v", payload)
	}
}

func TestToolsCallBlocksUntilAnswered(t *testing.T) {
	s, runDir := newTestServer(t)

	done := make(chan map[string]any, 1)
	go func() {
		done <- rpc(t, s, "/mcp/stage-1/implementation", "tools/call",
			map[string]any{
				"name": "ask_user",
				"arguments": map[string]any{"id": "q1", "question": "x?"},
			}, 3)
	}()

	// Give the goroutine time to register as waiter
	time.Sleep(50 * time.Millisecond)

	select {
	case <-done:
		t.Fatal("tools/call returned before answer was provided")
	default:
	}

	// Simulate UI answer
	dialogPath := filepath.Join(runDir, "stage-1", "implementation.dialog.jsonl")
	mcp.AppendAnswer(dialogPath, mcp.Answer{ID: "q1", Answer: "hello", FromOptions: false})
	if err := s.NotifyAnswer("stage-1", "implementation", "q1", "hello", false); err != nil {
		t.Fatal(err)
	}

	select {
	case resp := <-done:
		result, _ := resp["result"].(map[string]any)
		content, _ := result["content"].([]any)
		text, _ := content[0].(map[string]any)["text"].(string)
		if !contains(text, "hello") {
			t.Errorf("tool result missing answer: %s", text)
		}
	case <-time.After(time.Second):
		t.Fatal("tools/call did not return after answer")
	}
}

func TestToolsCallCancel(t *testing.T) {
	s, _ := newTestServer(t)

	done := make(chan map[string]any, 1)
	go func() {
		done <- rpc(t, s, "/mcp/stage-1/implementation", "tools/call",
			map[string]any{
				"name": "ask_user",
				"arguments": map[string]any{"id": "q1", "question": "x?"},
			}, 4)
	}()

	time.Sleep(50 * time.Millisecond)
	if err := s.CancelStage("stage-1"); err != nil {
		t.Fatal(err)
	}

	select {
	case resp := <-done:
		// Tool error expected
		if _, hasErr := resp["error"]; !hasErr {
			if result, _ := resp["result"].(map[string]any); result != nil {
				if isErr, _ := result["isError"].(bool); !isErr {
					t.Errorf("expected error result: %+v", resp)
				}
			}
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not unblock tools/call")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

- [x] **Step 2: Запустить — должен упасть**

```bash
go test ./pkg/mcp/ -v
```

Ожидается: FAIL (`Server undefined`).

- [x] **Step 3: Создать pkg/mcp/server.go**

```go
package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// MkdirAll exposed for tests; just wraps os.MkdirAll to keep the test
// package from depending on os directly for this helper.
func MkdirAll(p string, mode os.FileMode) error { return os.MkdirAll(p, mode) }

// Notifier is implemented by orchestrator.EventBus so the MCP server can
// publish ask_user/user_answered events without importing orchestrator.
type Notifier interface {
	PublishAskUser(stageID, phase, qID, question string, options []string, allowCustom bool)
	PublishUserAnswered(stageID, phase, qID, answer string)
	SetStageStatus(stageID string, awaitingInput bool, phase string)
}

// Server is the MCP HTTP server. One instance handles all stages and phases;
// the URL /mcp/<stage>/<phase> distinguishes them.
type Server struct {
	runDir   string
	notifier Notifier
	mu       sync.Mutex
	waiters  map[string]chan waiterEvent // key: stage|phase|qID
}

type waiterEvent struct {
	answer      string
	fromOptions bool
	cancelled   bool
}

func NewServer(runDir string, notifier Notifier) *Server {
	return &Server{
		runDir:   runDir,
		notifier: notifier,
		waiters:  make(map[string]chan waiterEvent),
	}
}

// ServeHTTP routes /mcp/<stage>/<phase> to the JSON-RPC handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/mcp/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.Error(w, "expected /mcp/<stage>/<phase>", http.StatusBadRequest)
		return
	}
	stageID, phase := parts[0], parts[1]
	if strings.Contains(stageID, "..") || strings.Contains(phase, "..") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, nil, -32700, "parse error")
		return
	}

	switch req.Method {
	case "initialize":
		s.writeResult(w, req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "flowmanager", "version": "1"},
		})
	case "tools/list":
		s.writeResult(w, req.ID, map[string]any{
			"tools": []any{askUserToolSchema()},
		})
	case "tools/call":
		s.handleToolsCall(w, req.ID, req.Params, stageID, phase)
	default:
		s.writeError(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

func askUserToolSchema() map[string]any {
	return map[string]any{
		"name":        "ask_user",
		"description": "Ask the human user a question and wait for their answer. Use this when you need clarification, a choice between alternatives, or any decision that requires human input.",
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"id", "question"},
			"properties": map[string]any{
				"id":           map[string]any{"type": "string", "description": "Stable, deterministic id for this question (e.g. q1, q2, …). Used for idempotent replay after restart."},
				"question":     map[string]any{"type": "string"},
				"options":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional suggested answers."},
				"allow_custom": map[string]any{"type": "boolean", "default": true, "description": "Whether the user may type a freeform answer."},
			},
		},
	}
}

func (s *Server) handleToolsCall(w http.ResponseWriter, rpcID json.RawMessage, params json.RawMessage, stageID, phase string) {
	var p struct {
		Name      string `json:"name"`
		Arguments struct {
			ID          string   `json:"id"`
			Question    string   `json:"question"`
			Options     []string `json:"options"`
			AllowCustom *bool    `json:"allow_custom"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		s.writeError(w, rpcID, -32602, "invalid params")
		return
	}
	if p.Name != "ask_user" {
		s.writeError(w, rpcID, -32601, "unknown tool: "+p.Name)
		return
	}
	if p.Arguments.ID == "" || p.Arguments.Question == "" {
		s.writeError(w, rpcID, -32602, "id and question are required")
		return
	}
	allowCustom := true
	if p.Arguments.AllowCustom != nil {
		allowCustom = *p.Arguments.AllowCustom
	}

	dialogPath := filepath.Join(s.runDir, stageID, phase+".dialog.jsonl")

	// 1. Idempotent replay
	existing, err := FindEntry(dialogPath, p.Arguments.ID)
	if err != nil {
		s.writeError(w, rpcID, -32603, "read dialog: "+err.Error())
		return
	}
	if existing != nil && existing.Answer != nil {
		s.writeToolResult(w, rpcID, *existing.Answer, existing.FromOptions)
		return
	}

	// 2. Record the question (only if not already recorded — idempotency)
	if existing == nil {
		if err := AppendQuestion(dialogPath, Question{
			ID: p.Arguments.ID, Question: p.Arguments.Question,
			Options: p.Arguments.Options, AllowCustom: allowCustom,
		}); err != nil {
			s.writeError(w, rpcID, -32603, "append question: "+err.Error())
			return
		}
		if s.notifier != nil {
			s.notifier.PublishAskUser(stageID, phase, p.Arguments.ID, p.Arguments.Question, p.Arguments.Options, allowCustom)
			s.notifier.SetStageStatus(stageID, true, phase)
		}
	}

	// 3. Register waiter and block
	key := waiterKey(stageID, phase, p.Arguments.ID)
	ch := make(chan waiterEvent, 1)
	s.mu.Lock()
	s.waiters[key] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.waiters, key)
		s.mu.Unlock()
	}()

	ev := <-ch
	if ev.cancelled {
		s.writeToolErrorResult(w, rpcID, "cancelled by user")
		return
	}
	s.writeToolResult(w, rpcID, ev.answer, ev.fromOptions)
}

func waiterKey(stage, phase, qID string) string {
	return stage + "|" + phase + "|" + qID
}

// NotifyAnswer is called from the dashboard REST handler when the user answers.
// It unblocks any waiting tools/call. Returns nil if there was no waiter
// (late answer arrived after restart — still valid, the answer is in the file).
func (s *Server) NotifyAnswer(stageID, phase, qID, answer string, fromOptions bool) error {
	key := waiterKey(stageID, phase, qID)
	s.mu.Lock()
	ch, ok := s.waiters[key]
	s.mu.Unlock()
	if ok {
		ch <- waiterEvent{answer: answer, fromOptions: fromOptions}
	}
	if s.notifier != nil {
		s.notifier.PublishUserAnswered(stageID, phase, qID, answer)
		s.notifier.SetStageStatus(stageID, false, phase)
	}
	return nil
}

// CancelStage cancels all pending waiters for the given stage (across phases).
func (s *Server) CancelStage(stageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := stageID + "|"
	for k, ch := range s.waiters {
		if strings.HasPrefix(k, prefix) {
			select {
			case ch <- waiterEvent{cancelled: true}:
			default:
			}
		}
	}
	return nil
}

func (s *Server) writeResult(w http.ResponseWriter, rpcID json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      rawOrNull(rpcID),
		"result":  result,
	})
}

func (s *Server) writeError(w http.ResponseWriter, rpcID json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      rawOrNull(rpcID),
		"error":   map[string]any{"code": code, "message": message},
	})
}

func (s *Server) writeToolResult(w http.ResponseWriter, rpcID json.RawMessage, answer string, fromOptions bool) {
	payload, _ := json.Marshal(map[string]any{"answer": answer, "from_options": fromOptions})
	s.writeResult(w, rpcID, map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": string(payload)},
		},
	})
}

func (s *Server) writeToolErrorResult(w http.ResponseWriter, rpcID json.RawMessage, message string) {
	s.writeResult(w, rpcID, map[string]any{
		"isError": true,
		"content": []any{
			map[string]any{"type": "text", "text": message},
		},
	})
}

func rawOrNull(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}

// Compile-time check that *Server implements http.Handler.
var _ http.Handler = (*Server)(nil)

func _unused() { fmt.Println("") } // keeps "fmt" import usable if removed; safe to delete
```

> **Note on the Notifier abstraction:** orchestrator.EventBus has a generic `Publish(Event)` method. To keep `pkg/mcp` from depending on `pkg/orchestrator` (avoiding a cycle), we introduce the `Notifier` interface; orchestrator will provide an adapter implementing it (see Task 6).

- [x] **Step 4: Прогнать тесты**

```bash
go test ./pkg/mcp/ -v -race
make lint
```

Ожидается: PASS. Если `make lint` ругается на `_unused` — удалить эту функцию и неиспользуемый импорт.

- [x] **Step 5: Commit**

```bash
git add pkg/mcp/server.go pkg/mcp/server_test.go
git commit -m "mcp: HTTP-сервер с tool ask_user

JSON-RPC 2.0 на путях /mcp/<stage>/<phase>. Поддерживает три метода:
initialize, tools/list, tools/call. Idempotent-replay по id вопроса,
блокирует tools/call на canale waiter до прихода ответа или отмены."
```

---

## Stage 4: Wire MCP into orchestrator

### Task 6: Новые EventType и Notifier-адаптер

**Files:**
- Modify: `pkg/orchestrator/eventbus.go`
- Test: `pkg/orchestrator/eventbus_test.go`
- Create: `pkg/orchestrator/mcp_notifier.go`

- [x] **Step 1: Написать падающий тест**

В `pkg/orchestrator/eventbus_test.go` добавить:

```go
func TestPublishAskUserEvent(t *testing.T) {
	bus := orchestrator.NewEventBus()
	defer bus.Close()
	ch := bus.Subscribe()

	bus.Publish(orchestrator.Event{
		Type:    orchestrator.EventAskUser,
		StageID: "s1",
		Data: map[string]any{
			"id": "q1", "phase": "implementation",
			"question": "x?", "options": []string{"a", "b"}, "allow_custom": true,
		},
	})

	ev := <-ch
	if ev.Type != orchestrator.EventAskUser {
		t.Errorf("type: got %q", ev.Type)
	}
}
```

- [x] **Step 2: Запустить — должен упасть**

```bash
go test ./pkg/orchestrator/ -run TestPublishAskUserEvent -v
```

Ожидается: FAIL (`EventAskUser undefined`).

- [x] **Step 3: Добавить EventType**

В `pkg/orchestrator/eventbus.go`, в блок констант:

```go
const (
	EventStageStatusChanged EventType = "stage_status_changed"
	EventAgentAction        EventType = "agent_action"
	EventAgentCompleted     EventType = "agent_completed"
	EventFeedbackReceived   EventType = "feedback_received"
	EventApproved           EventType = "approved"
	EventRevised            EventType = "revised"
	EventRetryScheduled     EventType = "retry_scheduled"
	EventRetryExhausted     EventType = "retry_exhausted"
	EventManualRetry        EventType = "manual_retry"
	EventAskUser            EventType = "ask_user"
	EventUserAnswered       EventType = "user_answered"
)
```

- [x] **Step 4: Создать Notifier-адаптер**

Создать `pkg/orchestrator/mcp_notifier.go`:

```go
package orchestrator

import (
	"github.com/akopichin/afm/pkg/state"
)

// McpNotifier bridges pkg/mcp to the orchestrator's EventBus and state.
// It satisfies mcp.Notifier without pkg/mcp importing pkg/orchestrator.
type McpNotifier struct {
	bus *EventBus
	o   *Orchestrator
}

// NewMcpNotifier returns a Notifier that publishes events to bus and
// transitions stage status via the orchestrator.
func NewMcpNotifier(o *Orchestrator) *McpNotifier {
	return &McpNotifier{bus: o.bus, o: o}
}

func (n *McpNotifier) PublishAskUser(stageID, phase, qID, question string, options []string, allowCustom bool) {
	n.bus.Publish(Event{
		Type:    EventAskUser,
		StageID: stageID,
		Data: map[string]any{
			"id":           qID,
			"phase":        phase,
			"question":     question,
			"options":      options,
			"allow_custom": allowCustom,
		},
	})
}

func (n *McpNotifier) PublishUserAnswered(stageID, phase, qID, answer string) {
	n.bus.Publish(Event{
		Type:    EventUserAnswered,
		StageID: stageID,
		Data: map[string]any{
			"id":     qID,
			"phase":  phase,
			"answer": answer,
		},
	})
}

// SetStageStatus transitions a stage to/from awaiting_user_input.
// When awaitingInput is true: planning-phase → planning is restored on answer;
// implementation/review → running.
func (n *McpNotifier) SetStageStatus(stageID string, awaitingInput bool, phase string) {
	if awaitingInput {
		n.o.setStatus(stageID, state.StatusAwaitingUserInput)
		return
	}
	if phase == "planning" {
		n.o.setStatus(stageID, state.StatusPlanning)
	} else {
		n.o.setStatus(stageID, state.StatusRunning)
	}
}
```

- [x] **Step 5: Прогнать тесты и линтер**

```bash
go test ./pkg/orchestrator/ -v
make lint
```

Ожидается: PASS.

- [x] **Step 6: Commit**

```bash
git add pkg/orchestrator/eventbus.go pkg/orchestrator/eventbus_test.go pkg/orchestrator/mcp_notifier.go
git commit -m "orchestrator: события ask_user/user_answered + Notifier для MCP

Два новых EventType (ask_user, user_answered) для уведомления UI
через WebSocket. McpNotifier — адаптер, реализующий mcp.Notifier,
чтобы pkg/mcp не зависел от pkg/orchestrator."
```

---

### Task 7: Executor — поддержка session-id и mcp-config

**Files:**
- Modify: `pkg/executor/executor.go`
- Test: `pkg/executor/executor_test.go`

> **Используй имена флагов claude, зафиксированные в Task 1.** Ниже — типичные значения (`--session-id`, `--resume`, `--mcp-config`); если CLI их не поддерживает, скорректировать.

- [x] **Step 1: Расширить Config**

В `pkg/executor/executor.go`, в struct `Config`, добавить:

```go
type Config struct {
	Command     string
	ExtraArgs   []string
	IdleTimeout time.Duration
	OnAction    func(tool, detail string)
	// NEW fields below:
	SessionID  string // if non-empty, passed via --session-id
	Resume     bool   // if true, --resume <SessionID> is used (requires SessionID)
	McpConfig  string // path to mcp.json, passed via --mcp-config when non-empty
}
```

- [x] **Step 2: Передавать новые аргументы в claude**

В `New(cfg Config)`: оставить логику дефолтных `ExtraArgs` для headless. Но при наличии новых полей добавить флаги. Изменить `run` метод так, чтобы он строил args правильно:

В функции `(e *Executor) run`:

```go
func (e *Executor) run(ctx context.Context, prompt string, lineCallback func(string)) error {
	args := append([]string{}, e.cfg.ExtraArgs...)
	if e.cfg.McpConfig != "" {
		args = append(args, "--mcp-config", e.cfg.McpConfig)
	}
	if e.cfg.SessionID != "" {
		if e.cfg.Resume {
			args = append(args, "--resume", e.cfg.SessionID)
		} else {
			args = append(args, "--session-id", e.cfg.SessionID)
		}
	}
	cmd := exec.CommandContext(ctx, e.cfg.Command, args...)
	cmd.Stdin = strings.NewReader(prompt)
	// … остальная логика без изменений …
```

- [x] **Step 3: Тест на формирование args**

Расширить `pkg/executor/executor_test.go`:

```go
func TestConfigBuildsArgs(t *testing.T) {
	// We can't easily intercept exec.Cmd without an actual run, so this test
	// verifies that Config fields survive to the New() result.
	cfg := executor.Config{
		Command:    "echo",
		ExtraArgs:  []string{"--print"},
		SessionID:  "uuid-1",
		McpConfig:  "/tmp/mcp.json",
		Resume:     false,
	}
	e := executor.New(cfg)
	if e == nil {
		t.Fatal("New returned nil")
	}
	// We cannot directly inspect cfg from outside the package — instead we
	// invoke RunAgent with a quick command that prints args. Use the actual
	// "echo" binary as the Command to keep the test hermetic.
	// (This is integration-ish; if not feasible, this test can be replaced
	// with a smaller "config preserved" check.)
}
```

> Этот тест — best-effort sanity check. Реальное поведение проверяется в integration-tests (Task 13). Если тест-нагрузка через echo слишком хрупкая — оставить тест-stub и положиться на integration_test.

- [x] **Step 4: Прогнать тесты и линтер**

```bash
go test ./pkg/executor/ -v
make lint
```

- [x] **Step 5: Commit**

```bash
git add pkg/executor/executor.go pkg/executor/executor_test.go
git commit -m "executor: поддержка --session-id, --resume, --mcp-config

Новые поля Config.SessionID, Config.Resume, Config.McpConfig
включают передачу соответствующих флагов в claude CLI. По умолчанию
поля пустые — поведение для существующих стейджей не меняется."
```

---

### Task 8: Server — wire MCP handler и REST для диалога

**Files:**
- Modify: `pkg/server/server.go`
- Modify: `pkg/server/handlers.go`
- Test: `pkg/server/handlers_test.go`

- [x] **Step 1: Расширить Config сервера**

В `pkg/server/server.go`, struct `Config`:

```go
type Config struct {
	Port      int
	RunDir    string
	StateFile string
	Bus       *orchestrator.EventBus
	ApproveFn func(stageID string)
	ReviseFn  func(stageID, feedback string)
	RetryFn   func(stageID string)
	// NEW
	McpServer       *mcp.Server          // can be nil for tests
	DialogAnswerFn  func(stageID, phase, qID, answer string, fromOptions bool) error
	DialogCancelFn  func(stageID string) error
}
```

И в struct `Server` добавить соответствующие поля:

```go
type Server struct {
	// … существующие …
	mcpSrv         *mcp.Server
	dialogAnswerFn func(stageID, phase, qID, answer string, fromOptions bool) error
	dialogCancelFn func(stageID string) error
}
```

В `New(cfg Config)` сохранить эти поля и **зарегистрировать MCP-хэндлер на mux**:

```go
mux := http.NewServeMux()
mux.HandleFunc("/api/status", s.handleStatus)
mux.HandleFunc("/api/stages/", s.routeStages)
mux.HandleFunc("/ws", s.handleWebSocket)
if cfg.McpServer != nil {
	mux.Handle("/mcp/", cfg.McpServer)
}
mux.Handle("/", http.FileServer(http.FS(web.FS)))
```

И добавить импорт `"github.com/akopichin/afm/pkg/mcp"`.

- [x] **Step 2: Добавить REST-handlers для диалога**

В `pkg/server/handlers.go`:

```go
// handleDialog handles GET/POST /api/stages/<id>/dialog and sub-paths.
// Routing is done in routeStages.

type dialogAnswerRequest struct {
	ID          string `json:"id"`
	Phase       string `json:"phase"`
	Answer      string `json:"answer"`
	FromOptions bool   `json:"from_options"`
}

func (s *Server) handleDialogGet(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/dialog")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}
	stageDir := filepath.Join(s.runDir, stageID)
	type uiEntry struct {
		ID          string   `json:"id"`
		Phase       string   `json:"phase"`
		TS          string   `json:"ts"`
		Question    string   `json:"question"`
		Options     []string `json:"options,omitempty"`
		AllowCustom bool     `json:"allow_custom"`
		Answer      *string  `json:"answer,omitempty"`
		AnswerTS    string   `json:"answer_ts,omitempty"`
		FromOptions bool     `json:"from_options,omitempty"`
	}
	var out []uiEntry
	for _, phase := range []string{"planning", "implementation", "review"} {
		path := filepath.Join(stageDir, phase+".dialog.jsonl")
		entries, err := mcp.ReadDialog(path)
		if err != nil {
			continue // missing files are normal
		}
		for _, e := range entries {
			out = append(out, uiEntry{
				ID: e.ID, Phase: phase, TS: e.TS, Question: e.Question,
				Options: e.Options, AllowCustom: e.AllowCustom,
				Answer: e.Answer, AnswerTS: e.AnswerTS, FromOptions: e.FromOptions,
			})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) handleDialogAnswer(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/dialog/answer")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}
	var req dialogAnswerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.ID == "" || req.Phase == "" || req.Answer == "" {
		http.Error(w, "id, phase, answer required", http.StatusBadRequest)
		return
	}
	// Append answer to the dialog file FIRST (persist before signalling).
	dialogPath := filepath.Join(s.runDir, stageID, req.Phase+".dialog.jsonl")
	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{
		ID: req.ID, Answer: req.Answer, FromOptions: req.FromOptions,
	}); err != nil {
		http.Error(w, "persist answer: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if s.dialogAnswerFn != nil {
		if err := s.dialogAnswerFn(stageID, req.Phase, req.ID, req.Answer, req.FromOptions); err != nil {
			http.Error(w, "notify: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleDialogCancel(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/dialog/cancel")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}
	// Verify stage is actually awaiting input
	rs, err := state.Load(s.stateFile)
	if err != nil {
		http.Error(w, "load state: "+err.Error(), http.StatusInternalServerError)
		return
	}
	st, ok := rs.Stages[stageID]
	if !ok || st.Status != state.StatusAwaitingUserInput {
		http.Error(w, "stage is not awaiting user input", http.StatusBadRequest)
		return
	}
	if s.dialogCancelFn != nil {
		if err := s.dialogCancelFn(stageID); err != nil {
			http.Error(w, "cancel: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "cancelled"})
}
```

Не забудь импорт:
```go
import (
	// … существующие …
	"github.com/akopichin/afm/pkg/mcp"
)
```

- [x] **Step 3: Подключить новые маршруты в routeStages**

В `pkg/server/server.go`, функция `routeStages`:

```go
func (s *Server) routeStages(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/plan"):
		s.handlePlan(w, r)
	case strings.HasSuffix(path, "/log"):
		s.handleLog(w, r)
	case strings.HasSuffix(path, "/approve") && r.Method == http.MethodPost:
		s.handleApprove(w, r)
	case strings.HasSuffix(path, "/revise") && r.Method == http.MethodPost:
		s.handleRevise(w, r)
	case strings.HasSuffix(path, "/retry") && r.Method == http.MethodPost:
		s.handleRetry(w, r)
	case strings.HasSuffix(path, "/dialog") && r.Method == http.MethodGet:
		s.handleDialogGet(w, r)
	case strings.HasSuffix(path, "/dialog/answer") && r.Method == http.MethodPost:
		s.handleDialogAnswer(w, r)
	case strings.HasSuffix(path, "/dialog/cancel") && r.Method == http.MethodPost:
		s.handleDialogCancel(w, r)
	default:
		http.NotFound(w, r)
	}
}
```

- [x] **Step 4: Тесты для handlers**

Добавить в `pkg/server/handlers_test.go`:

```go
func TestDialogGet(t *testing.T) {
	runDir := t.TempDir()
	stateFile := filepath.Join(runDir, "state.json")
	stage := "s1"
	stageDir := filepath.Join(runDir, stage)
	os.MkdirAll(stageDir, 0755)
	dialogPath := filepath.Join(stageDir, "implementation.dialog.jsonl")
	mcp.AppendQuestion(dialogPath, mcp.Question{ID: "q1", Question: "x?"})
	mcp.AppendAnswer(dialogPath, mcp.Answer{ID: "q1", Answer: "yes"})

	rs := state.NewRunState([]string{stage})
	rs.Save(stateFile)

	srv := server.New(server.Config{
		RunDir: runDir, StateFile: stateFile, Bus: orchestrator.NewEventBus(),
	})

	req := httptest.NewRequest("GET", "/api/stages/"+stage+"/dialog", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status %d, body %s", w.Code, w.Body.String())
	}
	var got []map[string]any
	json.Unmarshal(w.Body.Bytes(), &got)
	if len(got) != 1 || got[0]["id"] != "q1" || got[0]["phase"] != "implementation" {
		t.Errorf("dialog entries wrong: %+v", got)
	}
}

func TestDialogAnswer(t *testing.T) {
	runDir := t.TempDir()
	stateFile := filepath.Join(runDir, "state.json")
	stage := "s1"
	os.MkdirAll(filepath.Join(runDir, stage), 0755)

	rs := state.NewRunState([]string{stage})
	rs.Save(stateFile)

	called := struct {
		stage, phase, id, answer string
		fromOptions              bool
	}{}
	srv := server.New(server.Config{
		RunDir: runDir, StateFile: stateFile, Bus: orchestrator.NewEventBus(),
		DialogAnswerFn: func(s, p, q, a string, fo bool) error {
			called.stage, called.phase, called.id, called.answer, called.fromOptions = s, p, q, a, fo
			return nil
		},
	})

	body := `{"id":"q1","phase":"implementation","answer":"hello","from_options":false}`
	req := httptest.NewRequest("POST", "/api/stages/"+stage+"/dialog/answer", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d, body %s", w.Code, w.Body.String())
	}
	if called.stage != stage || called.id != "q1" || called.answer != "hello" {
		t.Errorf("answerFn called with wrong args: %+v", called)
	}
}

func TestDialogCancelRejectsNonAwaiting(t *testing.T) {
	runDir := t.TempDir()
	stateFile := filepath.Join(runDir, "state.json")
	stage := "s1"
	rs := state.NewRunState([]string{stage})
	rs.SetStageStatus(stage, state.StatusRunning) // not awaiting
	rs.Save(stateFile)

	srv := server.New(server.Config{
		RunDir: runDir, StateFile: stateFile, Bus: orchestrator.NewEventBus(),
	})

	req := httptest.NewRequest("POST", "/api/stages/"+stage+"/dialog/cancel", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for non-awaiting stage, got %d", w.Code)
	}
}
```

Не забудь дополнить импорты в тесте: `"path/filepath"`, `"os"`, `"strings"`, `"net/http/httptest"`, `"encoding/json"`, `"github.com/akopichin/afm/pkg/mcp"`.

- [x] **Step 5: Прогнать**

```bash
go test ./pkg/server/ -v
make lint
```

- [x] **Step 6: Commit**

```bash
git add pkg/server/server.go pkg/server/handlers.go pkg/server/handlers_test.go
git commit -m "server: REST-handlers для диалога + регистрация MCP-handler

GET /api/stages/<id>/dialog возвращает склеенный список Q/A пар
по всем фазам. POST .../dialog/answer персистит ответ и зовёт
notifier. POST .../dialog/cancel отменяет диалог. MCP-handler
монтируется по /mcp/ если cfg.McpServer != nil."
```

---

### Task 9: Orchestrator — runInteractiveAgent и ветка resume

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go`
- Test: `pkg/orchestrator/orchestrator_test.go`

- [x] **Step 1: Хелперы для session.json**

В `pkg/orchestrator/orchestrator.go` (или в новом файле `pkg/orchestrator/session.go`) добавить:

```go
// sessionFile returns the path to <phase>.session.json for a stage.
func sessionFile(stageDir, phase string) string {
	return filepath.Join(stageDir, phase+".session.json")
}

type phaseSession struct {
	SessionID string `json:"session_id"`
}

// loadOrCreateSession returns the session-id from disk, generating a fresh
// UUID if no file exists.
func loadOrCreateSession(stageDir, phase string) (string, error) {
	p := sessionFile(stageDir, phase)
	data, err := os.ReadFile(p)
	if err == nil {
		var s phaseSession
		if err := json.Unmarshal(data, &s); err != nil {
			return "", fmt.Errorf("parse session: %w", err)
		}
		if s.SessionID != "" {
			return s.SessionID, nil
		}
	}
	id := newUUID()
	out, _ := json.Marshal(phaseSession{SessionID: id})
	if err := os.WriteFile(p, out, 0644); err != nil {
		return "", fmt.Errorf("write session: %w", err)
	}
	return id, nil
}

// sessionExists reports whether <phase>.session.json is present.
func sessionExists(stageDir, phase string) bool {
	_, err := os.Stat(sessionFile(stageDir, phase))
	return err == nil
}

// newUUID generates a random RFC4122 v4 UUID using crypto/rand.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
```

Дополни импорты: `"crypto/rand"`, `"encoding/json"`.

- [x] **Step 2: Генерация mcp.json**

```go
// writeMcpConfig writes a per-stage mcp.json pointing to /mcp/<stage>/<phase>.
// dashURL is "http://127.0.0.1:<port>" (must not have trailing slash).
func writeMcpConfig(stageDir, stageID, phase, dashURL string) (string, error) {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"flowmanager": map[string]any{
				"type": "http",
				"url":  fmt.Sprintf("%s/mcp/%s/%s", dashURL, stageID, phase),
			},
		},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(stageDir, "mcp.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}
	return path, nil
}
```

- [x] **Step 3: Прокинуть dashURL в Orchestrator**

В struct `Options` добавить:
```go
type Options struct {
	// … существующие …
	DashboardURL string // e.g. "http://127.0.0.1:9876"
}
```

Это поле заполняется в `cmd/flowmanager/main.go` после `server.Start()`.

- [x] **Step 4: Расширить runnerFor чтобы включал interactive-флаги**

```go
// runnerFor returns the appropriate Runner for a stage's phase.
// For interactive stages, generates mcp.json and a session id, then
// returns an executor configured with --mcp-config and --session-id (or --resume).
func (o *Orchestrator) runnerFor(s flow.Stage, phase string) executor.Runner {
	if !s.Interactive {
		// Non-interactive — existing behavior
		if s.Command == "" {
			return o.runner
		}
		return executor.New(executor.Config{
			Command:     s.Command,
			IdleTimeout: o.opts.Config.Executor.IdleTimeout,
			OnAction:    o.actionPublisher(s.ID),
		})
	}

	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	mcpPath, err := writeMcpConfig(stageDir, s.ID, phase, o.opts.DashboardURL)
	if err != nil {
		// Fall back to non-interactive runner; the stage will run without ask_user.
		// In production this is logged but does not crash.
		return o.runner
	}

	resume := sessionExists(stageDir, phase)
	sessionID, err := loadOrCreateSession(stageDir, phase)
	if err != nil {
		return o.runner
	}

	cmd := s.Command
	if cmd == "" {
		cmd = o.opts.Config.Client.Command
	}
	return executor.New(executor.Config{
		Command:     cmd,
		ExtraArgs:   []string{"--print", "--output-format", "stream-json", "--dangerously-skip-permissions"},
		IdleTimeout: o.opts.Config.Executor.IdleTimeout,
		OnAction:    o.actionPublisher(s.ID),
		SessionID:   sessionID,
		Resume:      resume,
		McpConfig:   mcpPath,
	})
}

func (o *Orchestrator) actionPublisher(stageID string) func(string, string) {
	return func(tool, detail string) {
		o.bus.Publish(Event{Type: EventAgentAction, StageID: stageID, Data: map[string]string{
			"tool": tool, "detail": detail,
		}})
	}
}
```

> **Важно:** `runnerFor` теперь принимает `phase` параметр. Найди все вызовы `runnerFor(s)` в `orchestrator.go` и замени:
> - в `runPlanningAgent` → `runnerFor(s, "planning")`
> - в `runPlanningWithFeedback` → `runnerFor(s, "planning")` (revision is planning-phase)
> - в `runImplementationAgent` → `runnerFor(s, "implementation")` для основного запуска и `runnerFor(s, "review")` для отдельного review-запуска (review запускается отдельным вызовом `r.RunAgent` внутри метода — пересоздай runner перед ним). Это нужно чтобы review-агент тоже имел свою сессию claude и свой dialog.jsonl, если он окажется диалоговым.

- [x] **Step 5: Ветка StatusAwaitingUserInput в startPlanningForPending**

В функции `startPlanningForPending`, в основном switch (после изменения статуса при resume), добавить:

```go
case state.StatusAwaitingUserInput:
    // Stage was in dialog when flowManager crashed.
    // Restart the agent of the active phase via --resume.
    // MCP server will replay sealed Q/A pairs idempotently; open questions
    // re-block the tool call until user answers.
    go func(st flow.Stage) {
        sem := o.semFor(st)
        sem.acquire()
        defer sem.release()
        o.resumeInteractiveAgent(ctx, st)
    }(s)
```

- [x] **Step 6: resumeInteractiveAgent**

```go
// resumeInteractiveAgent re-runs the agent of the phase whose
// session.json exists most recently. The phase is detected by looking at
// mtimes of <phase>.session.json files in the stage directory.
func (o *Orchestrator) resumeInteractiveAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	phase := o.detectInterruptedPhase(stageDir)

	switch phase {
	case "planning":
		o.setStatus(s.ID, state.StatusPlanning)
		o.runPlanningAgent(ctx, s)
	case "review":
		// Review runs after implementation completes; if we see review session
		// open, the stage was paused inside review. Fall through to implementation
		// agent which will re-trigger review at the end.
		fallthrough
	default:
		o.setStatus(s.ID, state.StatusRunning)
		o.runImplementationAgent(ctx, s)
	}
}

func (o *Orchestrator) detectInterruptedPhase(stageDir string) string {
	var latestPhase string
	var latestMtime time.Time
	for _, phase := range []string{"planning", "implementation", "review"} {
		fi, err := os.Stat(sessionFile(stageDir, phase))
		if err != nil {
			continue
		}
		if fi.ModTime().After(latestMtime) {
			latestMtime = fi.ModTime()
			latestPhase = phase
		}
	}
	return latestPhase
}
```

- [x] **Step 7: Тест resumeInteractiveAgent**

В `pkg/orchestrator/orchestrator_test.go` добавить:

```go
func TestResumeInteractiveAgent_PlanningPhase(t *testing.T) {
	dir := t.TempDir()
	stageDir := filepath.Join(dir, "s1")
	os.MkdirAll(stageDir, 0755)
	os.WriteFile(filepath.Join(stageDir, "planning.session.json"), []byte(`{"session_id":"x"}`), 0644)

	stages := []flow.Stage{{ID: "s1", Agents: []flow.AgentType{flow.AgentPlanning}, Interactive: true}}
	rs := state.NewRunState([]string{"s1"})
	rs.SetStageStatus("s1", state.StatusAwaitingUserInput)
	stateFile := filepath.Join(dir, "state.json")
	rs.Save(stateFile)

	called := make(chan string, 1)
	runner := &fakeRunner{
		runPlanningFn: func(ctx context.Context, _, _, _, _ string) error {
			called <- "planning"
			return nil
		},
	}

	o := orchestrator.New(orchestrator.Options{
		RunDir: dir, Stages: stages, State: rs, StateFile: stateFile,
		Runner: runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go o.Run(ctx)

	select {
	case phase := <-called:
		if phase != "planning" {
			t.Errorf("expected planning, got %s", phase)
		}
	case <-ctx.Done():
		t.Fatal("planning runner was not called")
	}
}
```

Если в проекте уже есть `fakeRunner` — переиспользовать его. Если нет — добавить минимальный:

```go
type fakeRunner struct {
	runPlanningFn func(ctx context.Context, stageName, prompt, outFile, logFile string) error
	runAgentFn    func(ctx context.Context, agentType, stageName, prompt, logFile string) error
}

func (f *fakeRunner) RunPlanning(ctx context.Context, sn, p, of, lf string) error {
	if f.runPlanningFn != nil { return f.runPlanningFn(ctx, sn, p, of, lf) }
	return nil
}
func (f *fakeRunner) RunAgent(ctx context.Context, at, sn, p, lf string) error {
	if f.runAgentFn != nil { return f.runAgentFn(ctx, at, sn, p, lf) }
	return nil
}
```

- [x] **Step 8: Прогнать тесты**

```bash
go test ./pkg/orchestrator/ -v
make lint
```

- [x] **Step 9: Commit**

```bash
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/orchestrator_test.go
git commit -m "orchestrator: запуск interactive-агента и resume после рестарта

runnerFor теперь учитывает s.Interactive: для интерактивных стейджей
генерирует mcp.json и session.json для каждой фазы, передаёт исполнителю
--mcp-config и --session-id (или --resume при повторном запуске).
Новая ветка case StatusAwaitingUserInput запускает resumeInteractiveAgent,
который определяет прерванную фазу по mtime файлов сессии."
```

---

### Task 10: Подключение MCP-сервера в main.go

**Files:**
- Modify: `cmd/flowmanager/main.go`

> Ищется существующий код, который собирает `orchestrator.Options{}` и `server.Config{}`. Места могут немного отличаться от ожидаемых ниже — следуй фактической структуре файла.

- [x] **Step 1: Прочитать текущий main.go**

```bash
cat cmd/flowmanager/main.go | head -200
```

Найди блок, где создаются Orchestrator и Server.

- [x] **Step 2: Создать MCP-сервер и Notifier**

В main.go, ДО создания `orchestrator.New(...)`:

Сначала создаём оркестратор-stub для прокидывания EventBus в MCP:

```go
// Build orchestrator first to get access to its EventBus.
orch := orchestrator.New(orchestrator.Options{
    RunDir:    runDir,
    Stages:    flowFile.Stages,
    State:     rs,
    StateFile: stateFile,
    Config:    cfg,
    Prompts:   prompts,
    // DashboardURL will be filled in once server is listening
})

// MCP server uses orch's bus + state via Notifier.
mcpSrv := mcp.NewServer(runDir, orchestrator.NewMcpNotifier(orch))

// Server
srv := server.New(server.Config{
    Port:      cfg.Server.Port,
    RunDir:    runDir,
    StateFile: stateFile,
    Bus:       orch.Bus(),
    ApproveFn: orch.Approve,
    ReviseFn:  orch.Revise,
    RetryFn:   orch.Retry,
    McpServer: mcpSrv,
    DialogAnswerFn: func(stageID, phase, qID, answer string, fromOptions bool) error {
        return mcpSrv.NotifyAnswer(stageID, phase, qID, answer, fromOptions)
    },
    DialogCancelFn: func(stageID string) error {
        // Cancel waiters AND mark stage failed
        if err := mcpSrv.CancelStage(stageID); err != nil {
            return err
        }
        // Fail the stage via orchestrator
        orch.FailStage(stageID, "cancelled by user")
        return nil
    },
})

addr, err := srv.Start()
if err != nil { /* … */ }

// Now we know the actual listen address — set DashboardURL on orchestrator.
orch.SetDashboardURL("http://" + addr)

// Run orchestrator (existing code)
```

Если `orchestrator.Orchestrator` сейчас не имеет метода `SetDashboardURL` или `FailStage` — добавить их (тривиально):

```go
// In pkg/orchestrator/orchestrator.go
func (o *Orchestrator) SetDashboardURL(url string) { o.opts.DashboardURL = url }

func (o *Orchestrator) FailStage(stageID, reason string) {
	o.setStatus(stageID, state.StatusFailed)
	o.failBlockedStages()
}
```

- [x] **Step 3: Билд и smoke**

```bash
make build
./bin/flowmanager --help 2>&1 | head -10
```

Ожидается: сборка проходит, CLI помощь печатается.

- [x] **Step 4: Commit**

```bash
git add cmd/flowmanager/main.go pkg/orchestrator/orchestrator.go
git commit -m "cmd: подключить MCP-сервер и dialog-handlers в main

MCP-сервер монтируется в HTTP-mux, проксирует tools/call в waiters.
DashboardURL прокидывается в Orchestrator после server.Start().
Добавлены публичные методы SetDashboardURL и FailStage."
```

---

## Stage 5: Frontend

### Task 11: HTML и CSS секции «Диалог»

**Files:**
- Modify: `pkg/web/index.html`
- Modify: `pkg/web/style.css`

- [x] **Step 1: Добавить разметку в detail-content**

В `pkg/web/index.html`, внутри `<div id="detail-content">`, ПОСЛЕ `<div id="retry-section">` и ПЕРЕД блоком «Лог»:

```html
<div id="dialog-section" class="section hidden">
    <h3>Диалог</h3>

    <div id="dialog-history" class="dialog-history"></div>

    <div id="dialog-pending" class="dialog-pending hidden">
        <div class="dialog-question"></div>
        <div class="dialog-options"></div>
        <textarea class="dialog-custom" placeholder="Или вписать свой ответ…"></textarea>
        <div class="dialog-actions">
            <button class="btn btn-send">Отправить</button>
            <button class="btn btn-cancel-dialog">Отменить стейдж</button>
        </div>
    </div>

    <button id="dialog-toggle" class="dialog-toggle hidden">Свернуть историю ▴</button>
</div>
```

- [x] **Step 2: Стили + анимация**

В `pkg/web/style.css` добавить в конец файла:

```css
/* Dialog section */
.dialog-section { /* uses generic .section base */ }

.dialog-history {
    display: flex;
    flex-direction: column;
    gap: 12px;
    max-height: 320px;
    overflow-y: auto;
    margin-bottom: 16px;
}
.dialog-history.collapsed { display: none; }

.dialog-history .qa {
    border-left: 3px solid #888;
    padding-left: 10px;
}
.dialog-history .qa .q {
    font-weight: 500;
    color: #ddd;
    margin-bottom: 4px;
}
.dialog-history .qa .a {
    color: #9be39b;
    font-size: 0.95em;
}
.dialog-history .phase-divider {
    font-size: 0.75em;
    text-transform: uppercase;
    color: #777;
    border-bottom: 1px dashed #444;
    padding-bottom: 4px;
    margin-top: 8px;
}

.dialog-pending {
    background: #2a2f3a;
    border: 1px solid #7c5dff;
    border-radius: 6px;
    padding: 16px;
    margin-bottom: 12px;
    animation: dialogQuestionAppear 280ms ease-out;
}
.dialog-pending .dialog-question {
    font-size: 1.05em;
    margin-bottom: 12px;
    color: #fff;
}
.dialog-pending .dialog-options {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    margin-bottom: 12px;
}
.dialog-pending .dialog-options button {
    background: #3a4050;
    border: 1px solid #555;
    color: #ddd;
    padding: 6px 12px;
    border-radius: 4px;
    cursor: pointer;
    animation: dialogOptionAppear 240ms ease-out backwards;
}
.dialog-pending .dialog-options button.selected {
    background: #7c5dff;
    border-color: #7c5dff;
    color: #fff;
}
.dialog-pending .dialog-options.dimmed button:not(.selected) {
    opacity: 0.4;
}
.dialog-pending .dialog-custom {
    width: 100%;
    min-height: 60px;
    background: #1c2030;
    border: 1px solid #555;
    color: #ddd;
    padding: 8px;
    border-radius: 4px;
    font-family: inherit;
}
.dialog-pending .dialog-actions {
    display: flex;
    gap: 8px;
    margin-top: 8px;
}
.dialog-pending .btn-cancel-dialog {
    background: #5a2a2a;
}

@keyframes dialogQuestionAppear {
    from { transform: translateY(-8px); opacity: 0; }
    to   { transform: translateY(0); opacity: 1; }
}
@keyframes dialogOptionAppear {
    from { transform: translateY(4px); opacity: 0; }
    to   { transform: translateY(0); opacity: 1; }
}

/* Badge on stages-list */
.stages-list .dialog-badge {
    display: inline-block;
    margin-left: 6px;
    font-size: 0.9em;
}

/* Toggle */
.dialog-toggle {
    background: none;
    border: none;
    color: #888;
    cursor: pointer;
    font-size: 0.9em;
}
.dialog-toggle:hover { color: #ddd; }
```

- [x] **Step 3: Smoke — открой dashboard локально, убедись что layout не сломался**

```bash
make build
./bin/flowmanager run example-flow.yaml  # обычный неинтерактивный flow
# открой http://localhost:9876 — должно выглядеть как раньше; #dialog-section невидим
```

- [x] **Step 4: Commit**

```bash
git add pkg/web/index.html pkg/web/style.css
git commit -m "web: разметка и стили секции «Диалог»

Новая секция dialog-section в detail-panel — скрыта пока стейдж не
interactive. CSS-анимации появления вопроса и стаггерд-выезд кнопок-
вариантов. Бейдж 💬 в стайл-листе стейджей."
```

---

### Task 12: JavaScript — рендеринг и обработка событий

**Files:**
- Modify: `pkg/web/app.js`

- [x] **Step 1: Найти текущие DOM refs и WebSocket-handler**

```bash
grep -n "document.getElementById" pkg/web/app.js | head -30
grep -n "case \"" pkg/web/app.js | head -20
```

Изучи, как сейчас обрабатываются WS-события и обновляется UI после выбора стейджа.

- [x] **Step 2: Добавить DOM refs**

В блок `// ---- DOM refs ----` в начале app.js добавить:

```js
var $dialogSection   = document.getElementById("dialog-section");
var $dialogHistory   = document.getElementById("dialog-history");
var $dialogPending   = document.getElementById("dialog-pending");
var $dialogToggle    = document.getElementById("dialog-toggle");
```

Состояние:
```js
var dialogState = { pending: null }; // { id, phase, question, options, allow_custom }
var dialogEntries = [];              // array from GET /dialog
```

- [x] **Step 3: Loader, renderer**

Добавь функции (можно поместить рядом с другими helpers):

```js
function loadDialog(stageID) {
    apiGet("/api/stages/" + encodeURIComponent(stageID) + "/dialog", function(err, body) {
        if (err) { dialogEntries = []; renderDialog(stageID); return; }
        try { dialogEntries = JSON.parse(body); } catch (_) { dialogEntries = []; }
        renderDialog(stageID);
    });
}

function renderDialog(stageID) {
    // Determine if the stage uses dialog at all by checking if any entries exist
    // OR the stage is currently awaiting_user_input.
    var stageStatus = state && state.stages && state.stages[stageID] ? state.stages[stageID].status : "";
    var hasContent = dialogEntries.length > 0 || stageStatus === "awaiting_user_input";

    if (!hasContent) {
        $dialogSection.classList.add("hidden");
        return;
    }
    $dialogSection.classList.remove("hidden");

    // History: render Q/A pairs grouped by phase
    $dialogHistory.innerHTML = "";
    var currentPhase = "";
    dialogEntries.forEach(function(e) {
        if (e.phase !== currentPhase) {
            var div = document.createElement("div");
            div.className = "phase-divider";
            div.textContent = e.phase;
            $dialogHistory.appendChild(div);
            currentPhase = e.phase;
        }
        if (e.answer !== undefined && e.answer !== null) {
            // closed pair — render Q + A
            var qa = document.createElement("div");
            qa.className = "qa";
            qa.innerHTML = "<div class='q'>" + escapeHTML(e.question) + "</div>" +
                           "<div class='a'>→ " + escapeHTML(e.answer) + "</div>";
            $dialogHistory.appendChild(qa);
        }
    });

    // Pending: last open entry across all phases (if any)
    var open = null;
    for (var i = dialogEntries.length - 1; i >= 0; i--) {
        if (dialogEntries[i].answer === undefined || dialogEntries[i].answer === null) {
            open = dialogEntries[i];
            break;
        }
    }
    if (open) {
        renderPendingQuestion(stageID, open);
    } else {
        $dialogPending.classList.add("hidden");
        dialogState.pending = null;
    }
}

function renderPendingQuestion(stageID, q) {
    dialogState.pending = { stageID: stageID, id: q.id, phase: q.phase, allowCustom: q.allow_custom };

    $dialogPending.classList.remove("hidden");
    $dialogPending.querySelector(".dialog-question").textContent = q.question;

    var $opts = $dialogPending.querySelector(".dialog-options");
    $opts.innerHTML = "";
    $opts.classList.remove("dimmed");
    var selected = null;

    (q.options || []).forEach(function(opt, idx) {
        var btn = document.createElement("button");
        btn.type = "button";
        btn.textContent = opt;
        btn.style.animationDelay = (idx * 40) + "ms";
        btn.onclick = function() {
            selected = opt;
            Array.from($opts.querySelectorAll("button")).forEach(function(b) { b.classList.remove("selected"); });
            btn.classList.add("selected");
            $opts.classList.remove("dimmed");
            $dialogPending.querySelector(".dialog-custom").value = "";
        };
        $opts.appendChild(btn);
    });

    var $custom = $dialogPending.querySelector(".dialog-custom");
    $custom.value = "";
    $custom.disabled = !q.allow_custom;
    $custom.oninput = function() {
        if ($custom.value.length > 0) {
            $opts.classList.add("dimmed");
            Array.from($opts.querySelectorAll("button")).forEach(function(b) { b.classList.remove("selected"); });
            selected = null;
        } else {
            $opts.classList.remove("dimmed");
        }
    };

    $dialogPending.querySelector(".btn-send").onclick = function() {
        var customText = $custom.value.trim();
        var answer, fromOptions;
        if (customText.length > 0) {
            answer = customText;
            fromOptions = false;
        } else if (selected !== null) {
            answer = selected;
            fromOptions = true;
        } else {
            return; // nothing selected
        }
        sendAnswer(stageID, q.id, q.phase, answer, fromOptions);
    };

    $dialogPending.querySelector(".btn-cancel-dialog").onclick = function() {
        if (!confirm("Отменить стейдж?")) return;
        cancelDialog(stageID);
    };
}

function sendAnswer(stageID, qID, phase, answer, fromOptions) {
    apiPost("/api/stages/" + encodeURIComponent(stageID) + "/dialog/answer", {
        id: qID, phase: phase, answer: answer, from_options: fromOptions
    }, function(err) {
        if (err) { alert("Ошибка отправки: " + err.message); return; }
        $dialogPending.classList.add("hidden");
        loadDialog(stageID);
    });
}

function cancelDialog(stageID) {
    apiPost("/api/stages/" + encodeURIComponent(stageID) + "/dialog/cancel", null, function(err) {
        if (err) alert("Ошибка отмены: " + err.message);
    });
}
```

- [x] **Step 4: WebSocket-обработчики**

Найди существующий блок `switch (ev.type)` или эквивалент в обработке `message`-события WS. Добавь две ветки:

```js
case "ask_user":
    if (ev.stage_id === selectedStageID) {
        loadDialog(ev.stage_id);
    }
    // refresh stages-list to show badge
    fetchStatus();
    break;

case "user_answered":
    if (ev.stage_id === selectedStageID) {
        loadDialog(ev.stage_id);
    }
    fetchStatus();
    break;
```

(Имена `fetchStatus` подставь по факту — функция, которая перечитывает `/api/status` и перерисовывает список стейджей. Если её нет — взять обработчик из существующего status-changed.)

- [x] **Step 5: Загружать диалог при выборе стейджа**

Найди функцию, которая обрабатывает клик по стейджу (типа `selectStage(id)`). В конец добавить:

```js
loadDialog(id);
```

И в render-функции списка стейджей — рядом с именем стейджа, если его статус `awaiting_user_input`, добавить:

```js
if (stageStatus === "awaiting_user_input") {
    var badge = document.createElement("span");
    badge.className = "dialog-badge";
    badge.textContent = "💬";
    li.appendChild(badge);
}
```

- [x] **Step 6: Тогл истории**

```js
$dialogToggle.onclick = function() {
    $dialogHistory.classList.toggle("collapsed");
    $dialogToggle.textContent = $dialogHistory.classList.contains("collapsed")
        ? "Развернуть историю ▾"
        : "Свернуть историю ▴";
};
```

Кнопка `$dialogToggle` показывается, когда есть >0 закрытых пар:

В `renderDialog` после рендера истории добавить:
```js
var closed = dialogEntries.filter(function(e) { return e.answer !== undefined && e.answer !== null; });
if (closed.length > 0) {
    $dialogToggle.classList.remove("hidden");
} else {
    $dialogToggle.classList.add("hidden");
}
```

- [x] **Step 7: Smoke**

```bash
make build
./bin/flowmanager run example-flow.yaml
# открой dashboard — выбери стейдж — секция диалога должна быть скрыта
# никаких JS-ошибок в консоли
```

Открой DevTools → Console и убедись что нет ошибок.

- [x] **Step 8: Commit**

```bash
git add pkg/web/app.js
git commit -m "web: рендеринг диалога + обработка ask_user/user_answered

Подгружает /api/stages/<id>/dialog при выборе стейджа и по WS-событиям.
Анимированно отображает текущий вопрос с вариантами и полем кастомного
ответа. Кнопки-варианты затемняются при вводе кастомного текста."
```

---

## Stage 6: Integration tests и smoke

### Task 13: Integration test — полный диалоговый цикл через fake-runner

**Files:**
- Modify: `pkg/orchestrator/integration_test.go`

- [x] **Step 1: Изучить fakeRunner**

```bash
grep -n "fakeRunner\|FakeRunner" pkg/orchestrator/integration_test.go | head -10
```

Если в файле уже есть fake-runner, переиспользовать. Если нет — определить как в Task 9 Step 7.

- [x] **Step 2: Добавить тест**

В `pkg/orchestrator/integration_test.go` добавить:

```go
func TestFullDialogCycle(t *testing.T) {
	dir := t.TempDir()
	runDir := dir

	stages := []flow.Stage{
		{
			ID: "discovery", Name: "Discovery", Description: "ask user",
			Agents:      []flow.AgentType{flow.AgentImplementation},
			Plan:        "",
			Interactive: true,
			Artifacts: []flow.Artifact{
				{Name: "out", Path: "./out.txt", Description: "result"},
			},
		},
	}
	// Pre-create stage dir + a fake plan.md so implementation can start
	stageDir := filepath.Join(runDir, "discovery")
	os.MkdirAll(stageDir, 0755)
	os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# plan"), 0644)

	rs := state.NewRunState([]string{"discovery"})
	rs.SetStageStatus("discovery", state.StatusReady)
	stateFile := filepath.Join(runDir, "state.json")
	rs.Save(stateFile)

	// Fake runner: simulates agent calling ask_user via the MCP server.
	// We'll provoke this by making the runner POST to the MCP server when called.
	o := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, State: rs, StateFile: stateFile,
		DashboardURL: "http://127.0.0.1:0", // will be overwritten
	})
	mcpSrv := mcp.NewServer(runDir, orchestrator.NewMcpNotifier(o))

	srv := httptest.NewServer(mcpSrv)
	defer srv.Close()
	o.SetDashboardURL(srv.URL)

	// Replace runner with one that POSTs to MCP, then writes the artifact and exits
	runner := &interactiveFakeRunner{
		mcpURL: srv.URL + "/mcp/discovery/implementation",
		artifactWriter: func() {
			os.WriteFile(filepath.Join(stageDir, "out.txt"), []byte("done"), 0644)
			os.WriteFile(filepath.Join(stageDir, ".done"), []byte(""), 0644)
		},
	}
	// inject runner — option-level injection or test helper, depending on existing pattern

	// Run orchestrator in background
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go o.Run(ctx)

	// Wait until stage transitions to awaiting_user_input
	waitForStatus(t, stateFile, "discovery", state.StatusAwaitingUserInput, 2*time.Second)

	// Simulate UI answer
	dialogPath := filepath.Join(stageDir, "implementation.dialog.jsonl")
	mcp.AppendAnswer(dialogPath, mcp.Answer{ID: "q1", Answer: "go for it", FromOptions: false})
	mcpSrv.NotifyAnswer("discovery", "implementation", "q1", "go for it", false)

	// Wait for done
	waitForStatus(t, stateFile, "discovery", state.StatusDone, 2*time.Second)
}

// interactiveFakeRunner mimics an agent that does one ask_user then finishes.
type interactiveFakeRunner struct {
	mcpURL          string
	artifactWriter  func()
}

func (r *interactiveFakeRunner) RunPlanning(ctx context.Context, _, _, _, _ string) error { return nil }
func (r *interactiveFakeRunner) RunAgent(ctx context.Context, _, _, _, _ string) error {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_user","arguments":{"id":"q1","question":"go ahead?"}}}`
	resp, err := http.Post(r.mcpURL, "application/json", strings.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	r.artifactWriter()
	return nil
}

func waitForStatus(t *testing.T, stateFile, stageID string, want state.StageStatus, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		rs, err := state.Load(stateFile)
		if err == nil {
			if rs.Stages[stageID].Status == want {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("stage %s did not reach %s within %s", stageID, want, timeout)
}
```

> **Important:** Runner-injection в Options сейчас поддерживается через поле `Runner executor.Runner`. Если оно есть — `Options{Runner: runner}`. Если нет — добавить поле или использовать существующий механизм.

- [x] **Step 3: Прогнать**

```bash
go test ./pkg/orchestrator/ -run TestFullDialogCycle -v -race
```

Ожидается: PASS. Тест может потребовать тюнинга таймингов.

- [x] **Step 4: Commit**

```bash
git add pkg/orchestrator/integration_test.go
git commit -m "test: интеграционный тест полного диалогового цикла

Стейдж с interactive: true; fake-runner вызывает ask_user через
MCP-сервер, тест симулирует ответ пользователя через NotifyAnswer
и проверяет переход running → awaiting_user_input → done."
```

---

### Task 14: Integration test — crash mid-dialog и resume

**Files:**
- Modify: `pkg/orchestrator/integration_test.go`

- [x] **Step 1: Добавить тест**

```go
func TestResumeAfterCrash(t *testing.T) {
	dir := t.TempDir()
	stageDir := filepath.Join(dir, "discovery")
	os.MkdirAll(stageDir, 0755)
	os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# plan"), 0644)

	// Pre-populate: agent asked q1, didn't get answer before crash
	dialogPath := filepath.Join(stageDir, "implementation.dialog.jsonl")
	mcp.AppendQuestion(dialogPath, mcp.Question{ID: "q1", Question: "x?"})
	// Pre-populate: session.json exists (simulating prior run)
	os.WriteFile(filepath.Join(stageDir, "implementation.session.json"),
		[]byte(`{"session_id":"test-uuid"}`), 0644)

	stages := []flow.Stage{
		{ID: "discovery", Name: "D", Description: "d",
			Agents:      []flow.AgentType{flow.AgentImplementation},
			Interactive: true,
		},
	}
	rs := state.NewRunState([]string{"discovery"})
	rs.SetStageStatus("discovery", state.StatusAwaitingUserInput)
	stateFile := filepath.Join(dir, "state.json")
	rs.Save(stateFile)

	// User had already provided an answer (simulating: user answered, but
	// flowManager crashed before noticing — the answer is in the file).
	mcp.AppendAnswer(dialogPath, mcp.Answer{ID: "q1", Answer: "after restart"})

	resumedSession := ""
	runner := &fakeRunner{
		runAgentFn: func(ctx context.Context, _, _, _, _ string) error {
			// On resume, the executor should have been built with --resume=<sid>
			// — but at this layer we can't directly inspect that. Instead, we
			// just record that the agent was invoked, then "complete":
			os.WriteFile(filepath.Join(stageDir, ".done"), []byte(""), 0644)
			resumedSession = "called"
			return nil
		},
	}
	_ = runner

	o := orchestrator.New(orchestrator.Options{
		RunDir: dir, Stages: stages, State: rs, StateFile: stateFile,
		DashboardURL: "http://127.0.0.1:0",
		// Runner: runner,
	})

	mcpSrv := mcp.NewServer(dir, orchestrator.NewMcpNotifier(o))
	srv := httptest.NewServer(mcpSrv)
	defer srv.Close()
	o.SetDashboardURL(srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go o.Run(ctx)

	waitForStatus(t, stateFile, "discovery", state.StatusDone, 2*time.Second)
	if resumedSession != "called" {
		t.Error("agent was not invoked on resume")
	}
}
```

- [x] **Step 2: Прогнать**

```bash
go test ./pkg/orchestrator/ -run TestResumeAfterCrash -v
```

- [x] **Step 3: Commit**

```bash
git add pkg/orchestrator/integration_test.go
git commit -m "test: resume стейджа в awaiting_user_input после рестарта

Симулирует ситуацию: стейдж был в awaiting_user_input, ответ
пользователя уже был сохранён в dialog.jsonl, но процесс flowManager
рестартовал. На старте оркестратор замечает статус awaiting_user_input,
запускает resumeInteractiveAgent, агент видит готовый ответ через MCP
replay и доводит стейдж до done."
```

---

### Task 15: Manual smoke-test с реальным claude

**Files:**
- Create: `example-flow-interactive.yaml`

- [x] **Step 1: Создать пример**

Создать `example-flow-interactive.yaml`:

```yaml
name: interactive-demo
description: "Demo интерактивного стейджа"

stages:
  - id: discovery
    name: "Сбор требований"
    description: |
      Спроси у пользователя:
      1. Какой язык программирования предпочитает (предложи Go, Python, TypeScript).
      2. Какой стиль архитектуры (предложи monolith, microservices, "не знаю").

      После двух ответов запиши итог в ./summary.md в формате:
        # Сводка
        - Язык: <ответ>
        - Архитектура: <ответ>

      Используй tool ask_user с id вопросов q1, q2.
      Когда summary.md записан — заверши работу.
    agents: [implementation]
    interactive: true
    artifacts:
      - name: summary
        path: ./summary.md
        description: "Сводка ответов"
```

- [x] **Step 2: Запустить**

```bash
make build
./bin/flowmanager run example-flow-interactive.yaml
```

Ожидается: открывается dashboard. Стейдж discovery становится running, затем awaiting_user_input. В detail-panel стейджа появляется секция «Диалог» с первым вопросом и тремя вариантами.

- [x] **Step 3: Ответить на оба вопроса через UI**

- Кликни на «Go» → нажми «Отправить»
- На втором вопросе впиши свой текст «event-driven» → «Отправить»

Ожидается: после двух ответов стейдж переходит в done, summary.md создаётся в stage-директории с правильным содержимым.

- [x] **Step 4: Smoke test для restart**

Запусти ещё раз:
```bash
./bin/flowmanager run example-flow-interactive.yaml
```

В момент, когда показался первый вопрос — Ctrl+C.

Снова:
```bash
./bin/flowmanager run example-flow-interactive.yaml
```

Ожидается: на странице стейджа сразу показан первый вопрос (тот же id, тот же текст), без дублирования в истории.

- [x] **Step 5: Commit example**

```bash
git add example-flow-interactive.yaml
git commit -m "example: интерактивный flow для smoke-теста

Простой demo-flow с одним стейджем discovery (interactive: true),
который задаёт два вопроса и записывает summary.md. Используется
для ручной проверки end-to-end диалогового цикла."
```

---

## Stage 7: Финальная проверка

### Task 16: Полный прогон тестов и линта

- [x] **Step 1: Всё**

```bash
make test
make lint
make build
```

Все три должны пройти без ошибок.

- [x] **Step 2: Прогон существующего example-flow.yaml**

```bash
./bin/flowmanager run example-flow.yaml
```

Стейджи без `interactive: true` должны работать как раньше — никакой регрессии. Никаких dialog-секций, никаких MCP-конфигов в stage-директориях.

- [x] **Step 3: Финальный коммит, если есть незакоммиченные изменения**

```bash
git status
# если что-то не закоммичено — изучи, доделай или сбрось
```

---

## Done criteria

Реализация считается завершённой когда:

1. `make test` проходит, включая все новые тесты.
2. `make lint` без warning.
3. `make build` собирает бинарник без ошибок.
4. Существующий `example-flow.yaml` работает идентично прежнему (отсутствие регрессии).
5. `example-flow-interactive.yaml` проходит smoke-тест: оба вопроса задаются через UI, ответы доходят до агента, артефакт записывается, стейдж переходит в done.
6. Smoke-тест restart: Ctrl+C во время диалога → перезапуск → диалог продолжается с того же вопроса без дублей.
