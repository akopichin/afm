# Lifecycle-хуки Phase 2 — Implementation Plan (durable-доставка)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Добавить durable at-least-once доставку lifecycle-хуков для **FSM-производных** событий: outbox (`hook_events.jsonl` + `hook_deliveries.jsonl` с fsync), реконсиляцию `events.jsonl`↔outbox по `seq` при старте и повтор недоставленных пар после рестарта. Phase 1 (live best-effort) остаётся рабочим при выключенном outbox.

**Architecture:** Новый `pkg/lifecyclehooks/outbox.go` — durable-журнал конвертов событий (envelope = event_id + payload, fsync) и журнал доставок (пары event_id+hook_id). Dispatcher получает опциональный `*Outbox`: на `Emit` пишет конверт (fsync) ДО постановки в очередь хуков; воркер пишет пару доставки после успеха. При старте оркестратор реконсилит: по `store.History()` для каждого durable FSM-перехода гарантирует наличие конверта (`EnsureEnvelope`), затем `Redeliver()` доставляет все недоставленные пары. Не-FSM события (`flow_*`, `stage_script_*`, авто-ответы) — как в спеке — best-effort: пишутся в outbox при live-эмиссии (и потому переотправляются, если конверт успел записаться), но НЕ реконструируются после краха.

**Tech Stack:** Go (stdlib only для нового кода), существующий `pkg/lifecyclehooks`, `pkg/orchestrator` (`lifecycle_emit.go`, `orchestrator.go`, `bus/fsm.go`), `pkg/state` (`Store.History`, `Transition`), `cmd/afm/run.go`.

**Spec:** `docs/superpowers/specs/2026-09-18-lifecycle-hooks-design.md` — разделы «Phase 2 — durable-доставка» и «Гарантии доставки» (FSM-производные: полная гарантия; не-FSM: best-effort).

## Global Constraints

- Все коммиты — на русском, БЕЗ Co-Authored-By.
- Не менять версию Go в go.mod.
- После каждой задачи: `go build ./... && go vet ./...` чисто; `golangci-lint run` — без НОВЫХ issues vs baseline (проверять `git stash` при сомнении).
- Кроссплатформенность: новый код без Unix-only syscalls (fsync через `(*os.File).Sync()` — переносим). Windows-сборка `GOOS=windows go build ./pkg/lifecyclehooks/` должна оставаться зелёной.
- **At-least-once только для FSM-производных событий** (`fsmLifecycleEvents` в `pkg/orchestrator/lifecycle_emit.go`). Не-FSM — best-effort, реконсиляция их не воссоздаёт. Не «чинить» это.
- **Инвариант Phase 1 сохраняется:** ошибка хука/outbox никогда не трогает FSM/статус/результат флоу. Сбой записи в outbox → лог + продолжаем (live-доставка), не паника, не остановка рана.
- **Обратная совместимость:** `Outbox == nil` в dispatcher = точное поведение Phase 1 (все существующие тесты pkg/lifecyclehooks и pkg/orchestrator остаются зелёными без изменений). Outbox включается только через `cmd/afm/run.go`.
- Per-run файлы: `<runDir>/hook_events.jsonl`, `<runDir>/hook_deliveries.jsonl` (рядом с существующим `<runDir>/hooks/<id>.log`).

## File Structure

Новые файлы:
- `pkg/lifecyclehooks/outbox.go` — `Outbox`: `OpenOutbox`, `RecordEvent` (fsync), `RecordDelivery`, `HasEvent`, `Delivered`, `Envelopes`, `Close`; типы `envelopeRec`/`deliveryRec`.
- `pkg/lifecyclehooks/outbox_test.go`.
- `pkg/orchestrator/lifecycle_reconcile_test.go`.

Модифицируемые:
- `pkg/lifecyclehooks/dispatcher.go` — `DispatcherOptions.Outbox`, поле `outbox`, запись конверта в `Emit` до enqueue, запись доставки в `worker`, методы `EnsureEnvelope`/`Redeliver`, хелпер `matchesPayload`.
- `pkg/lifecyclehooks/dispatcher_test.go` — доп. тесты outbox-пути.
- `pkg/orchestrator/lifecycle_emit.go` — метод `reconcileLifecycleOutbox`.
- `pkg/orchestrator/orchestrator.go` — вызов `reconcileLifecycleOutbox` в `Run` (на резюме, до `startPlanningForPending`).
- `cmd/afm/run.go` — `OpenOutbox(runDir)`, проброс в `DispatcherOptions.Outbox`, `Close` в unified defer.
- `AGENTS.md` — обновить раздел «Lifecycle hooks» пометкой про Phase 2.

Ссылки на код (проверено 2026-09-18, ветка notify, HEAD d0ff71a):
- `Dispatcher`/`DispatcherOptions`/`delivery`/`New`/`Start` — dispatcher.go:20-84; `Emit` — :90-114; `eventID` — :123-136; `worker` — :139-148; `Flush`/`Stop` — ниже.
- `Event` (с полем `Key`), `Payload`/`StageInfo`/`FlowInfo`, `BuildPayload`, `RegisteredHook.MatchesEvent`, `Hook.Matches` — types.go/matcher.go.
- `fsmLifecycleEvents` (map `bus.FSMEvent`→`lifecyclehooks.EventType`), `emitLifecycle`, `stageName`, `phaseForStatus` — pkg/orchestrator/lifecycle_emit.go.
- `Store.History() ([]Transition, error)` — store.go:218; `Transition{Seq,Time,StageID,From,To,Event,Reason}` — store.go:16-24 (`Event` — строковое имя FSM-события, совпадает с underlying-значением `bus.FSMEvent`).
- `Run` — orchestrator.go: emit flow_started/resumed → (сюда вставить reconcile) → recoverReviewPause/startPlanningForPending; unified Flush+Stop defer — cmd/afm/run.go.

---

### Task 1: Outbox — durable журнал событий и доставок (`outbox.go`)

**Files:**
- Create: `pkg/lifecyclehooks/outbox.go`, `pkg/lifecyclehooks/outbox_test.go`

**Interfaces:**
- Consumes: `Payload` (types.go).
- Produces (используют Tasks 2-3):
  - `type Outbox struct {...}` (thread-safe).
  - `func OpenOutbox(dir string) (*Outbox, error)` — создаёт dir, реплеит оба файла, держит открытые append-хендлы.
  - `func (o *Outbox) RecordEvent(eventID string, p Payload) error` — идемпотентно (no-op если event_id уже виден), append envelope + `Sync()`.
  - `func (o *Outbox) RecordDelivery(eventID, hookID string) error` — append пары + `Sync()`; идемпотентно.
  - `func (o *Outbox) HasEvent(eventID string) bool`; `func (o *Outbox) Delivered(eventID, hookID string) bool`.
  - `func (o *Outbox) Envelopes() []Envelope` — конверты в порядке первой записи (копия); `type Envelope struct { EventID string; Payload Payload }`.
  - `func (o *Outbox) Close() error`.

- [ ] **Step 1: Write the failing test**

```go
package lifecyclehooks

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func samplerPayload(id string) Payload {
	return Payload{SchemaVersion: 1, EventID: id, Event: EventStageFinished, OccurredAt: time.Unix(0, 0).UTC(),
		Flow: FlowInfo{Name: "f", RunID: "run-1"}, Stage: &StageInfo{ID: "s1", To: "done"}}
}

func TestOutbox_RecordEventIdempotentAndDurable(t *testing.T) {
	dir := t.TempDir()
	ob, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ob.RecordEvent("e1", samplerPayload("e1")); err != nil {
		t.Fatal(err)
	}
	if err := ob.RecordEvent("e1", samplerPayload("e1")); err != nil { // повтор — no-op
		t.Fatal(err)
	}
	if !ob.HasEvent("e1") || ob.HasEvent("e2") {
		t.Fatal("HasEvent wrong")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "hook_events.jsonl"))
	// ровно одна строка (идемпотентность)
	if n := countLines(raw); n != 1 {
		t.Fatalf("want 1 envelope line, got %d: %s", n, raw)
	}
	if err := ob.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOutbox_ReplayOnReopen(t *testing.T) {
	dir := t.TempDir()
	ob, _ := OpenOutbox(dir)
	_ = ob.RecordEvent("e1", samplerPayload("e1"))
	_ = ob.RecordEvent("e2", samplerPayload("e2"))
	_ = ob.RecordDelivery("e1", "h1")
	_ = ob.Close()

	ob2, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ob2.Close()
	if !ob2.HasEvent("e1") || !ob2.HasEvent("e2") {
		t.Fatal("events not replayed")
	}
	if !ob2.Delivered("e1", "h1") {
		t.Fatal("delivery not replayed")
	}
	if ob2.Delivered("e2", "h1") {
		t.Fatal("false delivery")
	}
	envs := ob2.Envelopes()
	if len(envs) != 2 || envs[0].EventID != "e1" || envs[1].EventID != "e2" {
		t.Fatalf("envelopes/order wrong: %+v", envs)
	}
	if envs[0].Payload.Event != EventStageFinished {
		t.Fatalf("payload not round-tripped: %+v", envs[0].Payload)
	}
}

func TestOutbox_ToleratesTruncatedTail(t *testing.T) {
	dir := t.TempDir()
	ob, _ := OpenOutbox(dir)
	_ = ob.RecordEvent("e1", samplerPayload("e1"))
	_ = ob.Close()
	// имитируем крах во время append: дописываем половину JSON-строки без \n
	f, _ := os.OpenFile(filepath.Join(dir, "hook_events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(`{"event_id":"e2","payload":{"schema`)
	_ = f.Close()

	ob2, err := OpenOutbox(dir)
	if err != nil {
		t.Fatalf("truncated tail must not error: %v", err)
	}
	defer ob2.Close()
	if !ob2.HasEvent("e1") || ob2.HasEvent("e2") {
		t.Fatal("should keep e1, drop torn e2")
	}
}

func countLines(b []byte) int {
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}
```

(Опечатку `samplerPayload`/`samplerPayload` в эскизе привести к одному имени — `samplerPayload`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/lifecyclehooks/ -run TestOutbox -v`
Expected: FAIL (compile — типов нет).

- [ ] **Step 3: Write minimal implementation (`outbox.go`)**

```go
package lifecyclehooks

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	eventsFileName     = "hook_events.jsonl"
	deliveriesFileName = "hook_deliveries.jsonl"
)

// Envelope — durable-запись одного lifecycle-события: стабильный event_id и
// полный payload (чтобы переотправка после рестарта не пересобирала payload).
type Envelope struct {
	EventID string  `json:"event_id"`
	Payload Payload `json:"payload"`
}

type deliveryRec struct {
	EventID string `json:"event_id"`
	HookID  string `json:"hook_id"`
}

// Outbox — durable-журнал событий (envelope, fsync) и журнал доставок (пары
// event_id+hook_id). Обеспечивает at-least-once для FSM-производных событий:
// конверт пишется ДО запуска команды, пара доставки — после успеха; при старте
// недоставленные пары переотправляются. Все методы потокобезопасны.
type Outbox struct {
	mu        sync.Mutex
	eventsF   *os.File
	deliverF  *os.File
	seen      map[string]bool // event_id → записан конверт
	order     []string        // event_id в порядке первой записи
	payloads  map[string]Payload
	delivered map[string]bool // key(event_id,hook_id) → доставлено
}

func deliveryKey(eventID, hookID string) string { return eventID + "\x00" + hookID }

// OpenOutbox создаёт директорию, реплеит оба журнала и открывает их на append.
func OpenOutbox(dir string) (*Outbox, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("outbox mkdir: %w", err)
	}
	o := &Outbox{
		seen:      map[string]bool{},
		payloads:  map[string]Payload{},
		delivered: map[string]bool{},
	}
	if err := o.replayEvents(filepath.Join(dir, eventsFileName)); err != nil {
		return nil, err
	}
	if err := o.replayDeliveries(filepath.Join(dir, deliveriesFileName)); err != nil {
		return nil, err
	}
	var err error
	if o.eventsF, err = os.OpenFile(filepath.Join(dir, eventsFileName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
		return nil, fmt.Errorf("outbox open events: %w", err)
	}
	if o.deliverF, err = os.OpenFile(filepath.Join(dir, deliveriesFileName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
		o.eventsF.Close()
		return nil, fmt.Errorf("outbox open deliveries: %w", err)
	}
	return o, nil
}

func (o *Outbox) replayEvents(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var e Envelope
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.EventID == "" {
			continue // торн/битая строка (крах во время append) — пропускаем
		}
		if !o.seen[e.EventID] {
			o.seen[e.EventID] = true
			o.order = append(o.order, e.EventID)
		}
		o.payloads[e.EventID] = e.Payload
	}
	return sc.Err()
}

func (o *Outbox) replayDeliveries(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var d deliveryRec
		if json.Unmarshal(sc.Bytes(), &d) != nil || d.EventID == "" {
			continue
		}
		o.delivered[deliveryKey(d.EventID, d.HookID)] = true
	}
	return sc.Err()
}

// RecordEvent идемпотентно фиксирует конверт с fsync (envelope пишется ДО
// запуска команды хука). Повторный event_id — no-op.
func (o *Outbox) RecordEvent(eventID string, p Payload) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.seen[eventID] {
		return nil
	}
	line, err := json.Marshal(Envelope{EventID: eventID, Payload: p})
	if err != nil {
		return err
	}
	if _, err := o.eventsF.Write(append(line, '\n')); err != nil {
		return err
	}
	if err := o.eventsF.Sync(); err != nil {
		return err
	}
	o.seen[eventID] = true
	o.order = append(o.order, eventID)
	o.payloads[eventID] = p
	return nil
}

// RecordDelivery фиксирует успешную доставку (event_id, hook_id). Идемпотентно.
func (o *Outbox) RecordDelivery(eventID, hookID string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	k := deliveryKey(eventID, hookID)
	if o.delivered[k] {
		return nil
	}
	line, err := json.Marshal(deliveryRec{EventID: eventID, HookID: hookID})
	if err != nil {
		return err
	}
	if _, err := o.deliverF.Write(append(line, '\n')); err != nil {
		return err
	}
	if err := o.deliverF.Sync(); err != nil {
		return err
	}
	o.delivered[k] = true
	return nil
}

func (o *Outbox) HasEvent(eventID string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.seen[eventID]
}

func (o *Outbox) Delivered(eventID, hookID string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.delivered[deliveryKey(eventID, hookID)]
}

// Envelopes возвращает все конверты в порядке первой записи (копия).
func (o *Outbox) Envelopes() []Envelope {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]Envelope, 0, len(o.order))
	for _, id := range o.order {
		out = append(out, Envelope{EventID: id, Payload: o.payloads[id]})
	}
	return out
}

func (o *Outbox) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	var err error
	if o.eventsF != nil {
		err = o.eventsF.Close()
		o.eventsF = nil
	}
	if o.deliverF != nil {
		if e := o.deliverF.Close(); e != nil && err == nil {
			err = e
		}
		o.deliverF = nil
	}
	return err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/lifecyclehooks/ -run TestOutbox -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/lifecyclehooks/outbox.go pkg/lifecyclehooks/outbox_test.go
git commit -m "feat(lifecyclehooks): durable outbox — журнал событий (fsync) и доставок"
```

---

### Task 2: Dispatcher — интеграция outbox (envelope до команды, delivery после, EnsureEnvelope/Redeliver)

**Files:**
- Modify: `pkg/lifecyclehooks/dispatcher.go`
- Test: `pkg/lifecyclehooks/dispatcher_test.go` (доп.)

**Interfaces:**
- Consumes: Task 1 (`*Outbox`), существующие `Event`/`Payload`/`RegisteredHook`.
- Produces (используют Task 3):
  - `DispatcherOptions.Outbox *Outbox` (nil = поведение Phase 1).
  - Изменённые `Emit` (envelope до enqueue) и `worker` (delivery после успеха).
  - `func (d *Dispatcher) EnsureEnvelope(ev Event)` — вычисляет eventID+payload, `outbox.RecordEvent` (без enqueue); nil-outbox → no-op.
  - `func (d *Dispatcher) Redeliver()` — для каждого конверта из `outbox.Envelopes()`, для каждого подходящего хука без доставки — enqueue (блокирующая постановка, ничего не дропает).

- [ ] **Step 1: Write the failing test**

```go
func TestDispatcher_OutboxRecordsEnvelopeBeforeDelivery(t *testing.T) {
	dir := t.TempDir()
	ob, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ob.Close() })

	var envelopeExistedAtRun bool
	d := New(DispatcherOptions{
		Config: DispatcherConfig{RunID: "run-1", RunDir: dir, RootDir: dir},
		Hooks:  []RegisteredHook{{Hook: Hook{ID: "h", Events: EventSelector{All: true}, Command: "true"}}},
		LogDir: dir,
		Outbox: ob,
		RunFunc: func(ctx context.Context, h Hook, cfg DispatcherConfig, p Payload, logPath string) error {
			// на момент запуска команды конверт уже durable
			envelopeExistedAtRun = ob.HasEvent(p.EventID)
			return nil
		},
	})
	d.Start()
	defer d.Stop()
	d.Emit(Event{Type: EventFlowFinished, Time: time.Now()})
	if !d.Flush(2 * time.Second) {
		t.Fatal("flush timeout")
	}
	if !envelopeExistedAtRun {
		t.Fatal("envelope must be recorded BEFORE the command runs")
	}
	if !ob.Delivered("run-1:flow:flow_finished", "h") {
		t.Fatal("delivery must be recorded after success")
	}
}

func TestDispatcher_OutboxNoDeliveryOnFailure(t *testing.T) {
	dir := t.TempDir()
	ob, _ := OpenOutbox(dir)
	t.Cleanup(func() { ob.Close() })
	d := New(DispatcherOptions{
		Config: DispatcherConfig{RunID: "run-1", RunDir: dir, RootDir: dir},
		Hooks:  []RegisteredHook{{Hook: Hook{ID: "h", Events: EventSelector{All: true}, Command: "true"}}},
		LogDir: dir, Outbox: ob,
		RunFunc: func(ctx context.Context, h Hook, cfg DispatcherConfig, p Payload, logPath string) error {
			return errors.New("boom")
		},
		OnError: func(hookID, eventID string, err error) {},
	})
	d.Start()
	defer d.Stop()
	d.Emit(Event{Type: EventFlowFinished, Time: time.Now()})
	d.Flush(2 * time.Second)
	if !ob.HasEvent("run-1:flow:flow_finished") {
		t.Fatal("envelope recorded regardless")
	}
	if ob.Delivered("run-1:flow:flow_finished", "h") {
		t.Fatal("failed delivery must NOT be recorded (stays for redelivery)")
	}
}

func TestDispatcher_RedeliverEnqueuesUndelivered(t *testing.T) {
	dir := t.TempDir()
	// Пре-populate outbox: конверт есть, доставки нет (имитация краха до доставки).
	ob, _ := OpenOutbox(dir)
	_ = ob.RecordEvent("run-1:flow:flow_finished", Payload{SchemaVersion: 1, EventID: "run-1:flow:flow_finished", Event: EventFlowFinished})
	t.Cleanup(func() { ob.Close() })

	var delivered []string
	var mu sync.Mutex
	d := New(DispatcherOptions{
		Config: DispatcherConfig{RunID: "run-1", RunDir: dir, RootDir: dir},
		Hooks:  []RegisteredHook{{Hook: Hook{ID: "h", Events: EventSelector{All: true}, Command: "true"}}},
		LogDir: dir, Outbox: ob,
		RunFunc: func(ctx context.Context, h Hook, cfg DispatcherConfig, p Payload, logPath string) error {
			mu.Lock()
			delivered = append(delivered, p.EventID)
			mu.Unlock()
			return nil
		},
	})
	d.Start()
	defer d.Stop()
	d.Redeliver()
	if !d.Flush(2 * time.Second) {
		t.Fatal("flush timeout")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(delivered) != 1 || delivered[0] != "run-1:flow:flow_finished" {
		t.Fatalf("redeliver wrong: %+v", delivered)
	}
	if !ob.Delivered("run-1:flow:flow_finished", "h") {
		t.Fatal("redelivery must record delivery")
	}
}

func TestDispatcher_RedeliverSkipsAlreadyDelivered(t *testing.T) {
	dir := t.TempDir()
	ob, _ := OpenOutbox(dir)
	_ = ob.RecordEvent("run-1:flow:flow_finished", Payload{EventID: "run-1:flow:flow_finished", Event: EventFlowFinished})
	_ = ob.RecordDelivery("run-1:flow:flow_finished", "h")
	t.Cleanup(func() { ob.Close() })
	called := 0
	d := New(DispatcherOptions{
		Config: DispatcherConfig{RunID: "run-1"}, LogDir: dir, Outbox: ob,
		Hooks:   []RegisteredHook{{Hook: Hook{ID: "h", Events: EventSelector{All: true}, Command: "true"}}},
		RunFunc: func(ctx context.Context, h Hook, cfg DispatcherConfig, p Payload, logPath string) error { called++; return nil },
	})
	d.Start()
	defer d.Stop()
	d.Redeliver()
	d.Flush(time.Second)
	if called != 0 {
		t.Fatalf("already-delivered must not re-run, got %d", called)
	}
}

func TestDispatcher_EnsureEnvelopeNoEnqueue(t *testing.T) {
	dir := t.TempDir()
	ob, _ := OpenOutbox(dir)
	t.Cleanup(func() { ob.Close() })
	called := 0
	d := New(DispatcherOptions{
		Config: DispatcherConfig{RunID: "run-1"}, LogDir: dir, Outbox: ob,
		Hooks:   []RegisteredHook{{Hook: Hook{ID: "h", Events: EventSelector{All: true}, Command: "true"}}},
		RunFunc: func(ctx context.Context, h Hook, cfg DispatcherConfig, p Payload, logPath string) error { called++; return nil },
	})
	d.Start()
	defer d.Stop()
	d.EnsureEnvelope(Event{Type: EventStageFinished, StageID: "s1", Seq: 5, Time: time.Now()})
	d.Flush(time.Second)
	if called != 0 {
		t.Fatal("EnsureEnvelope must not enqueue/deliver")
	}
	if !ob.HasEvent("run-1:transition:5:stage_finished") {
		t.Fatal("EnsureEnvelope must record the envelope")
	}
}

func TestDispatcher_NilOutboxUnchanged(t *testing.T) {
	// Явно проверяем, что без outbox поведение прежнее (EnsureEnvelope/Redeliver — no-op).
	d, rec := newRecordingDispatcher(t, []RegisteredHook{{Hook: Hook{ID: "g", Events: EventSelector{All: true}, Command: "true"}}})
	d.Start()
	defer d.Stop()
	d.EnsureEnvelope(Event{Type: EventFlowStarted, Time: time.Now()}) // no-op, не паникует
	d.Redeliver()                                                     // no-op
	d.Emit(Event{Type: EventFlowStarted, Time: time.Now()})
	waitFor(t, rec, 1)
	if dl, _ := rec.snapshot(); len(dl) != 1 {
		t.Fatalf("nil-outbox Emit still works: %+v", dl)
	}
}
```

(Импорт `errors`, `sync` в тестовый файл, если ещё не импортированы.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/lifecyclehooks/ -run 'TestDispatcher_Outbox|TestDispatcher_Redeliver|TestDispatcher_EnsureEnvelope|TestDispatcher_NilOutbox' -v`
Expected: FAIL (методов/поля нет).

- [ ] **Step 3: Write minimal implementation (`dispatcher.go`)**

`DispatcherOptions` — добавить поле:

```go
	// Outbox (Phase 2) — durable-журнал at-least-once. nil = live best-effort
	// (Phase 1): конверты/доставки не пишутся, EnsureEnvelope/Redeliver — no-op.
	Outbox *Outbox
```

`Dispatcher` — поле `outbox *Outbox`; в `New` — `outbox: opts.Outbox`.

`Emit` — записать конверт ДО цикла enqueue (после вычисления eventID+payload):

```go
	eventID := d.eventID(ev)
	payload := BuildPayload(d.cfg, ev, eventID)
	if d.outbox != nil {
		if err := d.outbox.RecordEvent(eventID, payload); err != nil {
			// Сбой outbox не должен глушить live-доставку и тем более трогать
			// ран: логируем через OnError и продолжаем (реконсиляция подхватит
			// FSM-событие на следующем старте, если это FSM-производное).
			d.reportError("", eventID, fmt.Errorf("outbox record event: %w", err))
		}
	}
	for i, rh := range d.hooks {
		...
	}
```

`worker` — записать доставку после успеха:

```go
func (d *Dispatcher) worker(ctx context.Context, rh RegisteredHook, q <-chan delivery) {
	defer d.wg.Done()
	for dl := range q {
		logPath := filepath.Join(d.logDir, rh.Hook.ID+".log")
		err := d.runFn(ctx, rh.Hook, d.cfg, dl.payload, logPath)
		if err != nil {
			if ctx.Err() == nil {
				d.reportError(rh.Hook.ID, dl.eventID, err)
			}
		} else if d.outbox != nil {
			if rerr := d.outbox.RecordDelivery(dl.eventID, rh.Hook.ID); rerr != nil {
				d.reportError(rh.Hook.ID, dl.eventID, fmt.Errorf("outbox record delivery: %w", rerr))
			}
		}
		d.done.Add(1)
	}
}
```

`EnsureEnvelope` и `Redeliver` (+ хелпер `matchesPayload`) — новые методы:

```go
// EnsureEnvelope гарантирует, что для события есть durable-конверт, БЕЗ
// постановки в очередь. Используется реконсиляцией: по durable FSM-переходу из
// events.jsonl восстанавливает конверт, потерянный в окне краха. nil-outbox → no-op.
func (d *Dispatcher) EnsureEnvelope(ev Event) {
	if d.outbox == nil {
		return
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	eventID := d.eventID(ev)
	payload := BuildPayload(d.cfg, ev, eventID)
	if err := d.outbox.RecordEvent(eventID, payload); err != nil {
		d.reportError("", eventID, fmt.Errorf("outbox ensure envelope: %w", err))
	}
}

// Redeliver ставит в очередь все недоставленные пары (конверт, хук) из outbox.
// Блокирующая постановка (ничего не дропает) — вызывается один раз при старте,
// очереди пусты. nil-outbox → no-op.
func (d *Dispatcher) Redeliver() {
	if d.outbox == nil {
		return
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.stopped {
		return
	}
	for _, env := range d.outbox.Envelopes() {
		for i, rh := range d.hooks {
			if !matchesPayload(rh, env.Payload) || d.outbox.Delivered(env.EventID, rh.Hook.ID) {
				continue
			}
			select {
			case d.queues[i] <- delivery{eventID: env.EventID, payload: env.Payload}:
				d.sent.Add(1)
			case <-d.ctx.Done():
				return
			}
		}
	}
}

// matchesPayload — как RegisteredHook.MatchesEvent, но по восстановленному
// payload (у редоставки нет исходного Event): тип из payload.Event, скоуп из
// payload.Stage.
func matchesPayload(rh RegisteredHook, p Payload) bool {
	if !rh.Hook.Matches(p.Event) {
		return false
	}
	if rh.StageID == "" {
		return true
	}
	return p.Stage != nil && p.Stage.ID == rh.StageID
}
```

Примечание: `Redeliver` использует блокирующий `select` с `d.ctx.Done()` (в отличие от `Emit`'s drop-on-full) — при старте важно НЕ терять недоставленное; очереди пусты, воркеры уже подняты (`Start` до `Redeliver`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/lifecyclehooks/ -race -v`
Expected: PASS (новые + все старые, включая nil-outbox путь).

- [ ] **Step 5: Commit**

```bash
git add pkg/lifecyclehooks/dispatcher.go pkg/lifecyclehooks/dispatcher_test.go
git commit -m "feat(lifecyclehooks): outbox в dispatcher — конверт до команды, доставка после, Redeliver/EnsureEnvelope"
```

---

### Task 3: Orchestrator — реконсиляция events.jsonl ↔ outbox при старте

**Files:**
- Modify: `pkg/orchestrator/lifecycle_emit.go` (метод `reconcileLifecycleOutbox`), `pkg/orchestrator/orchestrator.go` (вызов в `Run`)
- Test: `pkg/orchestrator/lifecycle_reconcile_test.go`

**Interfaces:**
- Consumes: Task 2 (`EnsureEnvelope`/`Redeliver`), `fsmLifecycleEvents`, `stageName`, `phaseForStatus`, `Store.History`, `Transition`.
- Produces: `func (o *Orchestrator) reconcileLifecycleOutbox()` — по истории переходов гарантирует конверты FSM-производных событий, затем `Redeliver`. Вызывается в `Run` до `startPlanningForPending`.

- [ ] **Step 1: Write the failing test**

Модель: собрать orchestrator с реальным dispatcher (outbox во временном dir) + store, у которого в истории уже есть FSM-переход (как после резюма), при ПУСТОМ outbox (имитация краха: переход durable, конверт не записан). Вызвать `reconcileLifecycleOutbox` → конверт создан и доставлен.

```go
package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

func TestReconcile_MissingEnvelopeRecreatedAndDelivered(t *testing.T) {
	dir := t.TempDir()
	ob, err := lifecyclehooks.OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ob.Close() })

	var delivered []string
	disp := lifecyclehooks.New(lifecyclehooks.DispatcherOptions{
		Config: lifecyclehooks.DispatcherConfig{RunID: "run-1", RunDir: dir, RootDir: dir},
		Hooks:  []lifecyclehooks.RegisteredHook{{Hook: lifecyclehooks.Hook{ID: "h", Events: lifecyclehooks.EventSelector{All: true}, Command: "true"}}},
		LogDir: dir, Outbox: ob,
		RunFunc: recordDeliveredFn(&delivered), // хелпер: пишет p.EventID в срез под мьютексом
	})
	disp.Start()
	t.Cleanup(disp.Stop)

	o := newTestOrchestratorWithHooks(t, disp) // хелпер: orchestrator с этим dispatcher и стадией "s1"
	// имитируем durable-переход из прошлого рана: применяем EvComplete для s1
	seedTransition(t, o, "s1", bus.EvStartPlanning) // pending->planning (в истории)
	// ... привести стадию к running и применить EvComplete, чтобы в History был stage_finished

	o.reconcileLifecycleOutbox()
	if !disp.Flush(2 * time.Second) {
		t.Fatal("flush timeout")
	}
	// конверт для durable-перехода создан и доставлен
	if !ob.HasEvent("run-1:transition:1:stage_planning_started") {
		t.Fatalf("envelope not reconciled; delivered=%v", delivered)
	}
}

func TestReconcile_FreshRunNoHistoryNoop(t *testing.T) {
	dir := t.TempDir()
	ob, _ := lifecyclehooks.OpenOutbox(dir)
	t.Cleanup(func() { ob.Close() })
	var delivered []string
	disp := lifecyclehooks.New(lifecyclehooks.DispatcherOptions{
		Config: lifecyclehooks.DispatcherConfig{RunID: "run-1"}, LogDir: dir, Outbox: ob,
		Hooks:   []lifecyclehooks.RegisteredHook{{Hook: lifecyclehooks.Hook{ID: "h", Events: lifecyclehooks.EventSelector{All: true}, Command: "true"}}},
		RunFunc: recordDeliveredFn(&delivered),
	})
	disp.Start()
	t.Cleanup(disp.Stop)
	o := newTestOrchestratorWithHooks(t, disp)
	o.reconcileLifecycleOutbox() // история пуста → ничего
	disp.Flush(time.Second)
	if len(delivered) != 0 {
		t.Fatalf("fresh run must reconcile nothing, got %v", delivered)
	}
}

func TestReconcile_AlreadyDeliveredNotRedelivered(t *testing.T) {
	// Переход в истории + конверт + доставка уже есть → reconcile ничего не шлёт.
	dir := t.TempDir()
	ob, _ := lifecyclehooks.OpenOutbox(dir)
	_ = ob.RecordEvent("run-1:transition:1:stage_planning_started", lifecyclehooks.Payload{
		EventID: "run-1:transition:1:stage_planning_started", Event: lifecyclehooks.EventStagePlanningStarted,
		Stage: &lifecyclehooks.StageInfo{ID: "s1"},
	})
	_ = ob.RecordDelivery("run-1:transition:1:stage_planning_started", "h")
	t.Cleanup(func() { ob.Close() })
	var delivered []string
	disp := lifecyclehooks.New(lifecyclehooks.DispatcherOptions{
		Config: lifecyclehooks.DispatcherConfig{RunID: "run-1"}, LogDir: dir, Outbox: ob,
		Hooks:   []lifecyclehooks.RegisteredHook{{Hook: lifecyclehooks.Hook{ID: "h", Events: lifecyclehooks.EventSelector{All: true}, Command: "true"}}},
		RunFunc: recordDeliveredFn(&delivered),
	})
	disp.Start()
	t.Cleanup(disp.Stop)
	o := newTestOrchestratorWithHooks(t, disp)
	seedTransition(t, o, "s1", bus.EvStartPlanning)
	o.reconcileLifecycleOutbox()
	disp.Flush(time.Second)
	if len(delivered) != 0 {
		t.Fatalf("already-delivered must not redeliver, got %v", delivered)
	}
}
```

Хелперы (`newTestOrchestratorWithHooks`, `seedTransition`, `recordDeliveredFn`) — построить по образцу существующих хелперов пакета (grep `newTestOrchestrator` из Task 8-9 emit-тестов; `seedTransition` применяет `o.triggerWithSeq` или прямой `store.Apply`, чтобы наполнить `History`). ВАЖНО: если `seedTransition` идёт через `o.triggerWithSeq`, он сам вызовет live-`Emit` (запишет конверт+доставит) — тогда «missing envelope» не смоделировать. Чтобы смоделировать крах, наполнять историю нужно НАПРЯМУЮ через `o.opts.Store` (минуя `emitLifecycleTransition`), например применяя `state.Transition` в store так же, как это делает `fsm.Apply`, но без orchestrator-эмиссии. Сверить с тем, как тесты pkg/state наполняют историю (`store.Apply(&state.Transition{...})`), и переиспользовать. Если прямой `store.Apply` недоступен из пакета orchestrator — использовать `o.triggerWithSeq` НО с `o.hooks=nil` на момент seed, затем присвоить `o.hooks=disp` перед `reconcileLifecycleOutbox` (эмуляция «события применились в прошлом ране без этого dispatcher»). Второй вариант проще и надёжнее — задокументировать его в тесте.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run TestReconcile -v`
Expected: FAIL (метода нет).

- [ ] **Step 3: Write minimal implementation**

`lifecycle_emit.go`:

```go
// reconcileLifecycleOutbox восстанавливает durable-доставку FSM-производных
// lifecycle-событий после рестарта (Phase 2). По журналу переходов
// (events.jsonl через Store.History) для каждого durable FSM-перехода,
// мапящегося в публичное событие, гарантирует наличие конверта в outbox
// (EnsureEnvelope закрывает окно краха «переход записан, конверт — нет»), затем
// Redeliver доставляет все недоставленные пары (конверт, хук). На свежем ране
// история пуста → no-op. nil hooks/outbox → no-op (EnsureEnvelope/Redeliver
// сами это проверяют). Не-FSM события (flow_*, script_*, авто-ответы) НЕ
// реконструируются — они best-effort (нет durable-источника для реконсиляции).
func (o *Orchestrator) reconcileLifecycleOutbox() {
	if o.hooks == nil {
		return
	}
	hist, err := o.opts.Store.History()
	if err != nil {
		log.Printf("WARN: lifecycle reconcile: read history: %v", err)
		return
	}
	for _, tr := range hist {
		le, ok := fsmLifecycleEvents[bus.FSMEvent(tr.Event)]
		if !ok {
			continue
		}
		o.hooks.EnsureEnvelope(lifecyclehooks.Event{
			Type:      le,
			StageID:   tr.StageID,
			StageName: o.stageName(tr.StageID),
			From:      string(tr.From),
			To:        string(tr.To),
			Phase:     phaseForStatus(tr.From),
			Reason:    tr.Reason,
			Seq:       tr.Seq,
			Time:      tr.Time,
		})
	}
	o.hooks.Redeliver()
}
```

`orchestrator.go` — вызвать в `Run` СРАЗУ после emit flow_started/resumed и ДО recoverReviewPause/startPlanningForPending (на этот момент история содержит только переходы прошлых ранов — ровно то, что нужно реконсилить; на свежем ране пусто):

```go
	if o.opts.Resumed {
		o.emitLifecycle(lifecyclehooks.Event{Type: lifecyclehooks.EventFlowResumed})
	} else {
		o.emitLifecycle(lifecyclehooks.Event{Type: lifecyclehooks.EventFlowStarted})
	}
	o.reconcileLifecycleOutbox() // Phase 2: durable-переотправка пропущенного (no-op без outbox/на свежем ране)
```

Проверить импорты `bus`, `lifecyclehooks`, `log` в соответствующих файлах.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/ -run TestReconcile -v && go test ./pkg/orchestrator/... -count=1`
Expected: PASS (новые + все существующие; на свежем ране reconcile — no-op, existing тесты не меняются).

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/
git commit -m "feat(orchestrator): реконсиляция lifecycle-outbox по истории переходов при старте"
```

---

### Task 4: Сборка outbox в `cmd/afm/run.go` + end-to-end crash-recovery тест

**Files:**
- Modify: `cmd/afm/run.go`
- Test: `pkg/orchestrator/lifecycle_outbox_integration_test.go`

**Interfaces:**
- Consumes: всё выше (`OpenOutbox`, `DispatcherOptions.Outbox`, `reconcileLifecycleOutbox`).
- Produces: production-wiring — outbox открывается на `runDir`, прокидывается в dispatcher, закрывается в unified defer после Stop.

- [ ] **Step 1: Write the failing integration test**

End-to-end: реальный dispatcher с outbox и реальными sh-хуками. Сценарий краха: (1) конверт FSM-события в outbox без доставки → после «рестарта» (новый dispatcher на том же dir + Redeliver) хук получает событие; (2) недоставленное переотправляется ровно один раз. Использовать реальные процессы (RunFunc не подменять) с capture-файлом.

```go
func TestIntegration_OutboxRedeliversAfterRestart(t *testing.T) {
	dir := t.TempDir()
	cap := filepath.Join(dir, "cap.txt")
	// «прошлый ран»: конверт записан, доставка не случилась (краш).
	ob1, err := lifecyclehooks.OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = ob1.RecordEvent("run-1:transition:5:stage_finished", lifecyclehooks.Payload{
		SchemaVersion: 1, EventID: "run-1:transition:5:stage_finished",
		Event: lifecyclehooks.EventStageFinished, Stage: &lifecyclehooks.StageInfo{ID: "s1", To: "done"},
	})
	ob1.Close()

	// «рестарт»: новый outbox + dispatcher с реальным sh-хуком, Redeliver.
	ob2, err := lifecyclehooks.OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ob2.Close() })
	disp := lifecyclehooks.New(lifecyclehooks.DispatcherOptions{
		Config: lifecyclehooks.DispatcherConfig{RunID: "run-1", RunDir: dir, RootDir: dir},
		Hooks: []lifecyclehooks.RegisteredHook{{Hook: lifecyclehooks.Hook{
			ID: "cap", Events: lifecyclehooks.EventSelector{All: true},
			Command: "sh -c 'cat >> " + cap + "'"}}},
		LogDir: filepath.Join(dir, "hooks"), Outbox: ob2,
	})
	disp.Start()
	t.Cleanup(disp.Stop)
	disp.Redeliver()
	if !disp.Flush(5 * time.Second) {
		t.Fatal("flush timeout")
	}
	raw, _ := os.ReadFile(cap)
	if !strings.Contains(string(raw), `"event":"stage_finished"`) {
		t.Fatalf("redelivered event missing from capture: %s", raw)
	}
	if !ob2.Delivered("run-1:transition:5:stage_finished", "cap") {
		t.Fatal("redelivery not recorded")
	}

	// Идемпотентность: второй Redeliver ничего не шлёт.
	before := string(raw)
	disp.Redeliver()
	disp.Flush(2 * time.Second)
	raw2, _ := os.ReadFile(cap)
	if string(raw2) != before {
		t.Fatalf("second redeliver must be a no-op:\n%s\n---\n%s", before, raw2)
	}
}
```

- [ ] **Step 2: Run test to verify it fails/passes**

Run: `go test ./pkg/orchestrator/ -run TestIntegration_OutboxRedelivers -v`
Expected: этот тест проверяет уровень dispatcher+outbox и должен пройти уже после Tasks 1-2 (страховка регресса для wiring). Если падает — чинить в пакете, не в run.go.

- [ ] **Step 3: Modify `cmd/afm/run.go`**

В блоке сборки dispatcher (после `Combine`, если `len(combined) > 0`), ДО `lifecyclehooks.New`:

```go
			var hooksOutbox *lifecyclehooks.Outbox
			if ob, oerr := lifecyclehooks.OpenOutbox(runDir); oerr != nil {
				// outbox — durable-надстройка; её сбой не должен ронять ран.
				// Без outbox доставка деградирует до live best-effort (Phase 1).
				fmt.Fprintf(os.Stderr, "warning: lifecycle outbox disabled: %v\n", oerr)
			} else {
				hooksOutbox = ob
			}
```

Передать в options: `Outbox: hooksOutbox,` (рядом с `Config/Hooks/LogDir/OnError`).

В unified defer (Flush → Stop) добавить закрытие outbox ПОСЛЕ Stop:

```go
				defer func() {
					if hooksDisp != nil {
						if !hooksDisp.Flush(lifecyclehooks.FlushTimeout) {
							fmt.Fprintf(os.Stderr, "warning: lifecycle hooks flush timed out\n")
						}
						hooksDisp.Stop()
					}
					if hooksOutbox != nil {
						_ = hooksOutbox.Close()
					}
				}()
```

Реконсиляция уже вызывается внутри `Run` (Task 3) — в run.go отдельного вызова не нужно; убедиться, что `orch.Run` идёт после `hooksDisp.Start()` (Redeliver внутри reconcile требует поднятых воркеров).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./cmd/... && go test ./pkg/orchestrator/... ./pkg/lifecyclehooks/ -count=1 && GOOS=windows go build ./pkg/lifecyclehooks/ && go vet ./...`
Expected: PASS; обе сборки чисты.

- [ ] **Step 5: Commit**

```bash
git add cmd/afm/run.go pkg/orchestrator/
git commit -m "feat(run): подключение durable-outbox lifecycle-хуков и crash-recovery тест"
```

---

### Task 5: Документация AGENTS.md (Phase 2) + финальная верификация

**Files:**
- Modify: `AGENTS.md`

- [ ] **Step 1: Обновить раздел «Lifecycle hooks» в AGENTS.md**

Дописать в существующий раздел подпункт про Phase 2 (плотный русский стиль, ссылки на файлы):
- durable outbox: `<runDir>/hook_events.jsonl` (envelope=event_id+payload, fsync до команды) + `hook_deliveries.jsonl` (пары event_id+hook_id); `pkg/lifecyclehooks/outbox.go`.
- at-least-once ТОЛЬКО для FSM-производных событий; flow_*/script_*/авто-ответы остаются best-effort (нет durable-источника для реконсиляции).
- механика: `Emit` пишет конверт (fsync) до enqueue; воркер пишет доставку после успеха; провал доставки → пара не пишется → переотправка после рестарта.
- реконсиляция при старте (`reconcileLifecycleOutbox`, orchestrator): `Store.History()` → `EnsureEnvelope` для каждого durable FSM-перехода (закрывает окно «переход записан, конверт — нет») → `Redeliver` недоставленных пар. Вызывается в `Run` до `startPlanningForPending`; на свежем ране no-op.
- получатель использует `AFM_HOOK_EVENT_ID`/`event_id` как ключ идемпотентности (возможны дубли при at-least-once).
- outbox опционален: сбой открытия → warning, деградация до Phase 1 live best-effort; ран не падает.
- Phase 3 (секреты/Docker) — по-прежнему впереди.

- [ ] **Step 2: Финальная верификация**

```bash
make lint
make test
GOOS=windows go build ./pkg/lifecyclehooks/
```
Expected: lint 0 issues; Go-сьют зелёный (учесть известный предсуществующий флейк `TestDefaultExecFunc_ForwardsSignalToChild` в pkg/docker — не относится к этой ветке); windows-сборка чиста.

- [ ] **Step 3: Commit**

```bash
git add AGENTS.md
git commit -m "docs: durable-доставка lifecycle-хуков (Phase 2) в AGENTS.md"
```

---

## Self-Review (выполнен автором плана)

- **Spec coverage:** outbox hook_events.jsonl+hook_deliveries.jsonl с fsync (T1); envelope-до-команды + delivery-после (T2); Redeliver недоставленного после рестарта (T2/T4); реконсиляция events.jsonl↔outbox по seq (T3); at-least-once только для FSM-производных, не-FSM best-effort (T3 — reconcile мапит только fsmLifecycleEvents); wiring + Close (T4); документация + финальная верификация (T5).
- **Placeholder scan:** код во всех шагах полный; места сверки с существующими хелперами (orchestrator test infra, seedTransition) снабжены точным указанием, что искать и по какому образцу, включая явное предупреждение про «seed через store, а не через emit» для корректной симуляции краха.
- **Type consistency:** `OpenOutbox(dir)`, `RecordEvent(eventID,p)`, `RecordDelivery(eventID,hookID)`, `HasEvent`/`Delivered`/`Envelopes`/`Close`, `DispatcherOptions.Outbox`, `EnsureEnvelope(Event)`, `Redeliver()`, `matchesPayload(rh,p)`, `reconcileLifecycleOutbox()` — единообразны во всех задачах. `fsmLifecycleEvents[bus.FSMEvent(tr.Event)]` — ключ по строковому имени FSM-события из `Transition.Event`.
- **Инвариант:** все точки записи outbox при ошибке идут в `reportError`/stderr/log и НЕ трогают FSM/ран (best-effort надстройка). nil-outbox = поведение Phase 1 (отдельный regress-тест `TestDispatcher_NilOutboxUnchanged`).
