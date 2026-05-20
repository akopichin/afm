# Event-driven Redesign: flowManager v2 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Переписать flowManager на event-driven архитектуру с FSM, добавить веб-дашборд с полным UI (approve/revise из браузера), освободить главный контекст Claude Code от поллинга.

**Architecture:** Event-driven оркестратор с FSM на уровне стадии + EventBus для уведомлений. HTTP-сервер с WebSocket встроен в `flowmanager run`. Скиллы Claude Code переписаны на fire-and-forget. Executor вынесен за интерфейс Runner для тестируемости.

**Tech Stack:** Go 1.26, cobra, gorilla/websocket, embed, vanilla JS

---

## File Structure

```
pkg/
  orchestrator/
    fsm.go              — FSM: статусы, переходы, валидация (НОВЫЙ)
    fsm_test.go         — тесты FSM (НОВЫЙ)
    eventbus.go         — EventBus: pub/sub для событий (НОВЫЙ)
    eventbus_test.go    — тесты EventBus (НОВЫЙ)
    orchestrator.go     — event-driven run loop (ПЕРЕПИСАТЬ)
    orchestrator_test.go — тесты оркестратора (ПЕРЕПИСАТЬ)
    integration_test.go  — интеграционные тесты (ПЕРЕПИСАТЬ)
    graph.go            — без изменений
    graph_test.go       — без изменений
  executor/
    runner.go           — Runner интерфейс (НОВЫЙ)
    executor.go         — добавить реализацию Runner (ОБНОВИТЬ)
    executor_test.go    — без изменений
  state/
    state.go            — добавить StatusRevising, feedback поля (ОБНОВИТЬ)
    state_test.go       — обновить тесты (ОБНОВИТЬ)
  server/
    server.go           — HTTP сервер, роутинг (НОВЫЙ)
    server_test.go      — тесты сервера (НОВЫЙ)
    handlers.go         — HTTP handlers для API (НОВЫЙ)
    handlers_test.go    — тесты handlers (НОВЫЙ)
    websocket.go        — WebSocket hub (НОВЫЙ)
    websocket_test.go   — тесты WebSocket (НОВЫЙ)
  web/
    index.html          — дашборд UI (НОВЫЙ, через frontend-design)
    style.css           — стили (НОВЫЙ, через frontend-design)
    app.js              — клиентская логика (НОВЫЙ, через frontend-design)
cmd/
  flowmanager/
    run.go              — обновить: запуск сервера + оркестратора (ОБНОВИТЬ)
    revise.go           — обновить: сохранять feedback в файл (ОБНОВИТЬ)
    check.go            — обновить: расширенный вывод (ОБНОВИТЬ)
config/
    config.go           — добавить ServerConfig (ОБНОВИТЬ)
assets/
  claude/skills/
    flowmanager/SKILL.md         — переписать (fire-and-forget) (ОБНОВИТЬ)
    flowmanager-check/SKILL.md   — обновить (ОБНОВИТЬ)
    flowmanager-review/SKILL.md  — новый скилл (НОВЫЙ)
    flowmanager-monitor/         — УДАЛИТЬ
```

---

### Task 1: FSM — статусы и переходы стадий

**Files:**
- Create: `pkg/orchestrator/fsm.go`
- Create: `pkg/orchestrator/fsm_test.go`
- Modify: `pkg/state/state.go:14-22` — добавить `StatusRevising`
- Modify: `pkg/state/state_test.go`

- [ ] **Step 1: Добавить StatusRevising в state.go**

В файле `pkg/state/state.go` добавить новый статус:

```go
const (
	StatusPending          StageStatus = "pending"
	StatusPlanning         StageStatus = "planning"
	StatusAwaitingApproval StageStatus = "awaiting_approval"
	StatusRevising         StageStatus = "revising"
	StatusReady            StageStatus = "ready"
	StatusRunning          StageStatus = "running"
	StatusDone             StageStatus = "done"
	StatusFailed           StageStatus = "failed"
)
```

- [ ] **Step 2: Написать тесты FSM переходов**

Создать файл `pkg/orchestrator/fsm_test.go`:

```go
package orchestrator

import (
	"testing"

	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/state"
)

func TestFSM_ValidTransitions(t *testing.T) {
	cases := []struct {
		from state.StageStatus
		to   state.StageStatus
	}{
		{state.StatusPending, state.StatusPlanning},
		{state.StatusPlanning, state.StatusAwaitingApproval},
		{state.StatusPlanning, state.StatusFailed},
		{state.StatusAwaitingApproval, state.StatusReady},
		{state.StatusAwaitingApproval, state.StatusRevising},
		{state.StatusRevising, state.StatusPlanning},
		{state.StatusReady, state.StatusRunning},
		{state.StatusRunning, state.StatusDone},
		{state.StatusRunning, state.StatusFailed},
	}
	for _, c := range cases {
		if !ValidTransition(c.from, c.to) {
			t.Errorf("expected valid: %s → %s", c.from, c.to)
		}
	}
}

func TestFSM_InvalidTransitions(t *testing.T) {
	cases := []struct {
		from state.StageStatus
		to   state.StageStatus
	}{
		{state.StatusPending, state.StatusDone},
		{state.StatusDone, state.StatusRunning},
		{state.StatusReady, state.StatusPlanning},
		{state.StatusRunning, state.StatusReady},
		{state.StatusAwaitingApproval, state.StatusRunning},
	}
	for _, c := range cases {
		if ValidTransition(c.from, c.to) {
			t.Errorf("expected invalid: %s → %s", c.from, c.to)
		}
	}
}

func TestFSM_IsTerminal(t *testing.T) {
	if !IsTerminal(state.StatusDone) {
		t.Error("done should be terminal")
	}
	if !IsTerminal(state.StatusFailed) {
		t.Error("failed should be terminal")
	}
	if IsTerminal(state.StatusRunning) {
		t.Error("running should not be terminal")
	}
}
```

- [ ] **Step 3: Запустить тесты — убедиться что падают**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -run TestFSM -v`
Expected: FAIL — `ValidTransition` и `IsTerminal` не определены.

- [ ] **Step 4: Реализовать FSM**

Создать файл `pkg/orchestrator/fsm.go`:

```go
package orchestrator

import "gitlab.ae-rus.net/bx/ai-flow-manager/pkg/state"

// validTransitions задаёт допустимые переходы FSM стадии.
var validTransitions = map[state.StageStatus][]state.StageStatus{
	state.StatusPending:          {state.StatusPlanning},
	state.StatusPlanning:         {state.StatusAwaitingApproval, state.StatusFailed},
	state.StatusAwaitingApproval: {state.StatusReady, state.StatusRevising},
	state.StatusRevising:         {state.StatusPlanning},
	state.StatusReady:            {state.StatusRunning},
	state.StatusRunning:          {state.StatusDone, state.StatusFailed},
}

// ValidTransition проверяет допустимость перехода.
func ValidTransition(from, to state.StageStatus) bool {
	targets, ok := validTransitions[from]
	if !ok {
		return false
	}
	for _, t := range targets {
		if t == to {
			return true
		}
	}
	return false
}

// IsTerminal возвращает true для финальных статусов.
func IsTerminal(s state.StageStatus) bool {
	return s == state.StatusDone || s == state.StatusFailed
}
```

- [ ] **Step 5: Запустить тесты — убедиться что проходят**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -run TestFSM -v`
Expected: PASS

- [ ] **Step 6: Коммит**

```bash
git add pkg/orchestrator/fsm.go pkg/orchestrator/fsm_test.go pkg/state/state.go
git commit -m "feat: FSM переходы стадий с валидацией и статусом revising"
```

---

### Task 2: EventBus — pub/sub для событий

**Files:**
- Create: `pkg/orchestrator/eventbus.go`
- Create: `pkg/orchestrator/eventbus_test.go`

- [ ] **Step 1: Написать тесты EventBus**

Создать `pkg/orchestrator/eventbus_test.go`:

```go
package orchestrator

import (
	"sync"
	"testing"
	"time"
)

func TestEventBus_PublishAndSubscribe(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	sub := bus.Subscribe()
	bus.Publish(Event{Type: EventStageStatusChanged, StageID: "s1"})

	select {
	case ev := <-sub:
		if ev.Type != EventStageStatusChanged || ev.StageID != "s1" {
			t.Errorf("unexpected event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestEventBus_MultipleSubscribers(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	sub1 := bus.Subscribe()
	sub2 := bus.Subscribe()

	bus.Publish(Event{Type: EventAgentCompleted, StageID: "s1"})

	for _, sub := range []<-chan Event{sub1, sub2} {
		select {
		case ev := <-sub:
			if ev.StageID != "s1" {
				t.Errorf("unexpected: %+v", ev)
			}
		case <-time.After(time.Second):
			t.Fatal("timeout")
		}
	}
}

func TestEventBus_Unsubscribe(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	sub := bus.Subscribe()
	bus.Unsubscribe(sub)

	bus.Publish(Event{Type: EventStageStatusChanged, StageID: "s1"})

	select {
	case <-sub:
		t.Fatal("should not receive after unsubscribe")
	case <-time.After(50 * time.Millisecond):
		// ok
	}
}

func TestEventBus_ConcurrentPublish(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	sub := bus.Subscribe()
	n := 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			bus.Publish(Event{Type: EventAgentAction, StageID: "s1", Data: i})
		}()
	}

	received := 0
	done := make(chan struct{})
	go func() {
		for range sub {
			received++
			if received == n {
				close(done)
				return
			}
		}
	}()

	wg.Wait()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("received only %d/%d events", received, n)
	}
}
```

- [ ] **Step 2: Запустить тесты — убедиться что падают**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -run TestEventBus -v`
Expected: FAIL

- [ ] **Step 3: Реализовать EventBus**

Создать `pkg/orchestrator/eventbus.go`:

```go
package orchestrator

import "sync"

// EventType — тип события.
type EventType string

const (
	EventStageStatusChanged EventType = "stage_status_changed"
	EventAgentAction        EventType = "agent_action"
	EventAgentCompleted     EventType = "agent_completed"
	EventFeedbackReceived   EventType = "feedback_received"
	EventApproved           EventType = "approved"
	EventRevised            EventType = "revised"
)

// Event — событие в системе.
type Event struct {
	Type    EventType `json:"type"`
	StageID string    `json:"stage_id"`
	Data    any       `json:"data,omitempty"`
}

// EventBus — pub/sub шина событий.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[<-chan Event]chan Event
	closed      bool
}

// NewEventBus создаёт EventBus.
func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[<-chan Event]chan Event),
	}
}

// Subscribe возвращает канал для чтения событий.
func (b *EventBus) Subscribe() <-chan Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan Event, 64)
	b.subscribers[ch] = ch
	return ch
}

// Unsubscribe удаляет подписчика.
func (b *EventBus) Unsubscribe(ch <-chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if c, ok := b.subscribers[ch]; ok {
		delete(b.subscribers, ch)
		close(c)
	}
}

// Publish отправляет событие всем подписчикам.
func (b *EventBus) Publish(ev Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return
	}
	for _, ch := range b.subscribers {
		select {
		case ch <- ev:
		default:
			// подписчик не успевает — пропускаем
		}
	}
}

// Close закрывает все каналы подписчиков.
func (b *EventBus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for key, ch := range b.subscribers {
		delete(b.subscribers, key)
		close(ch)
	}
}
```

- [ ] **Step 4: Запустить тесты — убедиться что проходят**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -run TestEventBus -v`
Expected: PASS

- [ ] **Step 5: Коммит**

```bash
git add pkg/orchestrator/eventbus.go pkg/orchestrator/eventbus_test.go
git commit -m "feat: EventBus — pub/sub шина событий для оркестратора"
```

---

### Task 3: Runner интерфейс для Executor

**Files:**
- Create: `pkg/executor/runner.go`
- Modify: `pkg/executor/executor.go` — добавить соответствие интерфейсу

- [ ] **Step 1: Создать Runner интерфейс**

Создать `pkg/executor/runner.go`:

```go
package executor

import "context"

// Runner — интерфейс для запуска AI-агентов. Позволяет подменять для тестов.
type Runner interface {
	RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error
	RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error
}

// compile-time check
var _ Runner = (*Executor)(nil)
```

- [ ] **Step 2: Убедиться что компилируется**

Run: `cd /Users/alexander.kopichin/work/flowManager && go build ./pkg/executor/`
Expected: OK (Executor уже реализует оба метода)

- [ ] **Step 3: Коммит**

```bash
git add pkg/executor/runner.go
git commit -m "feat: Runner интерфейс для подмены executor в тестах"
```

---

### Task 4: Обновить конфиг — ServerConfig

**Files:**
- Modify: `pkg/config/config.go:13-30`
- Modify: `pkg/config/config_test.go`

- [ ] **Step 1: Написать тест для ServerConfig**

Добавить в `pkg/config/config_test.go` (прочитать файл и дописать):

```go
func TestServerConfigDefaults(t *testing.T) {
	cfg := Default()
	if cfg.Server.Port != 9876 {
		t.Errorf("default port: got %d, want 9876", cfg.Server.Port)
	}
	if !cfg.Server.OpenBrowser {
		t.Error("default open_browser should be true")
	}
}

func TestServerConfigOverride(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.yaml")
	os.WriteFile(configFile, []byte("server:\n  port: 8080\n  open_browser: false\n"), 0644)

	cfg, err := LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("port: got %d, want 8080", cfg.Server.Port)
	}
	if cfg.Server.OpenBrowser {
		t.Error("open_browser should be false")
	}
}

func TestServerPortZeroDisablesServer(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.yaml")
	os.WriteFile(configFile, []byte("server:\n  port: 0\n"), 0644)

	cfg, err := LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 0 {
		t.Errorf("port should be 0 when explicitly set: got %d", cfg.Server.Port)
	}
}
```

- [ ] **Step 2: Запустить тесты — убедиться что падают**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/config/ -run TestServer -v`
Expected: FAIL — `cfg.Server` не существует

- [ ] **Step 3: Добавить ServerConfig**

В `pkg/config/config.go` добавить:

```go
// ServerConfig configures the built-in web dashboard.
type ServerConfig struct {
	Port        int  `yaml:"port"`
	OpenBrowser bool `yaml:"open_browser"`
}
```

Обновить `Config`:

```go
type Config struct {
	Client     ClientConfig   `yaml:"client"`
	Executor   ExecutorConfig `yaml:"executor"`
	Server     ServerConfig   `yaml:"server"`
	PromptsDir string         `yaml:"prompts_dir"`
}
```

Обновить `Default()`:

```go
func Default() Config {
	return Config{
		Client:   ClientConfig{Command: "claude"},
		Executor: ExecutorConfig{IdleTimeout: 30 * time.Minute, MaxParallel: 0},
		Server:   ServerConfig{Port: 9876, OpenBrowser: true},
	}
}
```

Добавить merge-логику в `mergeFile`:

```go
if overlay.Server.Port != 0 {
	dst.Server.Port = overlay.Server.Port
}
// open_browser: нужно отдельное поле чтобы отличить "не указано" от "false"
// Используем указатель или отдельную проверку через yaml raw.
// Простое решение: всегда мёрджить если файл содержит server секцию.
```

Примечание: для `open_browser: false` потребуется `*bool` или raw yaml проверка. Простейший вариант — использовать `*bool`:

```go
type ServerConfig struct {
	Port        int   `yaml:"port"`
	OpenBrowser *bool `yaml:"open_browser"`
}
```

И в Default:

```go
openBrowser := true
Server: ServerConfig{Port: 9876, OpenBrowser: &openBrowser},
```

Merge:

```go
if overlay.Server.Port != 0 {
	dst.Server.Port = overlay.Server.Port
}
if overlay.Server.OpenBrowser != nil {
	dst.Server.OpenBrowser = overlay.Server.OpenBrowser
}
```

Хелпер для чтения:

```go
// IsOpenBrowser возвращает значение OpenBrowser (по умолчанию true).
func (s ServerConfig) IsOpenBrowser() bool {
	if s.OpenBrowser == nil {
		return true
	}
	return *s.OpenBrowser
}
```

- [ ] **Step 4: Запустить тесты — убедиться что проходят**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/config/ -v`
Expected: PASS

- [ ] **Step 5: Коммит**

```bash
git add pkg/config/config.go pkg/config/config_test.go
git commit -m "feat: ServerConfig для веб-дашборда (port, open_browser)"
```

---

### Task 5: State — feedback файл и версионирование планов

**Files:**
- Modify: `pkg/state/state.go`
- Modify: `pkg/state/state_test.go`

- [ ] **Step 1: Написать тесты для feedback и версионирования**

Добавить в `pkg/state/state_test.go`:

```go
func TestSaveFeedback(t *testing.T) {
	dir := t.TempDir()
	stageDir := filepath.Join(dir, "s1")
	os.MkdirAll(stageDir, 0755)

	err := SaveFeedback(stageDir, "Добавь Redis")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	if !strings.Contains(string(data), "Добавь Redis") {
		t.Errorf("feedback not saved: %q", string(data))
	}

	// Второй фидбэк — дописывается
	err = SaveFeedback(stageDir, "Ещё TTL")
	if err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	content := string(data)
	if !strings.Contains(content, "Добавь Redis") || !strings.Contains(content, "Ещё TTL") {
		t.Errorf("second feedback not appended: %q", content)
	}
	if !strings.Contains(content, "revision 2") {
		t.Errorf("missing revision separator: %q", content)
	}
}

func TestVersionPlan(t *testing.T) {
	dir := t.TempDir()
	stageDir := filepath.Join(dir, "s1")
	os.MkdirAll(stageDir, 0755)
	planFile := filepath.Join(stageDir, "plan.md")
	os.WriteFile(planFile, []byte("# Plan v1"), 0644)

	n, err := VersionPlan(stageDir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("version: got %d, want 1", n)
	}

	// plan.md переименован в plan.v1.md
	if _, err := os.Stat(filepath.Join(stageDir, "plan.v1.md")); err != nil {
		t.Error("plan.v1.md should exist")
	}
	// plan.md больше не существует
	if _, err := os.Stat(planFile); !os.IsNotExist(err) {
		t.Error("plan.md should be removed after versioning")
	}
}
```

- [ ] **Step 2: Запустить тесты — убедиться что падают**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/state/ -run "TestSaveFeedback|TestVersionPlan" -v`
Expected: FAIL

- [ ] **Step 3: Реализовать SaveFeedback и VersionPlan**

Добавить в `pkg/state/state.go`:

```go
// SaveFeedback дописывает фидбэк в feedback.md стадии с разделителем.
func SaveFeedback(stageDir, feedback string) error {
	fbFile := filepath.Join(stageDir, "feedback.md")

	n := 1
	existing, err := os.ReadFile(fbFile)
	if err == nil {
		n = strings.Count(string(existing), "--- revision ") + 1
	}

	separator := fmt.Sprintf("\n--- revision %d | %s ---\n",
		n, time.Now().Format("2006-01-02 15:04"))

	f, err := os.OpenFile(fbFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open feedback file: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(separator + feedback + "\n"); err != nil {
		return fmt.Errorf("write feedback: %w", err)
	}
	return nil
}

// VersionPlan переименовывает plan.md в plan.v{N}.md и возвращает N.
func VersionPlan(stageDir string) (int, error) {
	planFile := filepath.Join(stageDir, "plan.md")
	if _, err := os.Stat(planFile); err != nil {
		return 0, fmt.Errorf("plan.md not found: %w", err)
	}

	n := 1
	for {
		versionedPath := filepath.Join(stageDir, fmt.Sprintf("plan.v%d.md", n))
		if _, err := os.Stat(versionedPath); os.IsNotExist(err) {
			break
		}
		n++
	}

	dst := filepath.Join(stageDir, fmt.Sprintf("plan.v%d.md", n))
	if err := os.Rename(planFile, dst); err != nil {
		return 0, fmt.Errorf("rename plan: %w", err)
	}
	return n, nil
}
```

Добавить импорты `"strings"` и `"time"` в state.go если ещё нет.

- [ ] **Step 4: Запустить тесты**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/state/ -v`
Expected: PASS

- [ ] **Step 5: Коммит**

```bash
git add pkg/state/state.go pkg/state/state_test.go
git commit -m "feat: SaveFeedback и VersionPlan для механизма ревизий"
```

---

### Task 6: Event-driven оркестратор — переписать Run

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go` — полная переработка
- Modify: `pkg/orchestrator/orchestrator_test.go` — обновить тесты

Это самая большая задача. Оркестратор переходит от `PlanningPhase/WaitForApprovals/ImplementationPhase` к единому event loop.

- [ ] **Step 1: Написать тест единого Run loop**

Переписать `pkg/orchestrator/orchestrator_test.go`:

```go
package orchestrator_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/config"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/executor"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/flow"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/orchestrator"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/state"
)

// mockRunner имитирует выполнение агентов без реального AI-клиента.
type mockRunner struct {
	planContent string
	delay       time.Duration
}

func (m *mockRunner) RunPlanning(_ context.Context, _, _, outFile, logFile string) error {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	os.MkdirAll(filepath.Dir(outFile), 0755)
	os.WriteFile(outFile, []byte(m.planContent), 0644)
	os.WriteFile(logFile, []byte("planning log\n"), 0644)
	return nil
}

func (m *mockRunner) RunAgent(_ context.Context, agentType, _, _, logFile string) error {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	os.WriteFile(logFile, []byte(fmt.Sprintf("%s log\n", agentType)), 0644)
	return nil
}

func TestOrchestrator_RunSingleStage(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{
		{ID: "s1", Name: "Stage 1", Description: "do stuff",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	rs := state.NewRunState([]string{"s1"})
	stateFile := filepath.Join(runDir, "state.json")
	rs.Save(stateFile)

	runner := &mockRunner{planContent: "# Plan\n- step 1"}

	orch := orchestrator.New(orchestrator.Options{
		RunDir:    runDir,
		Stages:    stages,
		State:     rs,
		StateFile: stateFile,
		Config:    config.Default(),
		Prompts:   orchestrator.DefaultPrompts(),
		Runner:    runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Запускаем Run в горутине — он будет ждать approval
	done := make(chan error, 1)
	go func() { done <- orch.Run(ctx) }()

	// Ждём awaiting_approval
	waitForStatus(t, stateFile, "s1", state.StatusAwaitingApproval, 5*time.Second)

	// Approve
	orch.Approve("s1")

	// Ждём завершения
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for Run to complete")
	}

	final, _ := state.Load(stateFile)
	if final.Stages["s1"].Status != state.StatusDone {
		t.Errorf("expected done, got %v", final.Stages["s1"].Status)
	}
}

// waitForStatus поллит state.json пока стадия не достигнет нужного статуса.
func waitForStatus(t *testing.T, stateFile, stageID string, want state.StageStatus, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		rs, err := state.Load(stateFile)
		if err == nil {
			if s, ok := rs.Stages[stageID]; ok && s.Status == want {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for stage %s to reach %s", stageID, want)
}
```

- [ ] **Step 2: Запустить тесты — убедиться что падают**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -run TestOrchestrator_RunSingleStage -v`
Expected: FAIL — `Options.Runner` не существует, `orch.Run()` не существует, `orch.Approve()` не существует.

- [ ] **Step 3: Переписать orchestrator.go**

Заменить содержимое `pkg/orchestrator/orchestrator.go`:

```go
package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/config"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/executor"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/flow"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/state"
)

// Prompts holds the prompt templates for each agent type.
type Prompts struct {
	Planning       string
	Implementation string
	Review         string
	Summary        string
}

// DefaultPrompts returns empty prompts (will be set from assets).
func DefaultPrompts() Prompts { return Prompts{} }

// Options configures an Orchestrator.
type Options struct {
	RunDir    string
	Stages    []flow.Stage
	State     *state.RunState
	StateFile string
	Config    config.Config
	Prompts   Prompts
	Runner    executor.Runner // nil = реальный Executor
}

// Orchestrator manages the full lifecycle of a flow run via event loop.
type Orchestrator struct {
	opts  Options
	graph *Graph
	runner executor.Runner
	bus   *EventBus
	mu    sync.Mutex
}

// New creates an Orchestrator.
func New(opts Options) *Orchestrator {
	r := opts.Runner
	if r == nil {
		r = executor.New(executor.Config{
			Command:     opts.Config.Client.Command,
			ExtraArgs:   opts.Config.Client.ExtraArgs,
			IdleTimeout: opts.Config.Executor.IdleTimeout,
		})
	}
	return &Orchestrator{
		opts:   opts,
		graph:  NewGraph(opts.Stages),
		runner: r,
		bus:    NewEventBus(),
	}
}

// Bus возвращает EventBus для внешних подписчиков (сервер, WebSocket).
func (o *Orchestrator) Bus() *EventBus { return o.bus }

// Run запускает event-driven цикл оркестратора.
func (o *Orchestrator) Run(ctx context.Context) error {
	events := o.bus.Subscribe()
	defer o.bus.Unsubscribe(events)

	// Запустить planning для стадий без зависимостей
	o.startPlanningForPending(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-events:
			if err := o.handleEvent(ctx, ev); err != nil {
				return err
			}
			if o.allTerminal() {
				return nil
			}
		}
	}
}

// Approve одобряет план стадии.
func (o *Orchestrator) Approve(stageID string) {
	o.bus.Publish(Event{Type: EventApproved, StageID: stageID})
}

// Revise отправляет фидбэк для перепланирования стадии.
func (o *Orchestrator) Revise(stageID, feedback string) {
	o.bus.Publish(Event{Type: EventRevised, StageID: stageID, Data: feedback})
}

func (o *Orchestrator) handleEvent(ctx context.Context, ev Event) error {
	switch ev.Type {
	case EventAgentCompleted:
		return o.onAgentCompleted(ctx, ev)
	case EventApproved:
		return o.onApproved(ctx, ev)
	case EventRevised:
		return o.onRevised(ctx, ev)
	}
	return nil
}

func (o *Orchestrator) onAgentCompleted(ctx context.Context, ev Event) error {
	agentType, _ := ev.Data.(string)
	stageID := ev.StageID

	switch agentType {
	case "planning":
		o.setStatus(stageID, state.StatusAwaitingApproval)
	case "implementation":
		o.setStatus(stageID, state.StatusDone)
		o.startReadyStages(ctx)
	case "review":
		// review не меняет статус, оно после implementation
	}
	return nil
}

func (o *Orchestrator) onApproved(ctx context.Context, ev Event) error {
	o.setStatus(ev.StageID, state.StatusReady)
	o.startReadyStages(ctx)
	return nil
}

func (o *Orchestrator) onRevised(ctx context.Context, ev Event) error {
	stageID := ev.StageID
	feedback, _ := ev.Data.(string)

	o.setStatus(stageID, state.StatusRevising)

	stageDir := filepath.Join(o.opts.RunDir, stageID)
	if _, err := state.VersionPlan(stageDir); err != nil {
		return fmt.Errorf("version plan for %s: %w", stageID, err)
	}
	if err := state.SaveFeedback(stageDir, feedback); err != nil {
		return fmt.Errorf("save feedback for %s: %w", stageID, err)
	}

	o.setStatus(stageID, state.StatusPlanning)

	stage := o.graph.Stage(stageID)
	go o.runPlanningWithFeedback(ctx, *stage)
	return nil
}

// startPlanningForPending запускает planning для всех pending стадий без зависимостей.
func (o *Orchestrator) startPlanningForPending(ctx context.Context) {
	for _, s := range o.opts.Stages {
		if !s.NeedsPlanning() {
			stageDir := filepath.Join(o.opts.RunDir, s.ID)
			os.MkdirAll(stageDir, 0755)
			dst := filepath.Join(stageDir, "plan.md")
			if err := copyFile(s.Plan, dst); err != nil {
				o.setStatus(s.ID, state.StatusFailed)
				continue
			}
			o.setStatus(s.ID, state.StatusReady)
			continue
		}

		o.mu.Lock()
		current := o.opts.State.Stages[s.ID].Status
		o.mu.Unlock()

		if current != state.StatusPending {
			continue
		}

		o.setStatus(s.ID, state.StatusPlanning)
		go o.runPlanningAgent(ctx, s)
	}
}

// startReadyStages запускает implementation для стадий чьи зависимости выполнены.
func (o *Orchestrator) startReadyStages(ctx context.Context) {
	o.mu.Lock()
	statuses := make(map[string]state.StageStatus, len(o.opts.State.Stages))
	for id, s := range o.opts.State.Stages {
		statuses[id] = s.Status
	}
	o.mu.Unlock()

	ready := o.graph.ReadyStages(statuses)
	for _, id := range ready {
		stage := o.graph.Stage(id)
		o.setStatus(id, state.StatusRunning)
		go o.runImplementationAgent(ctx, *stage)
	}
}

func (o *Orchestrator) runPlanningAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	os.MkdirAll(stageDir, 0755)

	prompt := buildPlanningPrompt(o.opts.Prompts.Planning, s)
	outFile := filepath.Join(stageDir, "plan.md")
	logFile := filepath.Join(stageDir, "planning.log")

	err := o.runner.RunPlanning(ctx, s.Name, prompt, outFile, logFile)
	if err != nil {
		o.setStatus(s.ID, state.StatusFailed)
		return
	}

	o.bus.Publish(Event{Type: EventAgentCompleted, StageID: s.ID, Data: "planning"})
}

func (o *Orchestrator) runPlanningWithFeedback(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)

	feedbackData, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	// Читаем последнюю версию плана для контекста
	var prevPlan string
	entries, _ := os.ReadDir(stageDir)
	for _, e := range entries {
		if matched, _ := filepath.Match("plan.v*.md", e.Name()); matched {
			data, _ := os.ReadFile(filepath.Join(stageDir, e.Name()))
			prevPlan = string(data) // берём последнюю
		}
	}

	prompt := buildRevisionPrompt(o.opts.Prompts.Planning, s, prevPlan, string(feedbackData))
	outFile := filepath.Join(stageDir, "plan.md")
	logFile := filepath.Join(stageDir, fmt.Sprintf("planning-revision.log"))

	err := o.runner.RunPlanning(ctx, s.Name, prompt, outFile, logFile)
	if err != nil {
		o.setStatus(s.ID, state.StatusFailed)
		return
	}

	o.bus.Publish(Event{Type: EventAgentCompleted, StageID: s.ID, Data: "planning"})
}

func (o *Orchestrator) runImplementationAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	planData, err := os.ReadFile(filepath.Join(stageDir, "plan.md"))
	if err != nil {
		o.setStatus(s.ID, state.StatusFailed)
		return
	}

	prompt := buildImplementationPrompt(o.opts.Prompts.Implementation, s, string(planData))
	logFile := filepath.Join(stageDir, "implementation.log")

	if err := o.runner.RunAgent(ctx, "implementation", s.Name, prompt, logFile); err != nil {
		o.setStatus(s.ID, state.StatusFailed)
		return
	}

	if s.HasAgent(flow.AgentReview) {
		reviewPrompt := buildReviewPrompt(o.opts.Prompts.Review, s)
		reviewLog := filepath.Join(stageDir, "review.log")
		if err := o.runner.RunAgent(ctx, "review", s.Name, reviewPrompt, reviewLog); err != nil {
			o.setStatus(s.ID, state.StatusFailed)
			return
		}
	}

	o.bus.Publish(Event{Type: EventAgentCompleted, StageID: s.ID, Data: "implementation"})
}

func (o *Orchestrator) allTerminal() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, s := range o.opts.State.Stages {
		if !IsTerminal(s.Status) {
			return false
		}
	}
	return len(o.opts.State.Stages) > 0
}

func (o *Orchestrator) setStatus(id string, status state.StageStatus) {
	o.mu.Lock()
	o.opts.State.SetStageStatus(id, status)
	o.opts.State.Save(o.opts.StateFile)
	o.mu.Unlock()

	o.bus.Publish(Event{Type: EventStageStatusChanged, StageID: id, Data: string(status)})
}

func buildPlanningPrompt(template string, s flow.Stage) string {
	extra := ""
	if len(s.Skills) > 0 {
		extra = "\n\nUse these skills: " + joinStrings(s.Skills)
	}
	return fmt.Sprintf("%s\n\n## Stage: %s\n\n%s%s", template, s.Name, s.Description, extra)
}

func buildRevisionPrompt(template string, s flow.Stage, prevPlan, feedback string) string {
	extra := ""
	if len(s.Skills) > 0 {
		extra = "\n\nUse these skills: " + joinStrings(s.Skills)
	}
	return fmt.Sprintf(
		"%s\n\n## Stage: %s\n\n%s%s\n\n## Previous plan (needs revision)\n\n%s\n\n## Feedback\n\n%s\n\nRevise the plan according to the feedback above.",
		template, s.Name, s.Description, extra, prevPlan, feedback,
	)
}

func buildImplementationPrompt(template string, s flow.Stage, plan string) string {
	extra := ""
	if len(s.Skills) > 0 {
		extra = "\n\nUse these skills: " + joinStrings(s.Skills)
	}
	return fmt.Sprintf("%s\n\n## Stage: %s\n\n## Plan\n\n%s%s", template, s.Name, plan, extra)
}

func buildReviewPrompt(template string, s flow.Stage) string {
	return fmt.Sprintf("%s\n\n## Stage: %s\n\n%s", template, s.Name, s.Description)
}

func buildSummaryPrompt(template, runDir string, stages []flow.Stage) string {
	names := make([]string, len(stages))
	for i, s := range stages {
		names[i] = s.Name
	}
	return fmt.Sprintf("%s\n\nRun directory: %s\nStages: %s", template, runDir, joinStrings(names))
}

func joinStrings(ss []string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += ", "
		}
		result += s
	}
	return result
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}
```

- [ ] **Step 4: Запустить тесты**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -run TestOrchestrator_RunSingleStage -v -timeout 30s`
Expected: PASS

- [ ] **Step 5: Коммит**

```bash
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/orchestrator_test.go
git commit -m "feat: event-driven оркестратор с единым Run loop"
```

---

### Task 7: Обновить интеграционные тесты оркестратора

**Files:**
- Modify: `pkg/orchestrator/integration_test.go`

- [ ] **Step 1: Переписать интеграционные тесты под новый API**

Переписать `pkg/orchestrator/integration_test.go` с использованием `mockRunner` и `orch.Run()`:

```go
package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/config"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/flow"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/orchestrator"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/state"
)

func setupOrchestratorV2(t *testing.T, stages []flow.Stage, runner *mockRunner) (*orchestrator.Orchestrator, string, string) {
	t.Helper()
	runDir := t.TempDir()
	stageIDs := make([]string, len(stages))
	for i, s := range stages {
		stageIDs[i] = s.ID
	}
	rs := state.NewRunState(stageIDs)
	stateFile := filepath.Join(runDir, "state.json")
	rs.Save(stateFile)

	orch := orchestrator.New(orchestrator.Options{
		RunDir:    runDir,
		Stages:    stages,
		State:     rs,
		StateFile: stateFile,
		Config:    config.Default(),
		Prompts:   orchestrator.DefaultPrompts(),
		Runner:    runner,
	})
	return orch, runDir, stateFile
}

func TestIntegration_FullSingleStage(t *testing.T) {
	stages := []flow.Stage{
		{ID: "backend", Name: "Backend", Description: "implement backend",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}
	runner := &mockRunner{planContent: "# Plan\n- step 1"}
	orch, runDir, stateFile := setupOrchestratorV2(t, stages, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- orch.Run(ctx) }()

	waitForStatus(t, stateFile, "backend", state.StatusAwaitingApproval, 5*time.Second)
	orch.Approve("backend")

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout")
	}

	final, _ := state.Load(stateFile)
	if final.Stages["backend"].Status != state.StatusDone {
		t.Errorf("expected done, got %v", final.Stages["backend"].Status)
	}

	// plan.md существует
	if _, err := os.Stat(filepath.Join(runDir, "backend", "plan.md")); err != nil {
		t.Errorf("plan.md not found: %v", err)
	}
}

func TestIntegration_TwoParallelStages(t *testing.T) {
	stages := []flow.Stage{
		{ID: "alpha", Name: "Alpha", Description: "first", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
		{ID: "beta", Name: "Beta", Description: "second", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}
	runner := &mockRunner{planContent: "# Plan\n- step 1"}
	orch, _, stateFile := setupOrchestratorV2(t, stages, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- orch.Run(ctx) }()

	waitForStatus(t, stateFile, "alpha", state.StatusAwaitingApproval, 5*time.Second)
	waitForStatus(t, stateFile, "beta", state.StatusAwaitingApproval, 5*time.Second)
	orch.Approve("alpha")
	orch.Approve("beta")

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout")
	}

	final, _ := state.Load(stateFile)
	for _, id := range []string{"alpha", "beta"} {
		if final.Stages[id].Status != state.StatusDone {
			t.Errorf("stage %s: expected done, got %v", id, final.Stages[id].Status)
		}
	}
}

func TestIntegration_SequentialDependencies(t *testing.T) {
	stages := []flow.Stage{
		{ID: "first", Name: "First", Description: "first", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
		{ID: "second", Name: "Second", Description: "after first", DependsOn: []string{"first"}, Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}
	runner := &mockRunner{planContent: "# Plan\n- step 1"}
	orch, _, stateFile := setupOrchestratorV2(t, stages, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- orch.Run(ctx) }()

	// first планируется, second ждёт
	waitForStatus(t, stateFile, "first", state.StatusAwaitingApproval, 5*time.Second)
	orch.Approve("first")

	// После завершения first, second начнёт planning
	waitForStatus(t, stateFile, "second", state.StatusAwaitingApproval, 10*time.Second)
	orch.Approve("second")

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout")
	}

	final, _ := state.Load(stateFile)
	if final.Stages["first"].Status != state.StatusDone {
		t.Errorf("first: expected done, got %v", final.Stages["first"].Status)
	}
	if final.Stages["second"].Status != state.StatusDone {
		t.Errorf("second: expected done, got %v", final.Stages["second"].Status)
	}
}

func TestIntegration_ReviseAndReplan(t *testing.T) {
	stages := []flow.Stage{
		{ID: "s1", Name: "Stage 1", Description: "do stuff",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}
	runner := &mockRunner{planContent: "# Plan\n- step 1"}
	orch, runDir, stateFile := setupOrchestratorV2(t, stages, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- orch.Run(ctx) }()

	// Первый план
	waitForStatus(t, stateFile, "s1", state.StatusAwaitingApproval, 5*time.Second)

	// Revise с фидбэком
	orch.Revise("s1", "Добавь больше деталей")

	// Ждём нового плана
	waitForStatus(t, stateFile, "s1", state.StatusAwaitingApproval, 10*time.Second)

	// Проверяем что plan.v1.md создан
	if _, err := os.Stat(filepath.Join(runDir, "s1", "plan.v1.md")); err != nil {
		t.Error("plan.v1.md should exist after revise")
	}

	// Проверяем что feedback.md создан
	if _, err := os.Stat(filepath.Join(runDir, "s1", "feedback.md")); err != nil {
		t.Error("feedback.md should exist after revise")
	}

	// Approve ревизию
	orch.Approve("s1")

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout")
	}

	final, _ := state.Load(stateFile)
	if final.Stages["s1"].Status != state.StatusDone {
		t.Errorf("expected done, got %v", final.Stages["s1"].Status)
	}
}

func TestIntegration_PreExistingPlan(t *testing.T) {
	planDir := t.TempDir()
	planFile := filepath.Join(planDir, "existing.md")
	os.WriteFile(planFile, []byte("# Pre-existing Plan\n"), 0644)

	stages := []flow.Stage{
		{ID: "ready", Name: "Ready", Description: "has plan", Plan: planFile,
			Agents: []flow.AgentType{flow.AgentImplementation}},
	}
	runner := &mockRunner{planContent: "unused"}
	orch, _, stateFile := setupOrchestratorV2(t, stages, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Pre-existing plan → сразу ready → сразу running → done
	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final, _ := state.Load(stateFile)
	if final.Stages["ready"].Status != state.StatusDone {
		t.Errorf("expected done, got %v", final.Stages["ready"].Status)
	}
}
```

- [ ] **Step 2: Запустить все тесты оркестратора**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/orchestrator/ -v -timeout 60s`
Expected: PASS

- [ ] **Step 3: Коммит**

```bash
git add pkg/orchestrator/integration_test.go
git commit -m "test: интеграционные тесты под event-driven оркестратор"
```

---

### Task 8: HTTP сервер и API

**Files:**
- Create: `pkg/server/server.go`
- Create: `pkg/server/handlers.go`
- Create: `pkg/server/server_test.go`
- Create: `pkg/server/handlers_test.go`

- [ ] **Step 1: Написать тесты HTTP handlers**

Создать `pkg/server/handlers_test.go`:

```go
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/orchestrator"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/state"
)

func setupTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "s1")
	os.MkdirAll(stageDir, 0755)
	os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Test Plan"), 0644)
	os.WriteFile(filepath.Join(stageDir, "planning.log"), []byte("test log line\n"), 0644)

	rs := state.NewRunState([]string{"s1"})
	rs.SetStageStatus("s1", state.StatusAwaitingApproval)
	stateFile := filepath.Join(runDir, "state.json")
	rs.Save(stateFile)

	bus := orchestrator.NewEventBus()
	srv := &Server{
		runDir:    runDir,
		stateFile: stateFile,
		bus:       bus,
		approveFn: func(id string) {},
		reviseFn:  func(id, fb string) {},
	}
	return srv, runDir
}

func TestHandleStatus(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	var rs state.RunState
	json.NewDecoder(w.Body).Decode(&rs)
	if _, ok := rs.Stages["s1"]; !ok {
		t.Error("stage s1 missing from status")
	}
}

func TestHandlePlan(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/stages/s1/plan", nil)
	w := httptest.NewRecorder()
	srv.handlePlan(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "# Test Plan") {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

func TestHandleLog(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/stages/s1/log", nil)
	w := httptest.NewRecorder()
	srv.handleLog(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "test log line") {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

func TestHandleApprove(t *testing.T) {
	approved := ""
	srv, _ := setupTestServer(t)
	srv.approveFn = func(id string) { approved = id }

	req := httptest.NewRequest("POST", "/api/stages/s1/approve", nil)
	w := httptest.NewRecorder()
	srv.handleApprove(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	if approved != "s1" {
		t.Errorf("approve not called with s1, got %q", approved)
	}
}

func TestHandleRevise(t *testing.T) {
	var revisedID, revisedFB string
	srv, _ := setupTestServer(t)
	srv.reviseFn = func(id, fb string) { revisedID = id; revisedFB = fb }

	body := `{"feedback":"Добавь Redis"}`
	req := httptest.NewRequest("POST", "/api/stages/s1/revise", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleRevise(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	if revisedID != "s1" || revisedFB != "Добавь Redis" {
		t.Errorf("revise: id=%q fb=%q", revisedID, revisedFB)
	}
}
```

- [ ] **Step 2: Запустить тесты — убедиться что падают**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/server/ -v`
Expected: FAIL — пакет не существует

- [ ] **Step 3: Реализовать handlers.go**

Создать `pkg/server/handlers.go`:

```go
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/state"
)

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	rs, err := state.Load(s.stateFile)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rs)
}

func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	stageID := s.extractStageID(r.URL.Path, "/api/stages/", "/plan")
	planFile := filepath.Join(s.runDir, stageID, "plan.md")
	data, err := os.ReadFile(planFile)
	if err != nil {
		http.Error(w, fmt.Sprintf("plan not found: %v", err), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Write(data)
}

func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	stageID := s.extractStageID(r.URL.Path, "/api/stages/", "/log")
	stageDir := filepath.Join(s.runDir, stageID)

	// Попробовать прочитать самый свежий лог
	var logContent string
	for _, name := range []string{"implementation.log", "planning.log"} {
		data, err := os.ReadFile(filepath.Join(stageDir, name))
		if err == nil {
			logContent += string(data)
		}
	}
	if logContent == "" {
		http.Error(w, "no logs found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, logContent)
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	stageID := s.extractStageID(r.URL.Path, "/api/stages/", "/approve")
	s.approveFn(stageID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "approved", "stage_id": stageID})
}

type reviseRequest struct {
	Feedback string `json:"feedback"`
}

func (s *Server) handleRevise(w http.ResponseWriter, r *http.Request) {
	stageID := s.extractStageID(r.URL.Path, "/api/stages/", "/revise")
	var req reviseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Feedback == "" {
		http.Error(w, "feedback is required", http.StatusBadRequest)
		return
	}
	s.reviseFn(stageID, req.Feedback)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "revised", "stage_id": stageID})
}

func (s *Server) extractStageID(path, prefix, suffix string) string {
	path = strings.TrimPrefix(path, prefix)
	path = strings.TrimSuffix(path, suffix)
	return path
}
```

- [ ] **Step 4: Реализовать server.go**

Создать `pkg/server/server.go`:

```go
package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"

	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/orchestrator"
)

// Server — HTTP-сервер для дашборда и API.
type Server struct {
	runDir    string
	stateFile string
	bus       *orchestrator.EventBus
	approveFn func(stageID string)
	reviseFn  func(stageID, feedback string)
	httpSrv   *http.Server
}

// Config настройки сервера.
type Config struct {
	Port      int
	RunDir    string
	StateFile string
	Bus       *orchestrator.EventBus
	ApproveFn func(stageID string)
	ReviseFn  func(stageID, feedback string)
}

// New создаёт сервер.
func New(cfg Config) *Server {
	s := &Server{
		runDir:    cfg.RunDir,
		stateFile: cfg.StateFile,
		bus:       cfg.Bus,
		approveFn: cfg.ApproveFn,
		reviseFn:  cfg.ReviseFn,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	// Маршруты для стадий — используем prefix matching
	mux.HandleFunc("/api/stages/", s.routeStages)
	mux.HandleFunc("/ws", s.handleWebSocket)
	// TODO: статика дашборда (Task 10)

	s.httpSrv = &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: mux,
	}
	return s
}

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
	default:
		http.NotFound(w, r)
	}
}

// Start запускает HTTP-сервер в горутине. Возвращает фактический адрес.
func (s *Server) Start() (string, error) {
	ln, err := net.Listen("tcp", s.httpSrv.Addr)
	if err != nil {
		return "", fmt.Errorf("listen: %w", err)
	}
	go s.httpSrv.Serve(ln)
	return ln.Addr().String(), nil
}

// Shutdown gracefully останавливает сервер.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}
```

- [ ] **Step 5: Запустить тесты**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/server/ -v`
Expected: PASS (WebSocket handler пока заглушка — вернёт 501)

- [ ] **Step 6: Коммит**

```bash
git add pkg/server/
git commit -m "feat: HTTP сервер и API для дашборда (status, plan, log, approve, revise)"
```

---

### Task 9: WebSocket hub

**Files:**
- Create: `pkg/server/websocket.go`
- Create: `pkg/server/websocket_test.go`
- Modify: `go.mod` — добавить `gorilla/websocket`

- [ ] **Step 1: Добавить зависимость gorilla/websocket**

Run: `cd /Users/alexander.kopichin/work/flowManager && go get github.com/gorilla/websocket`

- [ ] **Step 2: Написать тесты WebSocket**

Создать `pkg/server/websocket_test.go`:

```go
package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/orchestrator"
)

func TestWebSocket_ReceivesEvents(t *testing.T) {
	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv.httpSrv.Handler)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Публикуем событие
	srv.bus.Publish(orchestrator.Event{
		Type:    orchestrator.EventStageStatusChanged,
		StageID: "s1",
		Data:    "awaiting_approval",
	})

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(msg), "s1") {
		t.Errorf("unexpected message: %s", msg)
	}
}
```

- [ ] **Step 3: Реализовать websocket.go**

Создать `pkg/server/websocket.go`:

```go
package server

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/gorilla/websocket"

	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/orchestrator"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade: %v", err)
		return
	}
	defer conn.Close()

	events := s.bus.Subscribe()
	defer s.bus.Unsubscribe(events)

	for ev := range events {
		data, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			return
		}
	}
}
```

- [ ] **Step 4: Обновить server.go — убрать заглушку handleWebSocket если была**

Метод `handleWebSocket` уже зарегистрирован в `mux.HandleFunc("/ws", s.handleWebSocket)` — теперь он реализован в websocket.go.

- [ ] **Step 5: Запустить тесты**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/server/ -v`
Expected: PASS

- [ ] **Step 6: Коммит**

```bash
git add pkg/server/websocket.go pkg/server/websocket_test.go go.mod go.sum
git commit -m "feat: WebSocket hub для real-time событий дашборда"
```

---

### Task 10: Веб-дашборд UI (frontend-design)

**Files:**
- Create: `pkg/web/index.html`
- Create: `pkg/web/style.css`
- Create: `pkg/web/app.js`
- Create: `pkg/web/embed.go`
- Modify: `pkg/server/server.go` — подключить статику

**Эта задача выполняется через `frontend-design` скилл.**

- [ ] **Step 1: Вызвать frontend-design скилл**

Использовать скилл `frontend-design` для создания дашборда со следующими требованиями:

Дашборд flowManager — single-page application (vanilla JS, без фреймворков).

Функционал:
- Левая панель: список стадий с цветными индикаторами статуса (pending=серый, planning=синий, awaiting_approval=жёлтый, revising=оранжевый, ready=голубой, running=синий пульсирующий, done=зелёный, failed=красный)
- Правая панель: детали стадии — план (markdown rendered), кнопки Approve/Revise, textarea для фидбэка, live-лог
- Нижняя строка: общий прогресс, время запуска, elapsed
- WebSocket подключение к `/ws` для real-time обновлений
- API: GET /api/status, GET /api/stages/:id/plan, GET /api/stages/:id/log, POST /api/stages/:id/approve, POST /api/stages/:id/revise
- Тёмная тема, минималистичный стиль
- Markdown рендеринг через простой парсер (без библиотек) или встроить marked.js через CDN

Технические ограничения:
- Всё в 3 файлах: index.html, style.css, app.js
- Нет npm, бандлеров, фреймворков
- Embedded в Go binary через `//go:embed`

- [ ] **Step 2: Создать embed.go**

Создать `pkg/web/embed.go`:

```go
package web

import "embed"

//go:embed index.html style.css app.js
var FS embed.FS
```

- [ ] **Step 3: Подключить статику в server.go**

Добавить в `pkg/server/server.go` в конструктор `New`:

```go
import "gitlab.ae-rus.net/bx/ai-flow-manager/pkg/web"

// В New(), после регистрации API маршрутов:
mux.Handle("/", http.FileServer(http.FS(web.FS)))
```

- [ ] **Step 4: Проверить что дашборд открывается**

Run: `cd /Users/alexander.kopichin/work/flowManager && go build ./pkg/server/ && go build ./pkg/web/`
Expected: OK

- [ ] **Step 5: Коммит**

```bash
git add pkg/web/ pkg/server/server.go
git commit -m "feat: веб-дашборд UI (frontend-design)"
```

---

### Task 11: Обновить cmd/flowmanager/run.go — запуск сервера

**Files:**
- Modify: `cmd/flowmanager/run.go`

- [ ] **Step 1: Переписать run.go**

Заменить содержимое `cmd/flowmanager/run.go`:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"gitlab.ae-rus.net/bx/ai-flow-manager/assets"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/config"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/flow"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/orchestrator"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/server"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/state"
)

func newRunCmd() *cobra.Command {
	var maxParallel int
	var idleTimeout time.Duration
	var port int

	cmd := &cobra.Command{
		Use:   "run [flow.yaml]",
		Short: "Run a flow (or resume the latest run)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if maxParallel > 0 {
				cfg.Executor.MaxParallel = maxParallel
			}
			if idleTimeout > 0 {
				cfg.Executor.IdleTimeout = idleTimeout
			}
			if cmd.Flags().Changed("port") {
				cfg.Server.Port = port
			}

			flowPath, err := resolveFlowPath(args)
			if err != nil {
				return err
			}

			f, err := flow.ParseFile(flowPath)
			if err != nil {
				return fmt.Errorf("parse flow: %w", err)
			}

			prompts, err := loadPrompts(cfg.PromptsDir)
			if err != nil {
				return err
			}

			runDir, rs, stateFile, err := resolveRun(f)
			if err != nil {
				return err
			}

			fmt.Printf("flowmanager: running %q\n", f.Name)
			fmt.Printf("  run dir: %s\n", runDir)

			orch := orchestrator.New(orchestrator.Options{
				RunDir:    runDir,
				Stages:    f.Stages,
				State:     rs,
				StateFile: stateFile,
				Config:    cfg,
				Prompts:   prompts,
			})

			// Запустить HTTP сервер если порт > 0
			if cfg.Server.Port > 0 {
				srv := server.New(server.Config{
					Port:      cfg.Server.Port,
					RunDir:    runDir,
					StateFile: stateFile,
					Bus:       orch.Bus(),
					ApproveFn: orch.Approve,
					ReviseFn:  orch.Revise,
				})
				addr, err := srv.Start()
				if err != nil {
					return fmt.Errorf("start dashboard: %w", err)
				}
				defer srv.Shutdown(context.Background())

				dashURL := fmt.Sprintf("http://%s", addr)
				fmt.Printf("  dashboard: %s\n", dashURL)
				if cfg.Server.IsOpenBrowser() {
					openBrowser(dashURL)
				}
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			if err := orch.Run(ctx); err != nil {
				return fmt.Errorf("run: %w", err)
			}

			fmt.Printf("flowmanager: flow %q completed\n", f.Name)
			return nil
		},
	}

	cmd.Flags().IntVar(&maxParallel, "max-parallel", 0, "max parallel stages (0=unlimited)")
	cmd.Flags().DurationVar(&idleTimeout, "idle-timeout", 0, "agent idle timeout")
	cmd.Flags().IntVar(&port, "port", 0, "dashboard port (0=use config)")
	return cmd
}

func openBrowser(url string) {
	var cmd string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "linux":
		cmd = "xdg-open"
	default:
		return
	}
	exec.Command(cmd, url).Start()
}
```

Оставить `resolveFlowPath`, `resolveRun`, `loadPrompts` без изменений (они уже определены в файле).

- [ ] **Step 2: Убедиться что компилируется**

Run: `cd /Users/alexander.kopichin/work/flowManager && go build ./cmd/flowmanager/`
Expected: OK

- [ ] **Step 3: Коммит**

```bash
git add cmd/flowmanager/run.go
git commit -m "feat: запуск HTTP-дашборда вместе с flowmanager run"
```

---

### Task 12: Обновить cmd/flowmanager/revise.go — сохранять feedback в файл

**Files:**
- Modify: `cmd/flowmanager/revise.go`

- [ ] **Step 1: Обновить revise.go**

Revise теперь должен: записать feedback в файл через `state.SaveFeedback()`, обновить статус на `revising` в state.json. Оркестратор (если запущен) подхватит изменение через polling state.json или через API.

Но в новой архитектуре revise через CLI должен работать так:
1. Записать feedback.md
2. Обновить state.json → `revising`
3. Если оркестратор запущен — он подхватит. Если нет — при следующем `run` resume.

Упростить `revise.go` — убрать прямой запуск executor (это делает оркестратор):

```go
func newReviseCmd() *cobra.Command {
	var feedback string

	cmd := &cobra.Command{
		Use:   "revise <stage-id>",
		Short: "Send feedback for plan revision",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			stageID := args[0]

			if feedback == "" {
				data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
				if err != nil {
					return fmt.Errorf("read stdin: %w", err)
				}
				feedback = strings.TrimSpace(string(data))
			}
			if feedback == "" {
				return errors.New("feedback is required (use --feedback or stdin)")
			}

			stateFile, err := findLatestStateFile(stageID)
			if err != nil {
				return err
			}
			rs, err := state.Load(stateFile)
			if err != nil {
				return fmt.Errorf("load state: %w", err)
			}
			st, ok := rs.Stages[stageID]
			if !ok {
				return fmt.Errorf("stage %q not found", stageID)
			}
			if st.Status != state.StatusAwaitingApproval {
				return fmt.Errorf("stage %q is %v, not awaiting_approval", stageID, st.Status)
			}

			runDir := filepath.Dir(stateFile)
			stageDir := filepath.Join(runDir, stageID)

			// Версионировать текущий план
			if _, err := state.VersionPlan(stageDir); err != nil {
				return fmt.Errorf("version plan: %w", err)
			}

			// Сохранить фидбэк
			if err := state.SaveFeedback(stageDir, feedback); err != nil {
				return fmt.Errorf("save feedback: %w", err)
			}

			// Обновить статус
			rs.SetStageStatus(stageID, state.StatusRevising)
			if err := rs.Save(stateFile); err != nil {
				return fmt.Errorf("save state: %w", err)
			}

			fmt.Printf("feedback saved for stage %q — orchestrator will re-plan\n", stageID)
			return nil
		},
	}
	cmd.Flags().StringVar(&feedback, "feedback", "", "feedback text for plan revision")
	return cmd
}
```

Удалить ставшие ненужными функции `buildRevisionPrompt` и `nextRevisionNumber` из revise.go.

- [ ] **Step 2: Обновить тесты revise**

Обновить `cmd/flowmanager/revise_test.go` если есть — убрать тесты executor-вызовов, добавить тесты что feedback.md и plan.v1.md создаются.

- [ ] **Step 3: Убедиться что компилируется**

Run: `cd /Users/alexander.kopichin/work/flowManager && go build ./cmd/flowmanager/`

- [ ] **Step 4: Коммит**

```bash
git add cmd/flowmanager/revise.go cmd/flowmanager/revise_test.go
git commit -m "refactor: revise сохраняет feedback в файл, перепланирование делает оркестратор"
```

---

### Task 13: Обновить cmd/flowmanager/check.go — расширенный вывод

**Files:**
- Modify: `cmd/flowmanager/check.go`

- [ ] **Step 1: Обновить check.go**

Добавить цветной вывод и показ последнего действия агента:

```go
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/state"
)

// ANSI цвета
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorCyan   = "\033[36m"
	colorGray   = "\033[90m"
)

func statusColor(s state.StageStatus) string {
	switch s {
	case state.StatusDone:
		return colorGreen
	case state.StatusFailed:
		return colorRed
	case state.StatusAwaitingApproval:
		return colorYellow
	case state.StatusRunning, state.StatusPlanning:
		return colorBlue
	case state.StatusRevising:
		return colorCyan
	default:
		return colorGray
	}
}

func newCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Show status of the latest flow run",
		RunE: func(cmd *cobra.Command, args []string) error {
			base := filepath.Join(".flowManager", "runs")
			entries, err := os.ReadDir(base)
			if err != nil {
				return fmt.Errorf("no runs found in %s", base)
			}

			var dirs []string
			for _, e := range entries {
				if e.IsDir() {
					dirs = append(dirs, filepath.Join(base, e.Name()))
				}
			}
			if len(dirs) == 0 {
				return errors.New("no runs found")
			}
			slices.Sort(dirs)
			latest := dirs[len(dirs)-1]

			rs, err := state.Load(filepath.Join(latest, "state.json"))
			if err != nil {
				return fmt.Errorf("load state: %w", err)
			}

			fmt.Printf("Run: %s\n\n", filepath.Base(latest))
			fmt.Printf("%-20s  %-22s  %-10s  %s\n", "STAGE", "STATUS", "UPDATED", "LAST ACTION")
			fmt.Printf("%-20s  %-22s  %-10s  %s\n", "-----", "------", "-------", "-----------")

			type row struct{ id, status, updated, lastAction string }
			var rows []row
			for id, s := range rs.Stages {
				action := lastLogAction(filepath.Join(latest, id))
				rows = append(rows, row{
					id:         id,
					status:     string(s.Status),
					updated:    s.UpdatedAt.Format("15:04:05"),
					lastAction: action,
				})
			}
			slices.SortFunc(rows, func(a, b row) int {
				if a.id < b.id {
					return -1
				}
				if a.id > b.id {
					return 1
				}
				return 0
			})
			for _, r := range rows {
				color := statusColor(state.StageStatus(r.status))
				fmt.Printf("%-20s  %s%-22s%s  %-10s  %s\n",
					r.id, color, r.status, colorReset, r.updated, r.lastAction)
			}
			return nil
		},
	}
}

func lastLogAction(stageDir string) string {
	for _, name := range []string{"implementation.log", "planning.log"} {
		data, err := os.ReadFile(filepath.Join(stageDir, name))
		if err != nil {
			continue
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) > 0 {
			last := lines[len(lines)-1]
			if len(last) > 60 {
				last = last[:60] + "..."
			}
			return last
		}
	}
	return ""
}
```

- [ ] **Step 2: Убедиться что компилируется**

Run: `cd /Users/alexander.kopichin/work/flowManager && go build ./cmd/flowmanager/`
Expected: OK

- [ ] **Step 3: Коммит**

```bash
git add cmd/flowmanager/check.go
git commit -m "feat: check с цветным выводом и последним действием агента"
```

---

### Task 14: Обновить скиллы Claude Code

**Files:**
- Modify: `assets/claude/skills/flowmanager/SKILL.md`
- Modify: `assets/claude/skills/flowmanager-check/SKILL.md`
- Create: `assets/claude/skills/flowmanager-review/SKILL.md`
- Delete: `assets/claude/skills/flowmanager-monitor/`

- [ ] **Step 1: Переписать flowmanager SKILL.md**

```markdown
---
description: Run a flowManager flow with stage plan approval
allowed-tools: [Bash, Read, AskUserQuestion]
---

# flowmanager — Run a Flow

**SCOPE**: Launch a flowManager flow. Fire-and-forget — the main context is freed immediately.

## Step 0: Verify Installation

```bash
which flowmanager
```

If not found: `go install gitlab.ae-rus.net/bx/ai-flow-manager/cmd/flowmanager@latest`

## Step 1: Select Flow

```bash
flowmanager list
```

If argument was provided to the skill, use it directly. Otherwise show available flows via AskUserQuestion.

## Step 2: Launch Flow

Tell the user to run the flow in background:

> "Запусти в отдельном терминале: `flowmanager run {selected-flow}`"
> "Дашборд откроется автоматически на http://localhost:9876"
> "Для статуса используй `/flowmanager-check`, для ревью плана — `/flowmanager-review <stage>`"

**STOP. Контекст свободен.**

## Constraints

- Do NOT spawn any subagents
- Do NOT poll or wait for anything
- Do NOT modify any code
```

- [ ] **Step 2: Обновить flowmanager-check SKILL.md**

```markdown
---
description: Show status of the current flowManager run
allowed-tools: [Bash]
---

# check flow — Flow Status

Run:

```bash
flowmanager check
```

Display the output as-is. **STOP immediately after displaying.**
```

- [ ] **Step 3: Создать flowmanager-review SKILL.md**

```markdown
---
description: Review a stage plan and provide feedback or approve
allowed-tools: [Bash, Read, AskUserQuestion]
---

# flowmanager-review — Review Stage Plan

**SCOPE**: Read a stage plan, ask for approval or feedback, then call approve/revise.

## Step 1: Find the stage

If argument was provided, use it as stage ID. Otherwise:

```bash
flowmanager check
```

Ask the user which stage to review via AskUserQuestion.

## Step 2: Read the plan

Find the latest run directory:

```bash
ls -t .flowManager/runs/ | head -1
```

Read the plan file:

```bash
cat .flowManager/runs/{run_dir}/{stage_id}/plan.md
```

## Step 3: Show plan and ask for feedback

Show the plan content to the user via AskUserQuestion:

> "Plan for stage `{stage_id}`:"
>
> {plan content}
>
> Reply **ok** to approve, or write your feedback for revision.

## Step 4: Act on feedback

**If approved** (ok / да / yes / lgtm / approve):

```bash
flowmanager approve {stage_id}
```

**If feedback** (any other text):

```bash
flowmanager revise {stage_id} --feedback "{user response verbatim}"
```

## Step 5: STOP

Report the result and **STOP immediately**. Do NOT poll or wait.
```

- [ ] **Step 4: Удалить flowmanager-monitor**

```bash
rm -rf assets/claude/skills/flowmanager-monitor/
```

- [ ] **Step 5: Коммит**

```bash
git add assets/claude/skills/ && git rm -r assets/claude/skills/flowmanager-monitor/
git commit -m "feat: обновить скиллы — fire-and-forget flowmanager, новый flowmanager-review"
```

---

### Task 15: Удалить ненужный resume cmd

**Files:**
- Modify: `cmd/flowmanager/main.go` — убрать `newResumeCmd()`

Resume больше не нужен — `flowmanager run` автоматически резюмит через `resolveRun`.

- [ ] **Step 1: Проверить есть ли resume.go**

```bash
ls cmd/flowmanager/resume*.go
```

- [ ] **Step 2: Если есть — удалить файл и убрать из main.go**

Удалить строку `newResumeCmd()` из `root.AddCommand(...)` в `cmd/flowmanager/main.go`.

- [ ] **Step 3: Коммит**

```bash
git add cmd/flowmanager/
git commit -m "refactor: удалить resume cmd (run автоматически резюмит)"
```

---

### Task 16: make lint + make build

**Files:** Все

- [ ] **Step 1: Запустить линтер**

Run: `cd /Users/alexander.kopichin/work/flowManager && make lint`
Expected: OK. Если есть ошибки — исправить.

- [ ] **Step 2: Запустить сборку**

Run: `cd /Users/alexander.kopichin/work/flowManager && make build`
Expected: OK

- [ ] **Step 3: Запустить все тесты**

Run: `cd /Users/alexander.kopichin/work/flowManager && make test`
Expected: PASS

- [ ] **Step 4: Коммит если были фиксы**

```bash
git add -A
git commit -m "fix: исправления по результатам lint/build/test"
```

---

### Task 17: Установить скиллы и пересобрать

**Files:** None (операционная задача)

- [ ] **Step 1: Переустановить скиллы**

Run: `cd /Users/alexander.kopichin/work/flowManager && make install-skills`

- [ ] **Step 2: Пересобрать и установить бинарник**

Run: `cd /Users/alexander.kopichin/work/flowManager && make build && ./install.sh`

- [ ] **Step 3: Проверить что скиллы доступны**

```bash
ls ~/.claude/skills/flowmanager/
ls ~/.claude/skills/flowmanager-check/
ls ~/.claude/skills/flowmanager-review/
ls ~/.claude/skills/flowmanager-monitor/ 2>/dev/null && echo "ERROR: monitor should be deleted" || echo "OK: monitor deleted"
```

---

### Task 18: E2E тест с реальным flow

**Files:** Использовать `../testFlow/flow.yml`

**Эта задача выполняется вручную (не автоматизирована).**

- [ ] **Step 1: Запустить flow**

```bash
cd /Users/alexander.kopichin/work/testFlow
flowmanager run flow.yml
```

Убедиться: дашборд открылся на http://localhost:9876.

- [ ] **Step 2: Дождаться planning стадии init**

В дашборде или через `flowmanager check` — дождаться `awaiting_approval` для `init`.

- [ ] **Step 3: Ревью плана init**

Прочитать план, оценить адекватность. Если ок — approve через дашборд или CLI:

```bash
flowmanager approve init
```

- [ ] **Step 4: Дождаться planning стадии frontend**

После завершения init, frontend начнёт planning.

- [ ] **Step 5: Ревью плана frontend**

Прочитать план. Если нужно — revise:

```bash
flowmanager revise frontend --feedback "Добавь больше деталей про анимацию"
```

Дождаться нового плана, затем approve.

- [ ] **Step 6: Дождаться planning стадии test**

После frontend → test.

- [ ] **Step 7: Approve test и дождаться ALL_DONE**

- [ ] **Step 8: Финальная проверка файлов**

```bash
# React приложение
ls package.json node_modules/
# state.json — все done
cat .flowManager/runs/*/state.json
# Логи не пустые
wc -l .flowManager/runs/*/*/planning.log
wc -l .flowManager/runs/*/*/implementation.log
```

- [ ] **Step 9: Визуальная верификация**

Запустить сервер лендинга, открыть в браузере через MCP chrome-devtools, сделать скриншот.
Сравнить с планом frontend:
- Есть лендинг BMW F900XR
- Есть сравнение с BMW R1300GS
- Есть фоновая анимация/видео
- Есть фото мотоциклов
- Есть плюсы и минусы

- [ ] **Step 10: Финальные make lint + make build**

```bash
cd /Users/alexander.kopichin/work/flowManager
make lint
make build
```

Expected: OK без ошибок.
