# Lifecycle-хуки Phase 1 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Реализовать Phase 1 lifecycle-хуков из спеки — observer-only хуки на 4 слоях (global/project/flow/stage), публичный каталог событий, JSON-payload в stdin + `AFM_*` env, timeout/retries, live best-effort доставка (без outbox/секретов/Docker).

**Architecture:** Новый пакет `pkg/lifecyclehooks` (типы, валидация, матчер, комбинатор слоёв, runner команд, dispatcher с per-hook очередями). Orchestrator получает `Options.Hooks *lifecyclehooks.Dispatcher` и эмитит события из `triggerWithSeq` (FSM-переходы), `Run` (flow-события + bounded flush), границ script-stage/script_before/script_after и dialog auto-answer. Сборка dispatcher — в `cmd/afm/run.go`. Ошибка хука никогда не трогает FSM.

**Tech Stack:** Go (stdlib only для нового пакета), yaml.v3 (уже в проекте), существующие пакеты `pkg/flow`, `pkg/config`, `pkg/orchestrator`, `pkg/orchestrator/bus`, `pkg/orchestrator/stagefiles`.

**Spec:** `docs/superpowers/specs/2026-09-18-lifecycle-hooks-design.md` — раздел «Phase 1» и все разделы, не помеченные Phase 2/3.

## Global Constraints

- Все коммиты — на русском, БЕЗ Co-Authored-By.
- Не менять версию Go в go.mod.
- После каждой задачи: `go build ./... && go vet ./...` чисто; в конце задачи — `golangci-lint run ./...` (0 issues), если быстро — полный `make lint`.
- Новое YAML-поле в Config/Flow требует обновления `schema/config.schema.json` / `schema/flow.schema.json`, иначе падает guard в `pkg/schemacheck`.
- Терминология: пакет `pkg/lifecyclehooks`; НЕ трогать существующий код script_before/after (`pkg/orchestrator/hooks.go`, кроме точечных вставок эмиссии).
- Существующие события script-хуков называются `stage_script_before_*` / `stage_script_after_*` (НЕ `stage_before_hook_*`).
- Хук-процесс в Phase 1 наследует окружение AFM (`os.Environ()` + `AFM_*` поверх).
- Ошибка lifecycle-хука: лог + dashboard-notice, никогда не меняет FSM/статус/результат.
- Доставка best-effort: переполнение очереди хука → дроп + warning-notice, не блокируем эмиттера.

## File Structure

Новые файлы:

- `pkg/lifecyclehooks/types.go` — каталог событий (EventType-константы), EventSelector, Hook, RegisteredHook, Event, Payload/FlowInfo/StageInfo, DispatcherConfig.
- `pkg/lifecyclehooks/validate.go` — ValidateLayer.
- `pkg/lifecyclehooks/matcher.go` — Matches + scope-матчинг.
- `pkg/lifecyclehooks/combine.go` — Combine слоёв с precedence по id.
- `pkg/lifecyclehooks/runner.go` — runCommand: sh -c, stdin, env, лог, timeout, retries, process-group kill.
- `pkg/lifecyclehooks/dispatcher.go` — Dispatcher: New/Start/Emit/Flush/Stop, per-hook воркеры.
- `pkg/orchestrator/lifecycle_emit.go` — FSM→lifecycle мапинг + nil-safe хелперы эмиссии.
- `pkg/orchestrator/lifecycle_*_test.go` — тесты эмиссии + интеграционный.

Модифицируемые:

- `pkg/config/config.go` — Config.Hooks + keyed merge в mergeFile + валидация в LoadFrom.
- `pkg/flow/flow.go` — Flow.Hooks, Stage.Hooks + валидация в Flow.validate().
- `pkg/orchestrator/orchestrator.go` — Options.{FlowName,RunID,Resumed,Hooks}, поле Orchestrator.hooks, вызов из triggerWithSeq, flow-события в Run.
- `pkg/orchestrator/hooks.go` — эмиссия stage_script_before_* / stage_script_after_*.
- `pkg/orchestrator/agents.go` — эмиссия stage_script_*.
- `pkg/orchestrator/dialog_poller.go` — эмиссия stage_question_asked/answered при авто-ответе.
- `pkg/orchestrator/bus/bus.go` — EventType EventLifecycleHookFailed.
- `cmd/afm/run.go` — сборка dispatcher, Options, flush/Stop после Run.
- `schema/config.schema.json`, `schema/flow.schema.json` — поле hooks.
- `pkg/web/dashboard/src/components/feed-workspace/feed-view-model.ts` — рендер lifecycle_hook_failed.
- `AGENTS.md` — раздел про lifecycle-хуки.

Ссылки на код (проверено на 2026-09-18, ветка notify):

- `Orchestrator.Options` — orchestrator.go:57-94; `New` — :394.
- `triggerWithSeq` — orchestrator.go:489-524 (общий funnel, возвращает `to, seq, ok`).
- `Run` — orchestrator.go:530-598; выходы: ctx.Done+fatal :569-571, ctx.Done без fatal :572, handleEvent err :574-576, shouldExit→runEndOfRunMemory→nil :580-595.
- `o.currentStatus(stageID)`, `o.graph.Stage(stageID)` — существуют.
- `runScriptStage` — agents.go:43-62; `runBeforeHook` — hooks.go:212-260; `runAfterHook` — hooks.go:268-311.
- dialog auto-answer — dialog_poller.go:197-232 (WriteAnswer при `stage != nil && !stage.Interactive`).
- `Config`/`mergeFile` — config.go:331-350, 464-563; `LoadFrom` — :406-421.
- `Flow`/`Stage`/`ParseFile` — flow.go:313-330, 135-203, 336-367; валидация стадии — блок buttons :561-581, reflect :639-667.
- `publishHookNotice` (ui + AppendNotice, одна точка) — hooks.go:202-205.
- `store.Snapshot() RunState` (с `LastSeq`) — store.go:189; `AllDone()` у RunState — см. usage в scheduling.go:428-443.
- FSM-события — bus/fsm.go:19-49: EvStartPlanning, EvPlanReady, EvApprove, EvRevise, EvStartRun, EvComplete, EvFail, EvAskUser, EvUserAnswered, EvScheduleRetry, EvResumeAfterRetry, EvManualRetry, EvPause, EvContinue (+EvReady/EvBlockedByDep/EvHookFailed/EvHookResolved — без lifecycle-мапинга).
- Тест-хелперы orchestrator: `runOrchestratorAsync`, `waitForStatus` (testrun_helper_test.go).

---

### Task 1: Каталог событий и типы (`pkg/lifecyclehooks/types.go`)

**Files:**
- Create: `pkg/lifecyclehooks/types.go`
- Test: `pkg/lifecyclehooks/types_test.go`

**Interfaces:**
- Consumes: ничего (чистый stdlib + yaml.v3).
- Produces (используют Tasks 2-13):
  - `type EventType string`; константы из каталога ниже; `AllEvents() []EventType`; `IsFlowEvent(ev EventType) bool`.
  - `type EventSelector struct { All bool; Events []EventType }` с `UnmarshalYAML(*yaml.Node) error`.
  - `type Hook struct { ID string; Events EventSelector; SkipEvents []EventType; Command string; Timeout time.Duration; Retries int }` с yaml-тегами `id,events,skip_events,omitempty,command,timeout,omitempty,retries,omitempty`.
  - `type RegisteredHook struct { Hook Hook; StageID string }` (StageID `""` = все стадии).
  - `type Event struct { Type EventType; StageID, StageName, From, To, Phase, Reason string; Seq uint64; Time time.Time }`.
  - `type Payload struct {...}` + `BuildPayload(cfg DispatcherConfig, ev Event, eventID string) Payload` + `type DispatcherConfig struct { FlowName, RunID, RunDir, RootDir string; Resumed bool }`.

- [ ] **Step 1: Write the failing test**

```go
package lifecyclehooks

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestEventSelectorUnmarshal(t *testing.T) {
	cases := []struct {
		name    string
		yamlIn  string
		want    EventSelector
		wantErr bool
	}{
		{"scalar all", "events: all\n", EventSelector{All: true}, false},
		{"list", "events: [stage_failed, flow_started]\n", EventSelector{Events: []EventType{"stage_failed", "flow_started"}}, false},
		{"scalar not all", "events: everything\n", EventSelector{}, true},
		{"mapping", "events: {a: 1}\n", EventSelector{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s struct {
				Events EventSelector `yaml:"events"`
			}
			err := yaml.Unmarshal([]byte(tc.yamlIn), &s)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", s.Events)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if s.Events.All != tc.want.All || len(s.Events.Events) != len(tc.want.Events) {
				t.Fatalf("got %+v, want %+v", s.Events, tc.want)
			}
			for i := range tc.want.Events {
				if s.Events.Events[i] != tc.want.Events[i] {
					t.Fatalf("events[%d]: got %q want %q", i, s.Events.Events[i], tc.want.Events[i])
				}
			}
		})
	}
}

func TestHookUnmarshal(t *testing.T) {
	yamlIn := `
id: notify
events: all
skip_events:
  - stage_question_answered
command: "bash /opt/notify.sh"
timeout: 30s
retries: 2
`
	var h Hook
	if err := yaml.Unmarshal([]byte(yamlIn), &h); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if h.ID != "notify" || !h.Events.All || h.Command != "bash /opt/notify.sh" {
		t.Fatalf("got %+v", h)
	}
	if h.Timeout != 30_000_000_000 || h.Retries != 2 {
		t.Fatalf("timeout=%v retries=%d", h.Timeout, h.Retries)
	}
	if len(h.SkipEvents) != 1 || h.SkipEvents[0] != EventStageQuestionAnswered {
		t.Fatalf("skip: %+v", h.SkipEvents)
	}
}

func TestAllEventsCatalog(t *testing.T) {
	got := make(map[EventType]bool)
	for _, ev := range AllEvents() {
		if got[ev] {
			t.Errorf("duplicate catalog entry %q", ev)
		}
		got[ev] = true
	}
	if len(got) != 27 {
		t.Errorf("catalog size: got %d want 27 (%v)", len(got), AllEvents())
	}
	for _, ev := range AllEvents() {
		if ev == "" {
			t.Error("empty event name in catalog")
		}
	}
}

func TestIsFlowEvent(t *testing.T) {
	if !IsFlowEvent(EventFlowStarted) {
		t.Error("flow_started must be a flow event")
	}
	if IsFlowEvent(EventStageFailed) {
		t.Error("stage_failed must not be a flow event")
	}
}

func TestBuildPayload(t *testing.T) {
	cfg := DispatcherConfig{FlowName: "release", RunID: "run-1", RunDir: "/r", RootDir: "/p", Resumed: false}
	ev := Event{Type: EventStageFailed, StageID: "deploy", StageName: "Deploy", From: "running", To: "failed", Phase: "implementation", Reason: "boom", Seq: 42, Time: time.Unix(0, 0).UTC()}
	p := BuildPayload(cfg, ev, "run-1:transition:42:stage_failed")
	if p.SchemaVersion != 1 || p.Event != EventStageFailed || p.EventID != "run-1:transition:42:stage_failed" {
		t.Fatalf("got %+v", p)
	}
	if p.Flow.RunID != "run-1" || p.Flow.Name != "release" || p.Stage == nil || p.Stage.To != "failed" || p.Reason != "boom" {
		t.Fatalf("got %+v", p)
	}
	// flow-событие: Stage == nil
	fe := Event{Type: EventFlowFinished, Time: time.Unix(0, 0).UTC()}
	if p2 := BuildPayload(cfg, fe, "run-1:flow:flow_finished"); p2.Stage != nil {
		t.Fatalf("flow payload must have no stage, got %+v", p2.Stage)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/lifecyclehooks/ -run 'TestEventSelector|TestHookUnmarshal|TestAllEvents|TestIsFlowEvent|TestBuildPayload' -v`
Expected: FAIL — пакет/типы не существуют (compile error).

- [ ] **Step 3: Write minimal implementation (`types.go`)**

```go
// Package lifecyclehooks реализует observer-only lifecycle-хуки AFM (Phase 1):
// команды, получающие JSON-payload события в stdin. Ошибка хука никогда не
// меняет FSM. Спека: docs/superpowers/specs/2026-09-18-lifecycle-hooks-design.md.
package lifecyclehooks

import (
	"time"

	"gopkg.in/yaml.v3"
)

// EventType — публичное имя lifecycle-события. Стабильный контракт: сырые
// имена FSM (Ev*) и события шины наружу не отдаются.
type EventType string

// События флоу.
const (
	EventFlowStarted     EventType = "flow_started"
	EventFlowResumed     EventType = "flow_resumed"
	EventFlowFinished    EventType = "flow_finished"
	EventFlowFailed      EventType = "flow_failed"
	EventFlowInterrupted EventType = "flow_interrupted"
)

// События стадии (FSM-производные, кроме question_* на авто-ответах).
const (
	EventStagePlanningStarted  EventType = "stage_planning_started"
	EventStagePlanReady        EventType = "stage_plan_ready"
	EventStageApproved         EventType = "stage_approved"
	EventStageRevisionStarted  EventType = "stage_revision_started"
	EventStageExecutionStarted EventType = "stage_execution_started"
	EventStageQuestionAsked    EventType = "stage_question_asked"
	EventStageQuestionAnswered EventType = "stage_question_answered"
	EventStageRetryScheduled   EventType = "stage_retry_scheduled"
	EventStageRetryStarted     EventType = "stage_retry_started"
	EventStagePaused           EventType = "stage_paused"
	EventStageResumed          EventType = "stage_resumed"
	EventStageFinished         EventType = "stage_finished"
	EventStageFailed           EventType = "stage_failed"
)

// События script-стадии и script_before/script_after (не-FSM границы).
const (
	EventStageScriptStarted        EventType = "stage_script_started"
	EventStageScriptFinished       EventType = "stage_script_finished"
	EventStageScriptFailed         EventType = "stage_script_failed"
	EventStageScriptBeforeStarted  EventType = "stage_script_before_started"
	EventStageScriptBeforeFinished EventType = "stage_script_before_finished"
	EventStageScriptBeforeFailed   EventType = "stage_script_before_failed"
	EventStageScriptAfterStarted   EventType = "stage_script_after_started"
	EventStageScriptAfterFinished  EventType = "stage_script_after_finished"
	EventStageScriptAfterFailed    EventType = "stage_script_after_failed"
)

var flowEvents = map[EventType]bool{
	EventFlowStarted: true, EventFlowResumed: true, EventFlowFinished: true,
	EventFlowFailed: true, EventFlowInterrupted: true,
}

// IsFlowEvent сообщает, относится ли событие к флоу (недопустимо в stage hook).
func IsFlowEvent(ev EventType) bool { return flowEvents[ev] }

// AllEvents возвращает весь публичный каталог (для `events: all` и валидации).
func AllEvents() []EventType {
	return []EventType{
		EventFlowStarted, EventFlowResumed, EventFlowFinished, EventFlowFailed, EventFlowInterrupted,
		EventStagePlanningStarted, EventStagePlanReady, EventStageApproved,
		EventStageRevisionStarted, EventStageExecutionStarted,
		EventStageQuestionAsked, EventStageQuestionAnswered,
		EventStageRetryScheduled, EventStageRetryStarted,
		EventStagePaused, EventStageResumed, EventStageFinished, EventStageFailed,
		EventStageScriptStarted, EventStageScriptFinished, EventStageScriptFailed,
		EventStageScriptBeforeStarted, EventStageScriptBeforeFinished, EventStageScriptBeforeFailed,
		EventStageScriptAfterStarted, EventStageScriptAfterFinished, EventStageScriptAfterFailed,
	}
}

// EventSelector: скаляр `all` или список имён событий (scalar-or-list, та же
// идиома, что flow.Input scalar-or-object).
type EventSelector struct {
	All    bool
	Events []EventType
}

// UnmarshalYAML декодирует `events: all` либо `events: [a, b]`.
func (s *EventSelector) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Value != "all" {
			return fmt.Errorf("events: scalar must be \"all\", got %q", value.Value)
		}
		s.All = true
		return nil
	case yaml.SequenceNode:
		var evs []EventType
		if err := value.Decode(&evs); err != nil {
			return fmt.Errorf("events: %w", err)
		}
		s.Events = evs
		return nil
	default:
		return fmt.Errorf("events must be \"all\" or a list of event names")
	}
}

// Hook — декларация lifecycle-хука (одинаковая для config/flow/stage слоёв).
type Hook struct {
	ID         string        `yaml:"id"`
	Events     EventSelector `yaml:"events"`
	SkipEvents []EventType   `yaml:"skip_events,omitempty"`
	Command    string        `yaml:"command"`
	Timeout    time.Duration `yaml:"timeout,omitempty"` // 0 → DefaultHookTimeout в runner
	Retries    int           `yaml:"retries,omitempty"` // 0 → одна попытка
}

// RegisteredHook — хук, вписанный в итоговый список: StageID "" = все стадии.
type RegisteredHook struct {
	Hook    Hook
	StageID string
}

// Event — событие, передаваемое в Emit. Seq > 0 только у FSM-производных
// (durable seq перехода) — из него строится стабильный event_id.
type Event struct {
	Type      EventType
	StageID   string
	StageName string
	From      string
	To        string
	Phase     string
	Reason    string
	Seq       uint64
	Time      time.Time
}

// DispatcherConfig — метаданные рана для payload/env.
type DispatcherConfig struct {
	FlowName string
	RunID    string
	RunDir   string
	RootDir  string
	Resumed  bool
}

// FlowInfo — блок flow JSON-payload.
type FlowInfo struct {
	Name    string `json:"name"`
	RunID   string `json:"run_id"`
	Resumed bool   `json:"resumed"`
	RunDir  string `json:"run_dir"`
	RootDir string `json:"root_dir"`
}

// StageInfo — блок stage JSON-payload (nil для flow-событий).
type StageInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
	Phase string `json:"phase,omitempty"`
}

// Payload — JSON-контракт hook-процесса (stdin).
type Payload struct {
	SchemaVersion int            `json:"schema_version"`
	EventID       string         `json:"event_id"`
	Event         EventType      `json:"event"`
	OccurredAt    time.Time      `json:"occurred_at"`
	Flow          FlowInfo       `json:"flow"`
	Stage         *StageInfo     `json:"stage,omitempty"`
	Reason        string         `json:"reason,omitempty"`
	Data          map[string]any `json:"data,omitempty"`
}

// BuildPayload собирает Payload из метаданных и события.
func BuildPayload(cfg DispatcherConfig, ev Event, eventID string) Payload {
	p := Payload{
		SchemaVersion: 1,
		EventID:       eventID,
		Event:         ev.Type,
		OccurredAt:    ev.Time,
		Flow: FlowInfo{
			Name: cfg.FlowName, RunID: cfg.RunID, Resumed: cfg.Resumed,
			RunDir: cfg.RunDir, RootDir: cfg.RootDir,
		},
		Reason: ev.Reason,
	}
	if ev.StageID != "" {
		p.Stage = &StageInfo{
			ID: ev.StageID, Name: ev.StageName,
			From: ev.From, To: ev.To, Phase: ev.Phase,
		}
	}
	return p
}
```

Не забыть импорт `fmt` в types.go.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/lifecyclehooks/ -v`
Expected: PASS (все тесты пакета).

- [ ] **Step 5: Commit**

```bash
git add pkg/lifecyclehooks/
git commit -m "feat(lifecyclehooks): каталог событий, EventSelector и типы payload"
```

---

### Task 2: Валидация и матчер (`validate.go`, `matcher.go`)

**Files:**
- Create: `pkg/lifecyclehooks/validate.go`, `pkg/lifecyclehooks/matcher.go`
- Test: `pkg/lifecyclehooks/validate_test.go`

**Interfaces:**
- Consumes: Task 1 (Hook, EventSelector, EventType, AllEvents, IsFlowEvent).
- Produces:
  - `func ValidateLayer(defs []Hook, stageScoped bool) error` — stageScoped=true запрещает flow-события.
  - `func (h Hook) Matches(ev EventType) bool`.
  - `func (rh RegisteredHook) MatchesEvent(ev Event) bool` — scope + события.

- [ ] **Step 1: Write the failing test**

```go
package lifecyclehooks

import "testing"

func TestValidateLayer(t *testing.T) {
	valid := Hook{ID: "n", Events: EventSelector{Events: []EventType{EventStageFailed}}, Command: "true"}
	cases := []struct {
		name    string
		defs    []Hook
		stage   bool
		wantErr string
	}{
		{"ok", []Hook{valid}, false, ""},
		{"ok stage-scoped", []Hook{{ID: "s", Events: EventSelector{All: true}, Command: "true"}}, true, ""},
		{"empty id", []Hook{{ID: "", Events: EventSelector{All: true}, Command: "true"}}, false, "id"},
		{"empty command", []Hook{{ID: "x", Events: EventSelector{All: true}, Command: ""}}, false, "command"},
		{"no events", []Hook{{ID: "x", Command: "true"}}, false, "events"},
		{"unknown event", []Hook{{ID: "x", Events: EventSelector{Events: []EventType{"stage_strted"}}, Command: "true"}}, false, "unknown"},
		{"unknown skip", []Hook{{ID: "x", Events: EventSelector{All: true}, SkipEvents: []EventType{"nope"}, Command: "true"}}, false, "unknown"},
		{"dup id in layer", []Hook{valid, {ID: "n", Events: EventSelector{All: true}, Command: "true"}}, false, "duplicate"},
		{"flow event in stage hook", []Hook{{ID: "x", Events: EventSelector{Events: []EventType{EventFlowFinished}}, Command: "true"}}, true, "flow-level"},
		{"flow event ok in flow layer", []Hook{{ID: "x", Events: EventSelector{Events: []EventType{EventFlowFinished}}, Command: "true"}}, false, ""},
		{"negative retries", []Hook{{ID: "x", Events: EventSelector{All: true}, Command: "true", Retries: -1}}, false, "retries"},
		{"negative timeout", []Hook{{ID: "x", Events: EventSelector{All: true}, Command: "true", Timeout: -1}}, false, "timeout"},
		// id становится именем файла лога — path traversal запрещён (codex CRIT#1):
		{"id traversal", []Hook{{ID: "../events.jsonl", Events: EventSelector{All: true}, Command: "true"}}, false, "id"},
		{"id with slash", []Hook{{ID: "a/b", Events: EventSelector{All: true}, Command: "true"}}, false, "id"},
		{"id dot", []Hook{{ID: ".", Events: EventSelector{All: true}, Command: "true"}}, false, "id"},
		{"id dotdot", []Hook{{ID: "..", Events: EventSelector{All: true}, Command: "true"}}, false, "id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLayer(tc.defs, tc.stage)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestMatches(t *testing.T) {
	all := Hook{ID: "a", Events: EventSelector{All: true}, Command: "true"}
	allSkip := Hook{ID: "b", Events: EventSelector{All: true}, SkipEvents: []EventType{EventStageQuestionAnswered}, Command: "true"}
	list := Hook{ID: "c", Events: EventSelector{Events: []EventType{EventStageFailed, EventFlowFinished}}, Command: "true"}
	empty := Hook{ID: "d", Command: "true"}

	if !all.Matches(EventStageFailed) || !all.Matches(EventFlowStarted) {
		t.Error("all must match everything")
	}
	if allSkip.Matches(EventStageQuestionAnswered) {
		t.Error("skip_events must exclude")
	}
	if !allSkip.Matches(EventStageFailed) {
		t.Error("skip must not exclude others")
	}
	if list.Matches(EventStageFinished) {
		t.Error("list must not match unlisted")
	}
	if !list.Matches(EventFlowFinished) || !list.Matches(EventStageFailed) {
		t.Error("list must match listed")
	}
	if empty.Matches(EventStageFailed) {
		t.Error("empty selector matches nothing")
	}
}

func TestRegisteredHookScope(t *testing.T) {
	global := RegisteredHook{Hook: Hook{Events: EventSelector{All: true}}, StageID: ""}
	scoped := RegisteredHook{Hook: Hook{Events: EventSelector{All: true}}, StageID: "deploy"}
	evOther := Event{Type: EventStageFailed, StageID: "build"}
	evOwn := Event{Type: EventStageFailed, StageID: "deploy"}

	if !global.MatchesEvent(evOther) || !global.MatchesEvent(evOwn) {
		t.Error("global scope matches all stages")
	}
	if scoped.MatchesEvent(evOther) {
		t.Error("stage scope must not match other stages")
	}
	if !scoped.MatchesEvent(evOwn) {
		t.Error("stage scope must match own stage")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/lifecyclehooks/ -run 'TestValidateLayer|TestMatches|TestRegisteredHookScope' -v`
Expected: FAIL (compile error — функций нет).

- [ ] **Step 3: Write minimal implementation**

`validate.go`:

```go
package lifecyclehooks

import (
	"fmt"
	"strings"
)

// ValidateLayer проверяет слой хуков (config-слой, flow-слой или хуки одной
// стадии). stageScoped=true — хуки конкретной стадии: flow-события в них
// запрещены (stage hook физически не может получить событие всего флоу).
func ValidateLayer(defs []Hook, stageScoped bool) error {
	known := make(map[EventType]bool, 32)
	for _, ev := range AllEvents() {
		known[ev] = true
	}
	ids := make(map[string]bool, len(defs))
	for i, h := range defs {
		if h.ID == "" {
			return fmt.Errorf("hooks[%d]: id is required", i)
		}
		// id становится именем файла лога (<LogDir>/<id>.log) — обязан быть
		// безопасным одиночным компонентом пути, иначе `id: ../events.jsonl`
		// допишет hook-лог в авторитетный events.jsonl (codex CRIT#1).
		if h.ID == "." || h.ID == ".." || strings.ContainsAny(h.ID, "/\\\x00") {
			return fmt.Errorf("hooks[%d]: id %q must be a single safe path component (no /, \\, NUL, dot segments)", i, h.ID)
		}
		if ids[h.ID] {
			return fmt.Errorf("hooks[%d]: duplicate id %q in the same layer", i, h.ID)
		}
		ids[h.ID] = true
		if h.Command == "" {
			return fmt.Errorf("hooks[%d] (%s): command is required", i, h.ID)
		}
		if !h.Events.All && len(h.Events.Events) == 0 {
			return fmt.Errorf("hooks[%d] (%s): events must be \"all\" or a non-empty list", i, h.ID)
		}
		for _, ev := range h.Events.Events {
			if !known[ev] {
				return fmt.Errorf("hooks[%d] (%s): unknown event %q", i, h.ID, ev)
			}
			if stageScoped && IsFlowEvent(ev) {
				return fmt.Errorf("hooks[%d] (%s): flow-level event %q is not allowed in a stage hook", i, h.ID, ev)
			}
		}
		for _, ev := range h.SkipEvents {
			if !known[ev] {
				return fmt.Errorf("hooks[%d] (%s): unknown skip_events entry %q", i, h.ID, ev)
			}
		}
		if h.Retries < 0 {
			return fmt.Errorf("hooks[%d] (%s): retries must not be negative", i, h.ID)
		}
		if h.Timeout < 0 {
			return fmt.Errorf("hooks[%d] (%s): timeout must not be negative", i, h.ID)
		}
	}
	return nil
}
```

`matcher.go`:

```go
package lifecyclehooks

// Matches сообщает, должен ли хук сработать для события ev: вход в selector
// минус skip_events.
func (h Hook) Matches(ev EventType) bool {
	in := h.Events.All || containsEvent(h.Events.Events, ev)
	return in && !containsEvent(h.SkipEvents, ev)
}

// MatchesEvent — scope-матчинг: StageID "" получает события всех стадий,
// непустой — только своей.
func (rh RegisteredHook) MatchesEvent(ev Event) bool {
	return (rh.StageID == "" || rh.StageID == ev.StageID) && rh.Hook.Matches(ev.Type)
}

func containsEvent(list []EventType, ev EventType) bool {
	for _, e := range list {
		if e == ev {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/lifecyclehooks/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/lifecyclehooks/
git commit -m "feat(lifecyclehooks): валидация слоя и матчинг событий со скоупом стадии"
```

---

### Task 3: Комбинатор слоёв (`combine.go`)

**Files:**
- Create: `pkg/lifecyclehooks/combine.go`
- Test: `pkg/lifecyclehooks/combine_test.go`

**Interfaces:**
- Consumes: Task 1 (Hook, RegisteredHook).
- Produces: `type Layer struct { StageID string; Hooks []Hook }`; `func Combine(layers ...Layer) []RegisteredHook` — плоский namespace по id: поздний слой заменяет одноимённый хук раннего ЦЕЛИКОМ (включая скоуп), позиция замены не меняется (стабильный порядок); новые id дописываются в порядке появления.

- [ ] **Step 1: Write the failing test**

```go
package lifecyclehooks

import "testing"

func hook(id string) Hook { return Hook{ID: id, Events: EventSelector{All: true}, Command: "true"} }

func TestCombine_LayersAppend(t *testing.T) {
	out := Combine(
		Layer{Hooks: []Hook{hook("a"), hook("b")}},
		Layer{Hooks: []Hook{hook("c")}},
	)
	if len(out) != 3 || out[0].Hook.ID != "a" || out[1].Hook.ID != "b" || out[2].Hook.ID != "c" {
		t.Fatalf("got %+v", out)
	}
	if out[0].StageID != "" || out[2].StageID != "" {
		t.Fatalf("no stage scope expected: %+v", out)
	}
}

func TestCombine_LaterLayerOverridesByID(t *testing.T) {
	replacement := Hook{ID: "a", Events: EventSelector{Events: []EventType{EventFlowFailed}}, Command: "other"}
	out := Combine(
		Layer{Hooks: []Hook{hook("a"), hook("b")}},
		Layer{Hooks: []Hook{replacement}},
	)
	if len(out) != 2 {
		t.Fatalf("len: %+v", out)
	}
	if out[0].Hook.Command != "other" || out[0].Hook.Events.All {
		t.Fatalf("override not applied: %+v", out[0])
	}
	if out[1].Hook.ID != "b" {
		t.Fatalf("order broken: %+v", out)
	}
}

func TestCombine_StageScopeOverridesGlobal(t *testing.T) {
	out := Combine(
		Layer{Hooks: []Hook{hook("a")}},                       // global
		Layer{StageID: "deploy", Hooks: []Hook{hook("a")}},    // stage заменяет global целиком
	)
	if len(out) != 1 {
		t.Fatalf("len: %+v", out)
	}
	if out[0].StageID != "deploy" {
		t.Fatalf("stage replacement must change scope: %+v", out[0])
	}
}

func TestCombine_Empty(t *testing.T) {
	if out := Combine(); out != nil {
		t.Fatalf("got %+v", out)
	}
	if out := Combine(Layer{}); out != nil {
		t.Fatalf("got %+v", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/lifecyclehooks/ -run TestCombine -v`
Expected: FAIL (compile error).

- [ ] **Step 3: Write minimal implementation (`combine.go`)**

```go
package lifecyclehooks

// Layer — слой хуков с опциональным скоупом стадии. Слои передаются в
// Combine в порядке возрастания специфичности: global config, project config
// (эти два уже смёржены в config.LoadFrom), flow, затем по одному слою на
// стадию.
type Layer struct {
	StageID string
	Hooks   []Hook
}

// Combine складывает слои в итоговый список. id — плоский namespace: хук с
// тем же id из более позднего (специфичного) слоя ЗАМЕНЯЕТ ранний целиком,
// включая скоуп стадии, на исходной позиции (порядок стабильный). Новые id
// дописываются в порядке появления.
func Combine(layers ...Layer) []RegisteredHook {
	var out []RegisteredHook
	idx := map[string]int{}
	for _, l := range layers {
		for _, h := range l.Hooks {
			if i, ok := idx[h.ID]; ok {
				out[i] = RegisteredHook{Hook: h, StageID: l.StageID}
				continue
			}
			idx[h.ID] = len(out)
			out = append(out, RegisteredHook{Hook: h, StageID: l.StageID})
		}
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/lifecyclehooks/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/lifecyclehooks/
git commit -m "feat(lifecyclehooks): комбинатор слоёв с заменой по id"
```

---

### Task 4: Runner команд (`runner.go`)

**Files:**
- Create: `pkg/lifecyclehooks/runner.go`
- Test: `pkg/lifecyclehooks/runner_test.go`

**Interfaces:**
- Consumes: Task 1 (Hook, Payload, DispatcherConfig).
- Produces:
  - `const DefaultHookTimeout = 30 * time.Second`; `const hookRetryBackoff = time.Second`.
  - `func buildEnv(cfg DispatcherConfig, p Payload) []string` — `os.Environ()` + 10 `AFM_*`-переменных (для flow-событий стадийные пустые).
  - `func runCommand(ctx context.Context, h Hook, cfg DispatcherConfig, p Payload, logPath string) error` — полный цикл попыток (`Retries+1`), каждая с timeout `h.Timeout || DefaultHookTimeout`, пауза `hookRetryBackoff` между попытками; лог каждой попытки дописывается в logPath.

- [ ] **Step 1: Write the failing test**

```go
package lifecyclehooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunCommand_Success_StdinAndEnv(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.log")
	script := writeScript(t, dir, "capture.sh", `#!/bin/sh
cat > "`+out+`.payload"
printf '%s|%s|%s|%s' "$AFM_HOOK_EVENT" "$AFM_RUN_ID" "$AFM_STAGE_ID" "$AFM_FLOW_NAME" > "`+out+`"
`)
	h := Hook{ID: "cap", Command: script, Timeout: 5 * time.Second}
	cfg := DispatcherConfig{FlowName: "fl", RunID: "run-1", RunDir: dir, RootDir: dir}
	p := BuildPayload(cfg, Event{Type: EventStageFailed, StageID: "deploy", Time: time.Now()}, "eid-1")
	logPath := filepath.Join(dir, "hook.log")

	if err := runCommand(context.Background(), h, cfg, p, logPath); err != nil {
		t.Fatalf("runCommand: %v", err)
	}
	got, _ := os.ReadFile(out)
	if string(got) != "stage_failed|run-1|deploy|fl" {
		t.Fatalf("env capture: %q", got)
	}
	payloadRaw, _ := os.ReadFile(out + ".payload")
	if !strings.Contains(string(payloadRaw), `"schema_version":1`) || !strings.Contains(string(payloadRaw), `"event":"stage_failed"`) {
		t.Fatalf("payload: %s", payloadRaw)
	}
	logRaw, _ := os.ReadFile(logPath)
	if !strings.Contains(string(logRaw), "stage_failed") {
		t.Fatalf("log must mention event: %s", logRaw)
	}
}

func TestRunCommand_RetriesThenSuccess(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "attempts")
	script := writeScript(t, dir, "flaky.sh", `#!/bin/sh
n=$(cat "`+marker+`" 2>/dev/null || echo 0)
n=$((n+1))
echo "$n" > "`+marker+`"
[ "$n" -ge 3 ] || exit 1
`)
	h := Hook{ID: "flaky", Command: script, Timeout: 5 * time.Second, Retries: 3}
	cfg := DispatcherConfig{RunID: "r", RunDir: dir, RootDir: dir}
	p := BuildPayload(cfg, Event{Type: EventFlowStarted, Time: time.Now()}, "eid-2")

	start := time.Now()
	if err := runCommand(context.Background(), h, cfg, p, filepath.Join(dir, "h.log")); err != nil {
		t.Fatalf("expected eventual success: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 2*hookRetryBackoff { // минимум 2 паузы между 3 попытками
		t.Fatalf("backoff not applied: %v", elapsed)
	}
}

func TestRunCommand_FinalFailure(t *testing.T) {
	dir := t.TempDir()
	h := Hook{ID: "bad", Command: "exit 7", Timeout: 5 * time.Second, Retries: 1}
	cfg := DispatcherConfig{RunID: "r", RunDir: dir, RootDir: dir}
	p := BuildPayload(cfg, Event{Type: EventFlowStarted, Time: time.Now()}, "eid-3")

	err := runCommand(context.Background(), h, cfg, p, filepath.Join(dir, "h.log"))
	if err == nil || !strings.Contains(err.Error(), "exit status") {
		t.Fatalf("want exit-status error, got %v", err)
	}
}

func TestRunCommand_TimeoutKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	// скрипт порождает внучка sleep и сам висит: kill только прямого ребёнка
	// оставил бы pipe открытым (урок из pkg/executor — killProcessGroup).
	script := writeScript(t, dir, "hang.sh", `#!/bin/sh
sleep 30 &
sleep 30
`)
	h := Hook{ID: "hang", Command: script, Timeout: 300 * time.Millisecond}
	cfg := DispatcherConfig{RunID: "r", RunDir: dir, RootDir: dir}
	p := BuildPayload(cfg, Event{Type: EventFlowStarted, Time: time.Now()}, "eid-4")

	start := time.Now()
	err := runCommand(context.Background(), h, cfg, p, filepath.Join(dir, "h.log"))
	if err == nil {
		t.Fatal("want timeout error")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("timeout did not kill the group: %v", time.Since(start))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/lifecyclehooks/ -run TestRunCommand -v`
Expected: FAIL (compile error).

- [ ] **Step 3: Write minimal implementation (`runner.go`)**

```go
package lifecyclehooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DefaultHookTimeout применяется, когда h.Timeout == 0. 30s: уведомлящие
// скрипты короткие; 5 минут (как у script-стадий) — слишком щедро для observer.
const DefaultHookTimeout = 30 * time.Second

// hookRetryBackoff — пауза между попытками одного delivery.
const hookRetryBackoff = time.Second

// buildEnv формирует окружение hook-процесса: Phase 1 — полное наследование
// окружения AFM плюс скалярные AFM_* события. Для flow-событий стадийные
// переменные пустые (контракт спеки).
func buildEnv(cfg DispatcherConfig, p Payload) []string {
	env := os.Environ()
	stage := p.Stage
	set := func(k, v string) { env = append(env, k+"="+v) }
	set("AFM_HOOK_EVENT", string(p.Event))
	set("AFM_HOOK_EVENT_ID", p.EventID)
	set("AFM_FLOW_NAME", p.Flow.Name)
	set("AFM_RUN_ID", p.Flow.RunID)
	set("AFM_RUN_DIR", p.Flow.RunDir)
	set("AFM_ROOT_DIR", p.Flow.RootDir)
	if stage != nil {
		set("AFM_STAGE_ID", stage.ID)
		set("AFM_STAGE_NAME", stage.Name)
		set("AFM_STAGE_FROM", stage.From)
		set("AFM_STAGE_TO", stage.To)
	} else {
		set("AFM_STAGE_ID", "")
		set("AFM_STAGE_NAME", "")
		set("AFM_STAGE_FROM", "")
		set("AFM_STAGE_TO", "")
	}
	return env
}

// runCommand выполняет команду хука: sh -c, stdin = JSON-payload, CWD =
// cfg.RootDir, stdout/stderr дописываются в logPath. Попытки: h.Retries+1,
// каждая ограничена timeout. Возвращает nil после первой успешной попытки,
// иначе — последнюю ошибку.
func runCommand(ctx context.Context, h Hook, cfg DispatcherConfig, p Payload, logPath string) error {
	payloadJSON, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	timeout := h.Timeout
	if timeout == 0 {
		timeout = DefaultHookTimeout
	}
	attempts := h.Retries + 1
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		lastErr = execOne(ctx, h, cfg, payloadJSON, logPath, attempt)
		if lastErr == nil {
			return nil
		}
		if attempt < attempts {
			select {
			case <-time.After(hookRetryBackoff):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return lastErr
}

func execOne(ctx context.Context, h Hook, cfg DispatcherConfig, payloadJSON []byte, logPath string, attempt int) error {
	attemptCtx, cancel := context.WithTimeout(ctx, perAttemptTimeout(h))
	defer cancel()

	cmd := exec.CommandContext(attemptCtx, "sh", "-c", h.Command)
	cmd.Dir = cfg.RootDir
	cmd.Stdin = bytes.NewReader(payloadJSON)
	cmd.Env = buildEnv(cfg, buildPayloadForEnv(cfg, payloadJSON))
	// Своя process group: таймаут-килл бьёт по группе (-pid), иначе внук
	// скрипта (sleep &) держал бы pipe и run висел до его конца — тот же
	// урок, что pkg/executor.killProcessGroup.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	appendHookLog(logPath, p_(payloadJSON), buf.String(), attempt, err)
	return err
}

func perAttemptTimeout(h Hook) time.Duration {
	if h.Timeout != 0 {
		return h.Timeout
	}
	return DefaultHookTimeout
}
```

Стоп — в эскизе выше намеренные огрехи, финальный вид ниже (используйте ЕГО):

```go
func execOne(ctx context.Context, h Hook, cfg DispatcherConfig, p Payload, logPath string, attempt int) error {
	timeout := h.Timeout
	if timeout == 0 {
		timeout = DefaultHookTimeout
	}
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	payloadJSON, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	cmd := exec.CommandContext(attemptCtx, "sh", "-c", h.Command)
	cmd.Dir = cfg.RootDir
	cmd.Stdin = bytes.NewReader(payloadJSON)
	cmd.Env = buildEnv(cfg, p)
	// Своя process group: таймаут-килл бьёт по группе (-pid), иначе внук
	// скрипта (sleep &) держал бы stdout-канал и Run висел до его конца —
	// тот же урок, что pkg/executor.killProcessGroup.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}

	// Потоковая запись stdout/stderr прямо в лог-файл (codex MAJ#12): буфер
	// в памяти не ограничен, шумный хук за 30s-таймаута мог бы исчерпать
	// память и уронить afm — observer не должен иметь такой рычаг. Ошибки
	// открытия лога не влияют на доставку (best-effort): вывод уходит в никуда.
	logFile, logErr := openAttemptLog(logPath, p, attempt)
	if logErr == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	} else {
		log.Printf("WARN: lifecycle hook log %s: %v", logPath, logErr)
	}
	err = cmd.Run()
	if logFile != nil {
		fmt.Fprintf(logFile, "=== attempt %d finished: %v ===\n", attempt, err)
		logFile.Close()
	}
	return err
}

// openAttemptLog создаёт/дописывает лог хука и пишет заголовок попытки.
// Один воркер на хук → записи сериализованы, мьютекс не нужен.
func openAttemptLog(logPath string, p Payload, attempt int) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(f, "=== %s event=%s id=%s stage=%s attempt=%d ===\n",
		time.Now().Format(time.RFC3339), p.Event, p.EventID, stageIDOrDash(p), attempt)
	return f, nil
}

func stageIDOrDash(p Payload) string {
	if p.Stage != nil && p.Stage.ID != "" {
		return p.Stage.ID
	}
	return "-"
}
```

`runCommand` при этом упрощается (marshal уходит в execOne):

```go
func runCommand(ctx context.Context, h Hook, cfg DispatcherConfig, p Payload, logPath string) error {
	attempts := h.Retries + 1
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		lastErr = execOne(ctx, h, cfg, p, logPath, attempt)
		if lastErr == nil {
			return nil
		}
		if attempt < attempts {
			select {
			case <-time.After(hookRetryBackoff):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return lastErr
}
```

Импорты: `bytes, context, encoding/json, fmt, log, os, os/exec, path/filepath, syscall, time` (bytes — Stdin-Reader; strings/sync больше не нужны — appendHookLog/logMu удалены). Убрать `perAttemptTimeout`/`buildPayloadForEnv`/`p_` из первого эскиза — их заменяет финальный код.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/lifecyclehooks/ -run TestRunCommand -v`
Expected: PASS (4 теста).

- [ ] **Step 5: Commit**

```bash
git add pkg/lifecyclehooks/
git commit -m "feat(lifecyclehooks): runner команд — stdin/env/лог/timeout/retries/process-group kill"
```

---

### Task 5: Dispatcher (`dispatcher.go`)

**Files:**
- Create: `pkg/lifecyclehooks/dispatcher.go`
- Test: `pkg/lifecyclehooks/dispatcher_test.go`

**Interfaces:**
- Consumes: Tasks 1-4.
- Produces (используют Tasks 8-13):
  - `const FlushTimeout = 10 * time.Second`; `const hookQueueSize = 256`.
  - `type DispatcherOptions struct { Config DispatcherConfig; Hooks []RegisteredHook; LogDir string; RunFunc func(ctx context.Context, h Hook, cfg DispatcherConfig, p Payload, logPath string) error; OnError func(hookID, eventID string, err error) }` (RunFunc/OnError — seam для тестов; RunFunc nil → runCommand).
  - `func New(opts DispatcherOptions) *Dispatcher`.
  - `func (d *Dispatcher) Start()` — собственный контекст + по одному воркеру на хук.
  - `func (d *Dispatcher) Emit(ev Event)` — неблокирующий; строит event_id, payload; дроп при полной очереди → OnError.
  - `func (d *Dispatcher) Flush(timeout time.Duration) bool` — ждёт опустошения (sent == done), bounded.
  - `func (d *Dispatcher) Stop()` — закрывает очереди, cancel, ждёт воркеров.

- [ ] **Step 1: Write the failing test**

```go
package lifecyclehooks

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeRun собирает доставки; блокировка через канал gate даёт управляемый
// темп, чтобы проверить сериализацию и flush.
func newRecordingDispatcher(t *testing.T, hooks []RegisteredHook) (*Dispatcher, *recorder) {
	t.Helper()
	rec := &recorder{}
	d := New(DispatcherOptions{
		Config: DispatcherConfig{FlowName: "f", RunID: "run-1", RunDir: t.TempDir(), RootDir: t.TempDir()},
		Hooks:  hooks,
		LogDir: t.TempDir(),
		RunFunc: func(ctx context.Context, h Hook, cfg DispatcherConfig, p Payload, logPath string) error {
			rec.mu.Lock()
			rec.deliveries = append(rec.deliveries, h.ID+":"+string(p.Event)+":"+p.EventID)
			rec.mu.Unlock()
			if rec.gate != nil {
				select {
				case <-rec.gate:
				case <-ctx.Done():
				}
			}
			return rec.err
		},
		OnError: func(hookID, eventID string, err error) {
			rec.mu.Lock()
			rec.failures = append(rec.failures, hookID+"|"+eventID+"|"+err.Error())
			rec.mu.Unlock()
		},
	})
	return d, rec
}

type recorder struct {
	mu         sync.Mutex
	deliveries []string
	failures   []string
	gate       chan struct{}
	err        error
}

func (r *recorder) snapshot() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := append([]string(nil), r.deliveries...)
	b := append([]string(nil), r.failures...)
	return a, b
}

func TestDispatcher_RoutesAndBuildsEventIDs(t *testing.T) {
	global := RegisteredHook{Hook: Hook{ID: "g", Events: EventSelector{All: true}, Command: "true"}}
	scoped := RegisteredHook{Hook: Hook{ID: "s", Events: EventSelector{All: true}, Command: "true"}, StageID: "deploy"}
	d, rec := newRecordingDispatcher(t, []RegisteredHook{global, scoped})
	d.Start()
	defer d.Stop()

	d.Emit(Event{Type: EventStageFailed, StageID: "build", Seq: 7, Time: time.Now()})
	d.Emit(Event{Type: EventStageFailed, StageID: "deploy", Seq: 8, Time: time.Now()})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if dl, _ := rec.snapshot(); len(dl) == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	dl, _ := rec.snapshot()
	if len(dl) != 3 {
		t.Fatalf("deliveries: %+v", dl)
	}
	has := func(s string) bool {
		for _, d := range dl {
			if d == s {
				return true
			}
		}
		return false
	}
	if !has("g:stage_failed:run-1:transition:7:stage_failed") || !has("g:stage_failed:run-1:transition:8:stage_failed") {
		t.Fatalf("global hook ids: %+v", dl)
	}
	if !has("s:stage_failed:run-1:transition:8:stage_failed") {
		t.Fatalf("scoped hook: %+v", dl)
	}
}

func TestDispatcher_FlowEventID(t *testing.T) {
	d, rec := newRecordingDispatcher(t, []RegisteredHook{{Hook: Hook{ID: "g", Events: EventSelector{All: true}, Command: "true"}}})
	d.Start()
	defer d.Stop()
	d.Emit(Event{Type: EventFlowFinished, Time: time.Now()})
	waitFor(t, rec, 1)
	dl, _ := rec.snapshot()
	if len(dl) != 1 || dl[0] != "g:flow_finished:run-1:flow:flow_finished" {
		t.Fatalf("flow id: %+v", dl)
	}
}

func TestDispatcher_NonFSMStageEventID(t *testing.T) {
	d, rec := newRecordingDispatcher(t, []RegisteredHook{{Hook: Hook{ID: "g", Events: EventSelector{Events: []EventType{EventStageScriptStarted}}, Command: "true"}}})
	d.Start()
	defer d.Stop()
	d.Emit(Event{Type: EventStageScriptStarted, StageID: "deploy", Time: time.Now()})
	waitFor(t, rec, 1)
	dl, _ := rec.snapshot()
	if len(dl) != 1 || dl[0] != "g:stage_script_started:run-1:deploy:stage_script_started" {
		t.Fatalf("non-fsm id: %+v", dl)
	}
}

func TestDispatcher_SequentialPerHook(t *testing.T) {
	d, rec := newRecordingDispatcher(t, []RegisteredHook{{Hook: Hook{ID: "g", Events: EventSelector{All: true}, Command: "true"}}})
	rec.gate = make(chan struct{})
	d.Start()
	defer d.Stop()

	d.Emit(Event{Type: EventFlowStarted, Time: time.Now()})
	d.Emit(Event{Type: EventFlowFinished, Time: time.Now()})
	time.Sleep(100 * time.Millisecond) // обе должны стоять в очереди, NONE выполняется
	if dl, _ := rec.snapshot(); len(dl) != 1 {
		t.Fatalf("worker must process strictly sequentially, got %+v", dl)
	}
	close(rec.gate)
	if ok := d.Flush(2 * time.Second); !ok {
		t.Fatal("flush timeout")
	}
	if dl, _ := rec.snapshot(); len(dl) != 2 {
		t.Fatalf("after flush: %+v", dl)
	}
}

func TestDispatcher_FinalFailureReportsOnError(t *testing.T) {
	d, rec := newRecordingDispatcher(t, []RegisteredHook{{Hook: Hook{ID: "g", Events: EventSelector{All: true}, Command: "true"}}})
	rec.err = errors.New("boom")
	d.Start()
	defer d.Stop()
	d.Emit(Event{Type: EventFlowStarted, Time: time.Now()})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, fl := rec.snapshot(); len(fl) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, fl := rec.snapshot()
	if len(fl) != 1 || fl[0] != "g|run-1:flow:flow_started|boom" {
		t.Fatalf("failures: %+v", fl)
	}
}

func TestDispatcher_QueueOverflowDropsNotBlocks(t *testing.T) {
	// один хук с заблокированным раннером + очередь 256: эмиттер не блокируется
	d, rec := newRecordingDispatcher(t, []RegisteredHook{{Hook: Hook{ID: "g", Events: EventSelector{All: true}, Command: "true"}}})
	rec.gate = make(chan struct{})
	d.Start()
	defer d.Stop()
	for i := 0; i < hookQueueSize+50; i++ {
		start := time.Now()
		d.Emit(Event{Type: EventFlowStarted, Time: time.Now()})
		if time.Since(start) > 500*time.Millisecond {
			t.Fatalf("Emit blocked at %d", i)
		}
	}
	_, fl := rec.snapshot()
	if len(fl) == 0 {
		t.Fatal("overflow must be reported via OnError")
	}
	close(rec.gate)
}

func TestDispatcher_ConcurrentEmitStopNoPanic(t *testing.T) {
	// codex CRIT#2: поздние эмиттеры (poller, agent-горутины) против Stop.
	// Гоняется вместе с -race.
	d, _ := newRecordingDispatcher(t, []RegisteredHook{{Hook: Hook{ID: "g", Events: EventSelector{All: true}, Command: "true"}}})
	d.Start()
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					d.Emit(Event{Type: EventFlowStarted, Time: time.Now()})
				}
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	d.Stop()  // на гонящихся эмиттерах
	d.Stop()  // идемпотентность
	close(done)
	wg.Wait()
}

func waitFor(t *testing.T, rec *recorder, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if dl, _ := rec.snapshot(); len(dl) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/lifecyclehooks/ -run TestDispatcher -v`
Expected: FAIL (compile error).

- [ ] **Step 3: Write minimal implementation (`dispatcher.go`)**

```go
package lifecyclehooks

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// FlushTimeout — bounded-ожидание опустошения очередей при завершении рана.
const FlushTimeout = 10 * time.Second

// hookQueueSize — буфер очереди одного хука. Живой хук, не успевающий за
// потоком событий, начинает терять доставки (best-effort контракт Phase 1).
const hookQueueSize = 256

// DispatcherOptions собирают dispatcher. RunFunc/OnError — seams: тесты
// подменяют RunFunc фейком; OnError получает финальные сбои доставки и
// переполнения очереди (для dashboard-warning).
type DispatcherOptions struct {
	Config  DispatcherConfig
	Hooks   []RegisteredHook
	LogDir  string
	RunFunc func(ctx context.Context, h Hook, cfg DispatcherConfig, p Payload, logPath string) error
	OnError func(hookID, eventID string, err error)
}

type delivery struct {
	eventID string
	payload Payload
}

// Dispatcher路由ит события хукам: по одному воркеру на хук (строго
// последовательные доставки для одного потребителя), параллельно между
// разными хуками. Собственный контекст, независимый от run-ctx: хуки —
// наблюдатели, событие flow_interrupted должно уйти даже после отмены рана.
type Dispatcher struct {
	cfg     DispatcherConfig
	hooks   []RegisteredHook
	logDir  string
	runFn   func(ctx context.Context, h Hook, cfg DispatcherConfig, p Payload, logPath string) error
	onError func(hookID, eventID string, err error)

	ctx    context.Context
	cancel context.CancelFunc
	queues []chan delivery
	wg     sync.WaitGroup
	sent   atomic.Int64
	done   atomic.Int64

	// mu/stopped закрывают гонку Emit-vs-Stop (codex CRIT#2): Emit шлёт под
	// RLock, Stop закрывает каналы под Lock — пересечение невозможно; Emit
	// после Stop — тихий no-op. Поздние эмиттеры (question poller, agent
	// goroutine, пережившие возврат Run) не паникуют на closed channel.
	mu      sync.RWMutex
	stopped bool
}

// New создаёт dispatcher (воркеры ещё не запущены — см. Start).
func New(opts DispatcherOptions) *Dispatcher {
	runFn := opts.RunFunc
	if runFn == nil {
		runFn = runCommand
	}
	return &Dispatcher{
		cfg:     opts.Config,
		hooks:   opts.Hooks,
		logDir:  opts.LogDir,
		runFn:   runFn,
		onError: opts.OnError,
	}
}

// Start запускает воркеров. Вызывается один раз.
func (d *Dispatcher) Start() {
	d.ctx, d.cancel = context.WithCancel(context.Background())
	d.queues = make([]chan delivery, len(d.hooks))
	for i, rh := range d.hooks {
		d.queues[i] = make(chan delivery, hookQueueSize)
		d.wg.Add(1)
		go d.worker(d.ctx, rh, d.queues[i])
	}
}

// Emit рассылает событие подходящим хукам. Неблокирующий: полная очередь
// одного хука не задерживает эмиттера (оркестратор) — доставка дропается
// с отчётом в OnError.
func (d *Dispatcher) Emit(ev Event) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.stopped || len(d.hooks) == 0 {
		return
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	eventID := d.eventID(ev)
	payload := BuildPayload(d.cfg, ev, eventID)
	for i, rh := range d.hooks {
		if !rh.MatchesEvent(ev) {
			continue
		}
		dl := delivery{eventID: eventID, payload: payload}
		select {
		case d.queues[i] <- dl:
			d.sent.Add(1)
		default:
			d.reportError(rh.Hook.ID, eventID, fmt.Errorf("hook queue overflow (%d pending), delivery dropped", hookQueueSize))
		}
	}
}

// eventID — стабильный идентификатор по спеке: FSM-переходы несут durable
// seq, flow-события уникальны в пределах рана, прочие stage-события —
// best-effort (повтор при retry стадии допустим).
func (d *Dispatcher) eventID(ev Event) string {
	switch {
	case ev.Seq > 0:
		return fmt.Sprintf("%s:transition:%d:%s", d.cfg.RunID, ev.Seq, ev.Type)
	case ev.StageID == "":
		return fmt.Sprintf("%s:flow:%s", d.cfg.RunID, ev.Type)
	default:
		return fmt.Sprintf("%s:%s:%s", d.cfg.RunID, ev.StageID, ev.Type)
	}
}

func (d *Dispatcher) worker(ctx context.Context, rh RegisteredHook, q <-chan delivery) {
	defer d.wg.Done()
	for dl := range q {
		logPath := filepath.Join(d.logDir, rh.Hook.ID+".log")
		if err := d.runFn(ctx, rh.Hook, d.cfg, dl.payload, logPath); err != nil && ctx.Err() == nil {
			d.reportError(rh.Hook.ID, dl.eventID, err)
		}
		d.done.Add(1)
	}
}

// Flush ждёт, пока все поставленные в очередь доставки завершатся, не дольше
// timeout. true — очереди пусты.
func (d *Dispatcher) Flush(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for d.sent.Load() != d.done.Load() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

// Stop закрывает очереди и воркеров; идемпотентен (повторный вызов — no-op).
func (d *Dispatcher) Stop() {
	d.mu.Lock()
	if d.stopped || d.cancel == nil {
		d.mu.Unlock()
		return
	}
	d.stopped = true
	for _, q := range d.queues {
		close(q)
	}
	cancel := d.cancel
	d.mu.Unlock()
	cancel()
	d.wg.Wait()
}

func (d *Dispatcher) reportError(hookID, eventID string, err error) {
	if d.onError == nil {
		return
	}
	d.onError(hookID, eventID, err)
}
```

Примечание: `worker` помечает done даже при ошибке — Flush не зависает на
повторно падающем хуке; отчёт об ошибке уже ушёл в OnError.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/lifecyclehooks/ -race -v`
Expected: PASS (все тесты пакета, включая -race).

- [ ] **Step 5: Commit**

```bash
git add pkg/lifecyclehooks/
git commit -m "feat(lifecyclehooks): dispatcher с per-hook очередями, event_id и bounded flush"
```

---

### Task 6: Config-слой (`pkg/config`)

**Files:**
- Modify: `pkg/config/config.go` (Config struct :331-350, LoadFrom :406-421, mergeFile :464-563)
- Modify: `schema/config.schema.json`
- Test: `pkg/config/config_test.go` (добавить тесты)

**Interfaces:**
- Consumes: Tasks 1-2 (`lifecyclehooks.Hook`, `ValidateLayer`).
- Produces: `Config.Hooks []lifecyclehooks.Hook` (yaml `hooks`) — глобальные и проектные хуки, смёрженные keyed-merge по id (project заменяет global по id, остальные складываются).

- [ ] **Step 1: Write the failing test** (добавить в `pkg/config/config_test.go`, рядом с `TestAutoRecoverMerge_ProjectDisablesGlobalDefault`)

```go
func TestHooksMerge_KeyedByID(t *testing.T) {
	dir := t.TempDir()
	global := "hooks:\n  - id: a\n    events: all\n    command: /bin/ga\n  - id: b\n    events: [flow_started]\n    command: /bin/gb\n"
	project := "hooks:\n  - id: b\n    events: [flow_failed]\n    command: /bin/pb\n  - id: c\n    events: all\n    command: /bin/pc\n"
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "project"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "global", "config.yaml"), []byte(global), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "project", "config.yaml"), []byte(project), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFrom(filepath.Join(dir, "global"), filepath.Join(dir, "project"))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if len(cfg.Hooks) != 3 {
		t.Fatalf("want 3 hooks, got %+v", cfg.Hooks)
	}
	byID := map[string]lifecyclehooks.Hook{}
	for _, h := range cfg.Hooks {
		byID[h.ID] = h
	}
	if byID["b"].Command != "/bin/pb" || byID["b"].Events.All {
		t.Fatalf("project must override global by id: %+v", byID["b"])
	}
	if byID["a"].Command != "/bin/ga" || byID["c"].Command != "/bin/pc" {
		t.Fatalf("other ids must survive: %+v", byID)
	}
}

func TestHooksMerge_ProjectOnly(t *testing.T) {
	dir := t.TempDir()
	proj := "hooks:\n  - id: solo\n    events: all\n    command: /bin/x\n"
	if err := os.MkdirAll(filepath.Join(dir, "g"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p", "config.yaml"), []byte(proj), 0o644); err != nil {
		t.Fatal(err) // LoadFrom сам создаёт? нет — нужна директория
	}
	cfg, err := LoadFrom(filepath.Join(dir, "g"), filepath.Join(dir, "p"))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if len(cfg.Hooks) != 1 || cfg.Hooks[0].ID != "solo" {
		t.Fatalf("got %+v", cfg.Hooks)
	}
}

func TestHooksMerge_InvalidEventFails(t *testing.T) {
	dir := t.TempDir()
	proj := "hooks:\n  - id: bad\n    events: [stage_strted]\n    command: /bin/x\n"
	if err := os.MkdirAll(filepath.Join(dir, "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "g"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p", "config.yaml"), []byte(proj), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFrom(filepath.Join(dir, "g"), filepath.Join(dir, "p")); err == nil {
		t.Fatal("unknown event name must fail config load")
	}
}
```

Во втором тесте поправить: `os.MkdirAll(filepath.Join(dir, "p"), 0o755)` ДО WriteFile (в эскизе порядок нарушен — привести как здесь).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/config/ -run TestHooksMerge -v`
Expected: FAIL (compile error — поля Hooks нет).

- [ ] **Step 3: Write minimal implementation**

`config.go` — поле (после `Accounting`):

```go
	// Hooks — lifecycle-хуки глобального и проектного слоёв (Phase 1).
	// mergeFile мёржит их keyed по id: проектный слой заменяет одноимённый
	// глобальный хук, остальные складываются. См. pkg/lifecyclehooks.
	Hooks []lifecyclehooks.Hook `yaml:"hooks,omitempty"`
```

Импорт: `"github.com/akopichin/afm/pkg/lifecyclehooks"` (проверить фактический module path — `github.com/akopichin/afm/...` по go.mod; pkg/lifecyclehooks его не импортирует → цикла нет).

`mergeFile` — блок перед `return nil` (после Pricing.Channels). ВАЖНО (codex MAJ#4): валидация слоя overlay ДО keyed-merge — иначе дубль id внутри одного файла молча «схлопнется» заменой до того, как ValidateLayer в LoadFrom его увидит:

```go
	if overlay.Hooks != nil {
		if err := lifecyclehooks.ValidateLayer(overlay.Hooks, false); err != nil {
			return fmt.Errorf("hooks: %w", err)
		}
		if dst.Hooks == nil {
			dst.Hooks = make([]lifecyclehooks.Hook, 0, len(overlay.Hooks))
		}
		for _, h := range overlay.Hooks {
			replaced := false
			for i := range dst.Hooks {
				if dst.Hooks[i].ID == h.ID {
					dst.Hooks[i] = h // более специфичный слой заменяет по id
					replaced = true
					break
				}
			}
			if !replaced {
				dst.Hooks = append(dst.Hooks, h)
			}
		}
	}
```

`LoadFrom` — после `validatePricing` (итоговая проверка смёрженного списка; ошибка mergeFile уже перехватила внутрислойные проблемы глобального и проектного файлов по отдельности):

```go
	if err := lifecyclehooks.ValidateLayer(cfg.Hooks, false); err != nil {
		return cfg, fmt.Errorf("hooks: %w", err)
	}
```

`schema/config.schema.json` — добавить в properties корня (рядом с `accounting`/`pricing`, в стиле файла):

```json
"hooks": {
  "type": "array",
  "description": "Lifecycle hooks: observer commands receiving event JSON on stdin (global/project layers)",
  "items": { "$ref": "#/definitions/lifecycleHook" }
}
```

и в definitions:

```json
"lifecycleHook": {
  "type": "object",
  "required": ["id", "events", "command"],
  "additionalProperties": false,
  "properties": {
    "id":       { "type": "string", "minLength": 1 },
    "events":   { "$ref": "#/definitions/lifecycleEventSelector" },
    "skip_events": { "type": "array", "items": { "type": "string" } },
    "command":  { "type": "string", "minLength": 1 },
    "timeout":  { "type": "string" },
    "retries":  { "type": "integer", "minimum": 0 }
  }
},
"lifecycleEventSelector": {
  "oneOf": [
    { "const": "all" },
    { "type": "array", "items": { "type": "string" }, "minItems": 1 }
  ]
}
```

(Свериться с фактической структурой файла: если additionalProperties у корня запрещён — поле обязательно добавить в список properties; имена enum-значений событий НЕ перечислять в схеме — валидацию имён делает ValidateLayer, а не схема.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/config/ ./pkg/schemacheck/ -v`
Expected: PASS (включая schemacheck guard).

- [ ] **Step 5: Commit**

```bash
git add pkg/config/ schema/config.schema.json
git commit -m "feat(config): слой hooks с keyed-merge по id и валидацией"
```

---

### Task 7: Flow-слой (`pkg/flow`)

**Files:**
- Modify: `pkg/flow/flow.go` (Flow :313-330, Stage :135-203, Flow.validate :492-670)
- Modify: `schema/flow.schema.json`
- Test: `pkg/flow/flow_test.go` (добавить)

**Interfaces:**
- Consumes: Tasks 1-2.
- Produces: `Flow.Hooks []lifecyclehooks.Hook` (yaml `hooks,omitempty`), `Stage.Hooks []lifecyclehooks.Hook` (yaml `hooks,omitempty`) — валидируются на этапе ParseFile: unknown event / dup id в слое / flow-события в stage hook.

- [ ] **Step 1: Write the failing test**

```go
func TestParseHooks_FlowLevel(t *testing.T) {
	f := writeFlow(t, `
name: h
hooks:
  - id: notify
    events: [flow_started, stage_failed]
    command: ./notify.sh
stages:
  - id: s1
    script: "true"
`)
	parsed, err := ParseFile(f)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(parsed.Hooks) != 1 || parsed.Hooks[0].ID != "notify" {
		t.Fatalf("got %+v", parsed.Hooks)
	}
}

func TestParseHooks_StageLevel(t *testing.T) {
	f := writeFlow(t, `
name: h
stages:
  - id: s1
    script: "true"
    hooks:
      - id: dep
        events: all
        skip_events: [stage_question_answered]
        command: ./dep.sh
        timeout: 15s
        retries: 1
`)
	parsed, err := ParseFile(f)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(parsed.Stages[0].Hooks) != 1 {
		t.Fatalf("got %+v", parsed.Stages[0].Hooks)
	}
	h := parsed.Stages[0].Hooks[0]
	if h.Timeout != 15*time.Second || h.Retries != 1 || len(h.SkipEvents) != 1 {
		t.Fatalf("hook fields: %+v", h)
	}
}

func TestParseHooks_Errors(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{"unknown event", `
name: h
hooks: [{id: x, events: [stage_strted], command: "true"}]
stages: [{id: s1, script: "true"}]
`},
		{"dup id flow layer", `
name: h
hooks: [{id: x, events: all, command: "true"}, {id: x, events: all, command: "true"}]
stages: [{id: s1, script: "true"}]
`},
		{"flow event in stage hook", `
name: h
stages: [{id: s1, script: "true", hooks: [{id: x, events: [flow_finished], command: "true"}]}]
`},
		{"empty command", `
name: h
hooks: [{id: x, events: all, command: ""}]
stages: [{id: s1, script: "true"}]
`},
		{"dup id stage layer", `
name: h
stages: [{id: s1, script: "true", hooks: [{id: x, events: all, command: "a"}, {id: x, events: all, command: "b"}]}]
`},
		{"dup id across stages", `
name: h
stages: [{id: s1, script: "true", hooks: [{id: x, events: all, command: "a"}]}, {id: s2, script: "true", hooks: [{id: x, events: all, command: "b"}]}]
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := writeFlow(t, tc.yaml)
			if _, err := ParseFile(f); err == nil {
				t.Fatal("want parse error, got nil")
			}
		})
	}
}

func TestParseHooks_DifferentIDsAcrossLayersOK(t *testing.T) {
	f := writeFlow(t, `
name: h
hooks: [{id: flowhook, events: all, command: "true"}]
stages: [{id: s1, script: "true", hooks: [{id: stagehook, events: all, command: "true"}]}]
`)
	if _, err := ParseFile(f); err != nil {
		t.Fatalf("different ids must coexist: %v", err)
	}
}
```

`writeFlow` — если такого хелпера ещё нет в flow_test.go, добавить:

```go
func writeFlow(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "flow.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}
```

(Сначала grep `func writeFlow` в пакете — возможно, уже есть под другим именем; тогда переиспользовать существующий.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/flow/ -run TestParseHooks -v`
Expected: FAIL (compile error — полей нет).

- [ ] **Step 3: Write minimal implementation**

`Flow` — поле (после Memory, перед Stages):

```go
	// Hooks — lifecycle-хуки флоу-слоя: получают события всех стадий.
	Hooks []lifecyclehooks.Hook `yaml:"hooks,omitempty"`
```

`Stage` — поле (после Buttons или Reflect, соблюдая стиль соседних):

```go
	// Hooks — lifecycle-хуки, ограниченные этой стадией (flow-события в них
	// запрещены валидацией).
	Hooks []lifecyclehooks.Hook `yaml:"hooks,omitempty"`
```

`Flow.validate()` — после блока reflect (перед закрывающей скобкой validate). Блок про cross-stage дубли (codex MAJ#3, ruling): плоская замена по id между СЛОЯМИ — специфицированное поведение (global < project < flow < stage, спека «Наследование и порядок»), но один и тот же id в хуках РАЗНЫХ стадий не должен молча схлопываться в Combine (выиграла бы последняя стадия в YAML) — это отдельная ошибка валидации:

```go
	if err := lifecyclehooks.ValidateLayer(f.Hooks, false); err != nil {
		return fmt.Errorf("hooks: %w", err)
	}
	stageHookIDs := map[string]string{} // hook id -> stage id (первый объявивший)
	for _, s := range f.Stages {
		if err := lifecyclehooks.ValidateLayer(s.Hooks, true); err != nil {
			return fmt.Errorf("stage %q: %w", s.ID, err)
		}
		for _, h := range s.Hooks {
			if other, dup := stageHookIDs[h.ID]; dup {
				return fmt.Errorf("stage %q: hooks: duplicate hook id %q across stages (already used by stage %q)", s.ID, h.ID, other)
			}
			stageHookIDs[h.ID] = s.ID
		}
	}
```

`schema/flow.schema.json` — `hooks` в properties корня и в properties стадии, items — тот же `$ref: "#/definitions/lifecycleHook"` + определение lifecycleHook/lifecycleEventSelector (скопировать из Task 6; если схемы шарят определения через общий файл — свериться с device схемы: `additionalProperties` у Stage/Flow).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/flow/ ./pkg/schemacheck/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/flow/ schema/flow.schema.json
git commit -m "feat(flow): hooks на уровне флоу и стадии с валидацией ParseFile"
```

---

### Task 8: Orchestrator — Options, emit-хелперы, интеграция triggerWithSeq

**Files:**
- Create: `pkg/orchestrator/lifecycle_emit.go`
- Modify: `pkg/orchestrator/orchestrator.go` (Options :57-94, структура Orchestrator :113+, New :394, triggerWithSeq :489-524)
- Modify: `pkg/orchestrator/bus/fsm.go` (FSM.Apply — возвращает `from`)
- Test: `pkg/orchestrator/lifecycle_emit_test.go`, `pkg/orchestrator/bus/fsm_test.go` (сигнатура Apply)

**Interfaces:**
- Consumes: Tasks 1-5 (`lifecyclehooks.Dispatcher`, `Event`, константы).
- Produces (используют Tasks 9-13):
  - `Options.FlowName string`, `Options.RunID string`, `Options.Resumed bool`, `Options.Hooks *lifecyclehooks.Dispatcher`.
  - Поле `Orchestrator.hooks *lifecyclehooks.Dispatcher`.
  - `func (o *Orchestrator) emitLifecycle(ev lifecyclehooks.Event)` — nil-safe.
  - `func (o *Orchestrator) emitLifecycleTransition(stageID string, from, to state.StageStatus, seq uint64, ev bus.FSMEvent, reason string)`.
  - `func (o *Orchestrator) emitStageEvent(ev lifecyclehooks.EventType, stageID, reason string)` — для не-FSM границ.
  - `func (o *Orchestrator) stageName(id string) string`.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"testing"

	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// captureDispatcher собирает события без реальных команд.
type captureDispatcher struct {
	events chan lifecyclehooks.Event
}

func newCaptureDispatcher() *captureDispatcher {
	c := &captureDispatcher{events: make(chan lifecyclehooks.Event, 64)}
	return c
}

func (c *captureDispatcher) asOptions() *lifecyclehooks.Dispatcher {
	// Настоящий Dispatcher с фейковым RunFunc: переиспользуем его матчинг и
	// event_id, не запуская процессы.
	return nil // заменяется в тесте прямым присваиванием поля o.hooks
}

func TestEmitLifecycleTransition_MapsFSMEvent(t *testing.T) {
	o := newTestOrchestrator(t) // см. хелпер ниже
	capture := newCaptureDispatcher()
	o.hooks = captureDispatcherAsReal(t, capture)

	o.emitLifecycleTransition("s1", state.StatusRunning, state.StatusFailed, 42, bus.EvFail, "agent exited 1")

	select {
	case ev := <-capture.events:
		if ev.Type != lifecyclehooks.EventStageFailed {
			t.Fatalf("type: %v", ev.Type)
		}
		if ev.Seq != 42 || ev.From != "running" || ev.To != "failed" || ev.Reason != "agent exited 1" {
			t.Fatalf("event: %+v", ev)
		}
		if ev.StageID != "s1" || ev.StageName != "" {
			t.Fatalf("stage: %+v", ev)
		}
	default:
		// dispatcher асинхронен: подождать через Flush реального dispatcher
	}
}

func TestEmitLifecycleTransition_UnmappedFSMEventIgnored(t *testing.T) {
	o := newTestOrchestrator(t)
	capture := newCaptureDispatcher()
	o.hooks = captureDispatcherAsReal(t, capture)

	o.emitLifecycleTransition("s1", state.StatusPending, state.StatusReady, 1, bus.EvReady, "")

	if len(capture.events) != 0 {
		t.Fatalf("EvReady must not produce a lifecycle event: %+v", <-capture.events)
	}
}

func TestEmitLifecycle_NilSafe(t *testing.T) {
	o := newTestOrchestrator(t)
	o.hooks = nil // хуки не настроены — обычные тесты orchestrator
	o.emitLifecycle(lifecyclehooks.Event{Type: lifecyclehooks.EventFlowStarted})
	o.emitLifecycleTransition("s1", state.StatusRunning, state.StatusFailed, 1, bus.EvFail, "x")
	o.emitStageEvent(lifecyclehooks.EventStageScriptStarted, "s1", "")
	// не паникует — тест проходит
}
```

Хелперы теста (в том же файле): `newTestOrchestrator` — построить минимальный orchestrator как это делают соседние тесты пакета (grep `orchestrator.New(` по `*_test.go`, взять самый короткий setup: state.NewRunState + state.Open в t.TempDir + graph.New + хотя бы одна стадия). `captureDispatcherAsReal` — реальный `lifecyclehooks.New(DispatcherOptions{Hooks: [{ID:"cap", Events: All, Command:"true"}], RunFunc: записывает payload-событие в capture.events, OnError: t.Logf})` + `Start()`, `t.Cleanup(Stop)`. Проверка в первом тесте — не `select/default`, а: эмит → `d.Flush(2s)` → drained из канала (упрощение ниже в шаге 3 кода теста привести к этому виду: после emit вызвать Flush и затем читать канал без default).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run TestEmitLifecycle -v`
Expected: FAIL (compile error).

- [ ] **Step 3: Write minimal implementation**

`lifecycle_emit.go`:

```go
package orchestrator

import (
	"time"

	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// fsmLifecycleEvents мапит FSM-переходы на публичные lifecycle-события.
// EvReady/EvBlockedByDep — внутренняя планировка (pending→ready и т.п.), им
// нет публичного аналога; EvHookFailed/EvHookResolved относятся к
// script_before/after, чьи события эмитятся на своих границах (Task 10) —
// здесь их нет, чтобы не задваивать.
var fsmLifecycleEvents = map[bus.FSMEvent]lifecyclehooks.EventType{
	bus.EvStartPlanning:    lifecyclehooks.EventStagePlanningStarted,
	bus.EvPlanReady:        lifecyclehooks.EventStagePlanReady,
	bus.EvApprove:          lifecyclehooks.EventStageApproved,
	bus.EvRevise:           lifecyclehooks.EventStageRevisionStarted,
	bus.EvStartRun:         lifecyclehooks.EventStageExecutionStarted,
	bus.EvAskUser:          lifecyclehooks.EventStageQuestionAsked,
	bus.EvUserAnswered:     lifecyclehooks.EventStageQuestionAnswered,
	bus.EvScheduleRetry:    lifecyclehooks.EventStageRetryScheduled,
	bus.EvResumeAfterRetry: lifecyclehooks.EventStageRetryStarted,
	bus.EvManualRetry:      lifecyclehooks.EventStageRetryStarted,
	bus.EvPause:            lifecyclehooks.EventStagePaused,
	bus.EvContinue:         lifecyclehooks.EventStageResumed,
	bus.EvComplete:         lifecyclehooks.EventStageFinished,
	bus.EvFail:             lifecyclehooks.EventStageFailed,
}

// emitLifecycle — точка отправки; nil-safe (хуки не настроены — обычный путь
// тестов и ранов без hooks).
func (o *Orchestrator) emitLifecycle(ev lifecyclehooks.Event) {
	if o.hooks == nil {
		return
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	o.hooks.Emit(ev)
}

// emitLifecycleTransition вызывается из triggerWithSeq ТОЛЬКО для успешно
// применённых переходов (seq > 0) — не применённый CAS событие не порождает.
func (o *Orchestrator) emitLifecycleTransition(stageID string, from, to state.StageStatus, seq uint64, ev bus.FSMEvent, reason string) {
	le, ok := fsmLifecycleEvents[ev]
	if !ok {
		return
	}
	o.emitLifecycle(lifecyclehooks.Event{
		Type:      le,
		StageID:   stageID,
		StageName: o.stageName(stageID),
		From:      string(from),
		To:        string(to),
		Phase:     phaseForStatus(from),
		Reason:    reason,
		Seq:       seq,
	})
}

// emitStageEvent — не-FSM границы (script-стадии, script_before/after,
// авто-ответы): без seq, event_id строит dispatcher.
func (o *Orchestrator) emitStageEvent(ev lifecyclehooks.EventType, stageID, reason string) {
	o.emitLifecycle(lifecyclehooks.Event{
		Type:      ev,
		StageID:   stageID,
		StageName: o.stageName(stageID),
		Reason:    reason,
	})
}

// stageName — отображаемое имя стадии (Name, иначе ID).
func (o *Orchestrator) stageName(id string) string {
	if s := o.graph.Stage(id); s != nil {
		if s.Name != "" {
			return s.Name
		}
		return s.ID
	}
	return ""
}

// phaseForStatus — грубая фаза по исходному статусу перехода: planning/
// revising → planning, running/retrying → implementation, остальное "".
func phaseForStatus(st state.StageStatus) string {
	switch st {
	case state.StatusPlanning, state.StatusRevising:
		return "planning"
	case state.StatusRunning, state.StatusRetrying:
		return "implementation"
	}
	return ""
}
```

`orchestrator.go`:
- Options — поля (после `Accounting`):

```go
	// Lifecycle hooks (Phase 1): FlowName/RunID/Resumed питают payload,
	// Hooks — собранный dispatcher (nil = хуков нет). Сборка — cmd/afm/run.go.
	FlowName string
	RunID    string
	Resumed  bool
	Hooks    *lifecyclehooks.Dispatcher
```

- Структура Orchestrator — поле (рядом с `mem`):

```go
	// hooks — lifecycle dispatcher (observer-only; nil-safe).
	hooks *lifecyclehooks.Dispatcher
```

- `New` — после `o := &Orchestrator{...}`: `o.hooks = opts.Hooks`.
- `pkg/orchestrator/bus/fsm.go` — `FSM.Apply` получает дополнительное возвращаемое значение `from` (codex MAJ#5: отдельное чтение `o.currentStatus` ДО Apply — TOCTOU-гонка; durable-переход пишет фактический from, lifecycle-payload обязан видеть тот же). У Apply ровно один продакшн-call site (orchestrator.go:490), смена сигнатуры безопасна:

```go
// было: func (f *FSM) Apply(stageID string, ev FSMEvent, ctx GuardCtx, reason string) (to state.StageStatus, seq uint64, ok bool, err error)
// стало:
func (f *FSM) Apply(stageID string, ev FSMEvent, ctx GuardCtx, reason string) (from, to state.StageStatus, seq uint64, ok bool, err error)
```

внутри Apply: `from` — статус, прочитанный для CAS-проверки ruleAllowsFrom (уже существует локально как current); возвращать его во всех ветках (при err/not applied — `from = current, to = current, seq = 0`). Обновить единственный call site и тесты bus, если они зовут Apply напрямую.

- `triggerWithSeq` — вызов меняется на:

```go
	from, to, seq, ok, err := o.fsm.Apply(stageID, ev, ctx, reason)
```

и внутри `if ok {` (после `o.critical.TryPublish(pubEv)`):

```go
		o.emitLifecycleTransition(stageID, from, to, seq, ev, reason)
```

Импорт `lifecyclehooks` в orchestrator.go.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/ -run TestEmitLifecycle -v && go test ./pkg/orchestrator/ -count=1 .`
Expected: PASS (новые + все существующие).

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/
git commit -m "feat(orchestrator): emit-хелперы lifecycle и интеграция triggerWithSeq"
```

---

### Task 9: Flow-события в `Run` + bounded flush

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go` (Run :530-598)
- Test: `pkg/orchestrator/lifecycle_flow_test.go`

**Interfaces:**
- Consumes: Task 8 (emitLifecycle, o.hooks).
- Produces: `func (o *Orchestrator) setTerminalFlow(ev lifecyclehooks.EventType)` + `func (o *Orchestrator) finalizeLifecycle()`; поле `Orchestrator.terminalFlow`. Порядок shutdown (codex MAJ#7): terminal-событие эмитится и флашится ПОСЛЕ `cancel()`+`WaitAgents()` (defer, зарегистрированный ПЕРВЫМ — исполняется ПОСЛЕДНИМ), а не инлайн в выходах Run. Классификация выхода (codex MAJ#8): fatal ИЛИ есть Failed-стадия → `flow_failed`; всё Done → `flow_finished`; прочая отмена ctx → `flow_interrupted`.

- [ ] **Step 1: Write the failing test**

Модель теста — существующие интеграционные тесты пакета (`runOrchestratorAsync` из testrun_helper_test.go + script-стадия). Хелпер диспетчера — как в Task 8 (реальный `lifecyclehooks.New` с записывающим RunFunc).

```go
package orchestrator

import (
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/lifecyclehooks"
)

func TestRun_FlowEvents_Finished(t *testing.T) {
	// flow: одна script-стадия "true"; хук на all. Ожидаем в порядке
	// появления как минимум flow_started, stage_script_started,
	// stage_script_finished, stage_finished (FSM), flow_finished.
	rec := newFlowRecorder(t)
	o := newScriptFlowOrch(t, rec)

	runOrchestratorAsync(t, o)
	waitForRunDone(t, o, 10*time.Second)

	evs := rec.ordered()
	assertContains(t, evs, lifecyclehooks.EventFlowStarted)
	assertContains(t, evs, lifecyclehooks.EventFlowFinished)
	assertContains(t, evs, lifecyclehooks.EventStageScriptStarted)
	assertContains(t, evs, lifecyclehooks.EventStageScriptFinished)
	assertContains(t, lifecyclehooks.EventStageFinished)
	assertOrder(t, evs, lifecyclehooks.EventFlowStarted, lifecyclehooks.EventFlowFinished)
	assertOrder(t, evs, lifecyclehooks.EventStageFinished, lifecyclehooks.EventFlowFinished)
}

func TestRun_FlowEvents_Resumed(t *testing.T) {
	// Тот же setup, но Options.Resumed = true → flow_resumed вместо flow_started.
	rec := newFlowRecorder(t)
	o := newScriptFlowOrch(t, rec)
	o.opts.Resumed = true // поле opts доступно внутри пакета

	runOrchestratorAsync(t, o)
	waitForRunDone(t, o, 10*time.Second)

	evs := rec.ordered()
	assertContains(t, evs, lifecyclehooks.EventFlowResumed)
	for _, e := range evs {
		if e == lifecyclehooks.EventFlowStarted {
			t.Fatal("resumed run must not emit flow_started")
		}
	}
}

func TestRun_FlowEvents_Interrupted(t *testing.T) {
	// Стадия-скрипт "sleep 30": cancel контекста до завершения →
	// flow_interrupted, без flow_finished.
	rec := newFlowRecorder(t)
	o := newScriptFlowOrch(t, rec, stageScript("sleep 30"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- o.Run(ctx) }()
	time.Sleep(500 * time.Millisecond)
	cancel()
	<-done

	evs := rec.ordered()
	assertContains(t, evs, lifecyclehooks.EventFlowInterrupted)
	for _, e := range evs {
		if e == lifecyclehooks.EventFlowFinished {
			t.Fatal("interrupted run must not emit flow_finished")
		}
	}
}

func TestRun_FlowEvents_Failed(t *testing.T) {
	// Стадия-скрипт "exit 1" → стадия failed, Run завершается (headless без
	// дашборда выходит по terminal) → flow_failed.
	rec := newFlowRecorder(t)
	o := newScriptFlowOrch(t, rec, stageScript("exit 1"))

	runOrchestratorAsync(t, o)
	waitForRunDone(t, o, 10*time.Second)

	evs := rec.ordered()
	assertContains(t, evs, lifecyclehooks.EventFlowFailed)
	assertContains(t, evs, lifecyclehooks.EventStageFailed)
}
```

Хелперы `newFlowRecorder` (RunFunc пишет `payload.Event` в слайс под мьютексом; метод `ordered()` возвращает копию), `newScriptFlowOrch` (flow-файл во временном каталоге через `flow.ParseFile`: одна стадия `script: <body>`; `state.NewRunState`+`Open`; `orchestrator.New` c `Hooks: rec.dispatcher()`; `rec.dispatcher()` делает `New`+`Start`, `t.Cleanup(Stop)`), `stageScript(body)` — модифицирует YAML тела, `waitForRunDone` — поллинг завершения по закрытому каналу `runOrchestratorAsync` (расширить хелпер: он уже регистрирует cleanup с done-каналом — вернуть этот канал; если хелпер не позволяет, локальный `go func(){ _ = o.Run(ctx) }` + `t.Cleanup(cancel)` по образцу соседних тестов), `assertContains`/`assertOrder` — тривиальные функции по срезу `[]lifecyclehooks.EventType`. При написании сверяться с реальными сигнатурами соседних интеграционных тестов пакета и адаптировать имена.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run TestRun_FlowEvents -v`
Expected: FAIL — событий нет (assertContains падает).

- [ ] **Step 3: Write minimal implementation**

`orchestrator.go` — новые поле и методы.

Поле структуры Orchestrator (рядом с `hooks`):

```go
	// terminalFlow — финальное flow-событие, установленное одним из выходов
	// Run; эмитится finalizeLifecycle ПОСЛЕ остановки продюсеров (single
	// writer — горутина Run, отдельная синхронизация не нужна).
	terminalFlow lifecyclehooks.EventType
```

Методы (рядом с Run):

```go
// setTerminalFlow фиксирует финальное flow-событие выхода Run.
func (o *Orchestrator) setTerminalFlow(ev lifecyclehooks.EventType) {
	o.terminalFlow = ev
}

// hasFailedStage — есть ли в снапшоте Failed-стадия. Классификатор codex
// MAJ#8: отмена ctx поверх рана, заблокированного failed-стадией (дашборд
// держит Run живым для retry), — это flow_failed, а не flow_interrupted.
func (o *Orchestrator) hasFailedStage() bool {
	for _, st := range o.opts.Store.Snapshot().Stages {
		if st.Status == state.StatusFailed {
			return true
		}
	}
	return false
}

// finalizeLifecycle эмитит терминальное flow-событие и bounded-ждёт
// опустошения очередей dispatcher. Вызывается ТОЛЬКО из defer, живёт после
// cancel()+WaitAgents() — продюсеры событий (агенты) уже остановлены.
// Раньше инлайн-flush на выходах Run наблюдал sent==done ДО того, как
// отменённые агенты успевали эмитить последнее (codex MAJ#7).
func (o *Orchestrator) finalizeLifecycle() {
	if o.terminalFlow == "" || o.hooks == nil {
		return
	}
	o.emitLifecycle(lifecyclehooks.Event{Type: o.terminalFlow})
	if !o.hooks.Flush(lifecyclehooks.FlushTimeout) {
		log.Printf("WARN: lifecycle hooks flush timed out after %s", lifecyclehooks.FlushTimeout)
	}
}
```

`Run` — пять правок:

1. ПЕРВОЙ строкой тела Run (до `defer o.concurrency.WaitAgents()` / `defer cancel()`) зарегистрировать финализатор — LIFO исполнит его ПОСЛЕДНИМ (после cancel и WaitAgents):

```go
	defer o.finalizeLifecycle()
```

2. После `o.runMu.Unlock()` (:533-535), ДО recoverReviewPause:

```go
	if o.opts.Resumed {
		o.emitLifecycle(lifecyclehooks.Event{Type: lifecyclehooks.EventFlowResumed})
	} else {
		o.emitLifecycle(lifecyclehooks.Event{Type: lifecyclehooks.EventFlowStarted})
	}
```

3. Выход ctx.Done (:568-572) — классифицируем, не флашим:

```go
		case <-ctx.Done():
			if ferr := o.loadFatal(); ferr != nil {
				o.setTerminalFlow(lifecyclehooks.EventFlowFailed)
				return ferr
			}
			if o.hasFailedStage() {
				o.setTerminalFlow(lifecyclehooks.EventFlowFailed)
			} else {
				o.setTerminalFlow(lifecyclehooks.EventFlowInterrupted)
			}
			return ctx.Err()
```

4. Выход после handleEvent-ошибки и post-handleEvent fatal (:574-579):

```go
			if err := o.handleEvent(ctx, ev); err != nil {
				o.setTerminalFlow(lifecyclehooks.EventFlowFailed)
				return err
			}
			if ferr := o.loadFatal(); ferr != nil {
				o.setTerminalFlow(lifecyclehooks.EventFlowFailed)
				return ferr
			}
```

5. Выход shouldExit (:580-595), перед `return nil` (после runEndOfRunMemory). Снапшот — в локальную переменную: у `AllDone` pointer-receiver, вызов на non-addressable результате `Snapshot()` не компилируется (codex MAJ#13):

```go
					snap := o.opts.Store.Snapshot()
					if snap.AllDone() {
						o.setTerminalFlow(lifecyclehooks.EventFlowFinished)
					} else {
						o.setTerminalFlow(lifecyclehooks.EventFlowFailed)
					}
					return nil
```

Импорт `state` в orchestrator.go уже есть.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/ -run TestRun_FlowEvents -v && go test ./pkg/orchestrator/ -count=1`
Expected: PASS (новые + все существующие; существующие тесты без хуков идут через nil-путь и не меняются).

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/
git commit -m "feat(orchestrator): flow-события lifecycle на выходах Run и bounded flush"
```

---

### Task 10: События script-границ (`agents.go`, `hooks.go`)

**Files:**
- Modify: `pkg/orchestrator/agents.go` (runScriptStage :43-62)
- Modify: `pkg/orchestrator/hooks.go` (runBeforeHook :212-260, runAfterHook :268-311)
- Test: `pkg/orchestrator/lifecycle_scripts_test.go`

**Interfaces:**
- Consumes: Task 8 (emitStageEvent).
- Produces: эмиссия 9 script-событий на границах операций. Семантика серий попыток (спека + codex MAJ#11): `*_started` эмитится перед КАЖДОЙ серией `runScriptWithRetry` (внутри внешнего `for` — пользовательский Retry в hook_failed UI запускает новую серию, и она получает свой started), `*_finished` после успеха серии, `*_failed` после исчерпания попыток серии. Повторный event_id при retry допустим (best-effort).

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/lifecyclehooks"
)

func TestScriptStage_Events(t *testing.T) {
	rec := newFlowRecorder(t)
	o := newScriptFlowOrch(t, rec, stageScript("true"))
	runOrchestratorAsync(t, o)
	waitForRunDone(t, o, 10*time.Second)

	evs := rec.ordered()
	assertOrder(t, evs, lifecyclehooks.EventStageScriptStarted, lifecyclehooks.EventStageScriptFinished)
	assertContains(t, evs, lifecyclehooks.EventStageFinished)
	assertNotContains(t, evs, lifecyclehooks.EventStageScriptFailed)
}

func TestScriptStage_FailureEvents(t *testing.T) {
	rec := newFlowRecorder(t)
	o := newScriptFlowOrch(t, rec, stageScript("exit 1"))
	runOrchestratorAsync(t, o)
	waitForRunDone(t, o, 10*time.Second)

	evs := rec.ordered()
	assertOrder(t, evs, lifecyclehooks.EventStageScriptStarted, lifecyclehooks.EventStageScriptFailed)
	assertContains(t, evs, lifecyclehooks.EventStageFailed)
	assertNotContains(t, evs, lifecyclehooks.EventStageScriptFinished)
}

func TestBeforeAfterHook_Events(t *testing.T) {
	rec := newFlowRecorder(t)
	// flow-файл: стадия script с script_before/script_after
	o := newScriptFlowOrch(t, rec, stageScript("true"),
		withBefore("echo before"), withAfter("echo after"))
	runOrchestratorAsync(t, o)
	waitForRunDone(t, o, 10*time.Second)

	evs := rec.ordered()
	assertContains(t, evs, lifecyclehooks.EventStageScriptBeforeStarted)
	assertContains(t, evs, lifecyclehooks.EventStageScriptBeforeFinished)
	assertContains(t, evs, lifecyclehooks.EventStageScriptAfterStarted)
	assertContains(t, evs, lifecyclehooks.EventStageScriptAfterFinished)
	assertOrder(t, evs, lifecyclehooks.EventStageScriptBeforeFinished, lifecyclehooks.EventStageScriptStarted)
	assertOrder(t, evs, lifecyclehooks.EventStageScriptFinished, lifecyclehooks.EventStageScriptAfterStarted)
}

func TestBeforeHook_FailureEvent(t *testing.T) {
	rec := newFlowRecorder(t)
	o := newScriptFlowOrch(t, rec, stageScript("true"), withBefore("exit 1"))
	runOrchestratorAsync(t, o)
	// hook_failed блокирует стадию; ждём событие, Run не завершается —
	// хватит появления события, затем cleanup-отмена.
	waitForEvent(t, rec, lifecyclehooks.EventStageScriptBeforeFailed, 10*time.Second)
	assertContains(t, rec.ordered(), lifecyclehooks.EventStageScriptBeforeFailed)
}
```

Хелперы `withBefore`/`withAfter` — добавляют `script_before:`/`script_after:` в YAML flow (расширить конструктор из Task 9 опциями-функциями или отдельным параметром — по вкусу, главное воспроизводимость). `waitForEvent` — поллинг rec.ordered() до появления события.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run 'TestScriptStage_|TestBeforeAfterHook_|TestBeforeHook_' -v`
Expected: FAIL — событий нет.

- [ ] **Step 3: Write minimal implementation**

`agents.go`, `runScriptStage` — обернуть вызов runScriptWithRetry:

```go
	logFile := filepath.Join(stageDir, phaseScript+".log")

	o.emitStageEvent(lifecyclehooks.EventStageScriptStarted, s.ID, "")
	err := runScriptWithRetry(ctx, func() error {
		return o.execScript(ctx, s, phaseScript, s.Script, s.ScriptTimeout, logFile)
	})
	if err != nil {
		o.emitStageEvent(lifecyclehooks.EventStageScriptFailed, s.ID, err.Error())
		o.Trigger(s.ID, bus.EvFail, bus.GuardCtx{}, err.Error())
		o.failBlockedStages()
		return
	}
	o.emitStageEvent(lifecyclehooks.EventStageScriptFinished, s.ID, "")
```

(импорты: lifecyclehooks; имя фазы `phaseScript` — оставить как есть.)

`hooks.go`, `runBeforeHook` — до `for {` и внутри:

```go
	logFile := filepath.Join(stageDir, "before.log")

	for {
		// started на каждую серию попыток — пользовательский Retry начинает
		// новую серию (codex MAJ#11).
		o.emitStageEvent(lifecyclehooks.EventStageScriptBeforeStarted, s.ID, "")
		err := runScriptWithRetry(ctx, func() error {
			return o.execScript(ctx, s, hookBefore, s.ScriptBefore, s.ScriptBeforeTimeout, logFile)
		})
		if err == nil {
			o.emitStageEvent(lifecyclehooks.EventStageScriptBeforeFinished, s.ID, "")
			return true
		}
		o.emitStageEvent(lifecyclehooks.EventStageScriptBeforeFailed, s.ID, err.Error())
		// ... существующий код registerHookWaiter/writeHookPending/triggerWithSeq
```

`runAfterHook` — симметрично с `EventStageScriptAfterStarted/Finished/Failed`, started тоже ВНУТРИ внешнего цикла перед каждым `runScriptWithRetry` (finished — в ветке `err == nil` перед `return`; failed — сразу после исчерпания `err != nil`, ДО registerHookWaiter).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/ -run 'TestScriptStage_|TestBeforeAfterHook_|TestBeforeHook_' -v && go test ./pkg/orchestrator/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/
git commit -m "feat(orchestrator): lifecycle-события script-стадии и script_before/after"
```

---

### Task 11: Диалоговые события авто-ответов (`dialog_poller.go`)

**Files:**
- Modify: `pkg/orchestrator/dialog_poller.go` (:197-232, ветка авто-ответа)
- Test: `pkg/orchestrator/lifecycle_dialog_test.go`

**Interfaces:**
- Consumes: Task 8 (emitStageEvent / emitLifecycle).
- Produces: при авто-ответе неинтерактивной стадии эмитятся `stage_question_asked` + `stage_question_answered` (не-FSM путь; интерактивный путь уже покрыт FSM-мапингом EvAskUser/EvUserAnswered из Task 8).

- [ ] **Step 1: Write the failing test**

По образцу существующих `TestPollQuestions_*` (mock-агент, пишущий question.json, и поллер): setup — неинтерактивная стадия, вопрос с опциями; после тика поллера появляются оба события. Форма:

```go
package orchestrator

import (
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/lifecyclehooks"
)

func TestPollQuestions_AutoAnswerEmitsLifecycleEvents(t *testing.T) {
	rec := newFlowRecorder(t)
	o := newPollerOrch(t, rec) // стадия non-interactive, вопросы пишет тест напрямую

	// тест сам кладёт question-файл в стадийный каталог (как TestPollQuestions_AutoAnswers):
	writeQuestionFile(t, o, "s1", "implementation", "q1", "Continue?", []string{"Yes (recommended)", "No"})

	waitForEvent(t, rec, lifecyclehooks.EventStageQuestionAnswered, 10*time.Second)
	evs := rec.ordered()
	assertOrder(t, evs, lifecyclehooks.EventStageQuestionAsked, lifecyclehooks.EventStageQuestionAnswered)
}
```

`newPollerOrch`/`writeQuestionFile` — собрать по образцу любого существующего `TestPollQuestions_`-теста (они уже умеют: создать стадию, каталог, вопрос, запустить поллер `startQuestionPoller` или полный `runOrchestratorAsync`). Свериться с фактическим соседним тестом и переиспользовать его setup максимально дословно.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run TestPollQuestions_AutoAnswerEmits -v`
Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

`dialog_poller.go`, ветка `if stage != nil && !stage.Interactive`. Два изменения против наивной эмиссии (codex MAJ#10):

1. **Гейт против дублей:** в паркинге (стадия в `awaiting_user_input`) `resumeAfterAnswer` ниже применяет FSM `EvUserAnswered`, который Task 8 уже мапит в `stage_question_answered`; `stage_question_asked` в паркинге уже случился через `EvAskUser`. Безгословная эмиссия здесь дублировала бы события. Поэтому не-FSM пара эмитится ТОЛЬКО когда стадия НЕ была запаркована (обычный случай: агент жив, опрашивает answer.json, FSM не участвует). `parked` снимаем ДО WriteAnswer.
2. **Malformed-fallback:** та же пара нужна в `autoAnswerMalformed` (~:413-431) после успешного `WriteAnswer`, с тем же гейтом.

```go
				// Гейт снимаем ДО WriteAnswer: паркинг-стадию покрывает
				// FSM-путь (EvAskUser/EvUserAnswered через triggerWithSeq).
				parked := o.currentStatus(stageID) == state.StatusAwaitingUserInput
				answer, fromOptions := mcp.PickAutoAnswer(q)
				if err := mcp.WriteAnswer(stageDir, q.Phase, q.ID, answer, fromOptions, true); err != nil {
					// ... существующий обработчик ошибки (log + continue)
				}
				processed[key] = true
				if !parked {
					o.emitLifecycle(lifecyclehooks.Event{
						Type:      lifecyclehooks.EventStageQuestionAsked,
						StageID:   stageID,
						StageName: o.stageName(stageID),
						Phase:     q.Phase,
						Reason:    q.Question,
					})
					o.emitLifecycle(lifecyclehooks.Event{
						Type:      lifecyclehooks.EventStageQuestionAnswered,
						StageID:   stageID,
						StageName: o.stageName(stageID),
						Phase:     q.Phase,
						Reason:    answer,
					})
				}
```

В `autoAnswerMalformed` — симметричный блок после успешного `mcp.WriteAnswer` (тот же `parked`-гейт; asked Reason — сырой текст заглушки, answered — авто-ответ). Импорты: lifecyclehooks, state — проверить наличие в dialog_poller.go.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/ -run TestPollQuestions -count=1 -v`
Expected: PASS (новый + все существующие poller-тесты).

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/
git commit -m "feat(orchestrator): lifecycle-события вопроса при авто-ответе неинтерактивной стадии"
```

---

### Task 12: Dashboard-warning при сбое хука (bus + notices + frontend)

**Files:**
- Modify: `pkg/orchestrator/bus/bus.go` (EventType)
- Modify: `cmd/afm/run.go` (OnError-wiring — совместно с Task 13, здесь тип и метод)
- Modify: `pkg/orchestrator/lifecycle_emit.go` (метод PublishLifecycleHookFailure)
- Modify: `pkg/web/dashboard/src/components/feed-workspace/feed-view-model.ts`
- Test: `pkg/orchestrator/lifecycle_notice_test.go`, `pkg/web/dashboard/src/components/feed-workspace/feed-view-model.test.ts`

**Interfaces:**
- Consumes: Task 5 (DispatcherOptions.OnError), существующий `publishHookNotice` (hooks.go:202-205).
- Produces:
  - `bus.EventLifecycleHookFailed EventType = "lifecycle_hook_failed"`.
  - `func (o *Orchestrator) PublishLifecycleHookFailure(hookID, eventID string, err error)` — ui.Publish + AppendNotice (одна точка, симметрично publishHookNotice).

- [ ] **Step 1: Write the failing test (Go)**

```go
package orchestrator

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishLifecycleHookFailure_Notice(t *testing.T) {
	o := newTestOrchestrator(t) // хелпер из Task 8
	o.PublishLifecycleHookFailure("telegram", "run-1:flow:flow_started", errors.New("exit status 1"))

	raw, err := os.ReadFile(filepath.Join(o.opts.RunDir, "notices.jsonl"))
	if err != nil {
		t.Fatalf("notices.jsonl: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw[:len(raw)-1], &got); err != nil { // последняя строка
		t.Fatalf("parse notice: %v (%s)", err, raw)
	}
	if got["type"] != "lifecycle_hook_failed" {
		t.Fatalf("type: %v", got["type"])
	}
	data, _ := got["data"].(map[string]any)
	if data["hook_id"] != "telegram" || data["error"] != "exit status 1" {
		t.Fatalf("data: %+v", data)
	}
}
```

(Формат строки notices: свериться со stagefiles/notices.go — noticeEntry{Time,Type,StageID,Data}; парсить последнюю строку файла; StageID здесь пустой — событие рана, не стадии.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run TestPublishLifecycleHookFailure -v`
Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

`bus.go` — рядом с EventHookFailed/EventHookResolved:

```go
	// EventLifecycleHookFailed fires when a lifecycle (observer) hook exhausts
	// its retries or its delivery queue overflows. Purely informational: never
	// affects the FSM, the stage or the run outcome.
	EventLifecycleHookFailed EventType = "lifecycle_hook_failed"
```

`lifecycle_emit.go` — метод:

```go
// PublishLifecycleHookFailure — dashboard-warning о финальном сбое lifecycle
// хука: live в ui-шину + durable в notices.jsonl (тот же one-map-two-sinks
// паттерн, что publishHookNotice). Вызывается из OnError-callback dispatcher
// (cmd/afm/run.go), поэтому метод публичный.
func (o *Orchestrator) PublishLifecycleHookFailure(hookID, eventID string, err error) {
	data := map[string]string{"hook_id": hookID, "event_id": eventID, "error": err.Error()}
	o.ui.Publish(bus.Event{Type: bus.EventLifecycleHookFailed, Data: data})
	stagefiles.AppendNotice(o.opts.RunDir, "", string(bus.EventLifecycleHookFailed), data)
}
```

(импорты bus, stagefiles в lifecycle_emit.go; сверить сигнатуру AppendNotice — `(runDir, stageID, eventType string, data any)`.)

- [ ] **Step 4: Frontend — failing test затем мапинг**

`feed-view-model.test.ts` — по образцу соседних кейсов (найти тест для `hook_failed` или `context_warning`):

```typescript
it('renders lifecycle_hook_failed as system warning with hook id', () => {
  const items = mapFeedEvents([
    { type: 'lifecycle_hook_failed', data: { hook_id: 'telegram', event_id: 'e1', error: 'exit status 1' }, ts: 1 },
  ]);
  expect(items).toHaveLength(1);
  expect(items[0].side).toBe('system');
  expect(JSON.stringify(items[0])).toContain('telegram');
});
```

(Точная форма входного события — сверить с тем, как `/api/events` отдаёт notices: `{type, stage_id, data, ts}`-подобный объект; взять соседний тест за эталон и повторить его форму с другим type.)

Реализация в `feed-view-model.ts` — кейс рядом с обработкой `hook_failed`:

```typescript
case 'lifecycle_hook_failed': {
  const d = (ev.data ?? {}) as { hook_id?: string; error?: string };
  return { /* system/tone-warning item, текст вида: `Lifecycle hook ${d.hook_id ?? '?'} failed: ${d.error ?? ''}` */ };
}
```

(точную структуру FeedItem повторить из соседнего кейса файла).

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./pkg/orchestrator/ -run TestPublishLifecycleHookFailure -v` и `cd pkg/web/dashboard && npx vitest run src/components/feed-workspace/feed-view-model.test.ts`
Expected: PASS оба.

- [ ] **Step 6: Commit**

```bash
git add pkg/orchestrator/ pkg/web/dashboard/src/
git commit -m "feat(lifecycle): dashboard-notice о сбое lifecycle-хука и его рендер в ленте"
```

---

### Task 13: Сборка в `cmd/afm/run.go` + интеграционный тест

**Files:**
- Modify: `pkg/state/store.go` (Snapshot: копировать LastSeq)
- Modify: `cmd/afm/agent_environment.go` (lifecycleRootDir) + `cmd/afm/run.go` (блок orchOpts :275-290, unified defer)
- Test: `pkg/state/store_test.go` (доп.), `cmd/afm/agent_environment_test.go` (доп.), `pkg/orchestrator/lifecycle_integration_test.go`

**Interfaces:**
- Consumes: всё выше (Combine, New/Start/Stop/Flush, Options-поля, PublishLifecycleHookFailure).
- Produces: production-wiring: dispatcher из слоёв config+flow+stage, `Resumed` по `store.Snapshot().LastSeq > 0`, flush после Run, Stop через defer.

- [ ] **Step 1: Write the failing test (интеграционный, настоящий sh-раннер)**

```go
package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/lifecyclehooks"
)

// Полный Phase-1 контур: настоящие процессы sh, настоящий dispatcher, хук
// дописывает каждое событие в файл. Проверка — по содержимому файла.
func TestIntegration_LifecycleHooks_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "captured.events")

	flowPath := filepath.Join(dir, "flow.yaml")
	flowYAML := `
name: hooks-e2e
hooks:
  - id: capture-flow
    events: [flow_started, flow_finished, stage_finished]
    command: sh -c 'cat >> ` + capture + `'
stages:
  - id: s1
    script: "true"
    hooks:
      - id: capture-stage
        events: [stage_script_started, stage_script_finished]
        command: sh -c 'cat >> ` + capture + `'
`
	if err := os.WriteFile(flowPath, []byte(flowYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := flow.ParseFile(flowPath)
	if err != nil {
		t.Fatal(err)
	}

	// store/runDir как в соседних интеграционных тестах пакета
	runDir, store := newRunStoreForTest(t, "hooks-e2e") // grep соседей: state.Open на t.TempDir()

	disp := lifecyclehooks.New(lifecyclehooks.DispatcherOptions{
		Config: lifecyclehooks.DispatcherConfig{
			FlowName: f.Name, RunID: "hooks-e2e-run", RunDir: runDir, RootDir: dir,
		},
		Hooks: lifecyclehooks.Combine(
			lifecyclehooks.Layer{Hooks: f.Hooks},
			lifecyclehooks.Layer{StageID: "s1", Hooks: f.Stages[0].Hooks},
		),
		LogDir: filepath.Join(runDir, "hooks"),
	})
	disp.Start()
	t.Cleanup(disp.Stop)

	o := newOrchestratorForFlow(t, f, runDir, store, disp) // New(Options{... Hooks: disp, FlowName, RunID, RootDir: dir, ...})
	runOrchestratorAsync(t, o)
	waitForRunDone(t, o, 15*time.Second)

	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("capture file: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		`"event":"flow_started"`,
		`"schema_version":1`,
		`"event":"stage_script_started"`,
		`"event":"stage_script_finished"`,
		`"event":"stage_finished"`,
		`"event":"flow_finished"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("capture missing %s\nbody:\n%s", want, body)
		}
	}
	if strings.Contains(body, `"event":"stage_script_started"`) && !strings.Contains(body, `"stage":{"id":"s1"`) {
		t.Errorf("stage-scoped hook must carry stage block\n%s", body)
	}

	// логи хуков на месте
	for _, id := range []string{"capture-flow", "capture-stage"} {
		if _, err := os.Stat(filepath.Join(runDir, "hooks", id+".log")); err != nil {
			t.Errorf("hook log %s: %v", id, err)
		}
	}
}
```

Хелперы `newRunStoreForTest`/`newOrchestratorForFlow` — извлечь из setup любого существующего интеграционного теста пакета (они строят store+orchestrator; скорее всего, это уже частично есть в хелперах из Task 8-9 — переиспользовать и там, и здесь; если удобнее, расширить `newScriptFlowOrch` параметром flow-объекта).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run TestIntegration_LifecycleHooks_EndToEnd -v`
Expected: FAIL — capture-файл пуст/отсутствует (пока wiring не сделан... wiring в orchestrator уже есть из Task 8-11; этот тест проверяет ТО ЖЕ САМОЕ через реальный раннер и ДОЛЖЕН пройти уже сейчас; если упал — чинить findbug в хелперах, а не в проде. Если он проходит сразу — это нормально: он страховка регресса для wiring run.go, который ниже).

- [ ] **Step 3a: `pkg/state/store.go` — Snapshot() теряет LastSeq (codex MAJ#6)**

`Snapshot()` (:189-206) не копирует `LastSeq` — `Snapshot().LastSeq` всегда 0, и `Resumed` в production никогда не сработал бы. Добавить поле в копию:

```go
	out := RunState{
		FlowName:             s.snapshot.FlowName,
		StartedAt:            s.snapshot.StartedAt,
		LastSeq:              s.snapshot.LastSeq,
		StageOrder:           append([]string(nil), s.snapshot.StageOrder...),
		Stages:               make(map[string]StageState, len(s.snapshot.Stages)),
		IdleAccumulatedMs:    s.snapshot.IdleAccumulatedMs,
		BackoffAccumulatedMs: s.snapshot.BackoffAccumulatedMs,
	}
```

Регресс-тест (в `pkg/state`, по образцу соседних): открыть store на пустом каталоге → `Snapshot().LastSeq == 0`; применить переход → `Snapshot().LastSeq == 1`. Запуск: `go test ./pkg/state/ -count=1`.

- [ ] **Step 3b: Modify `cmd/afm/run.go`**

Предпосылки (codex MAJ#9, MAJ#13): в run.go НЕТ переменной `runID` — вычислить `runID := filepath.Base(runDir)` рядом с определением runDir. При пустом `agentRootDir` («наследовать CWD») метаданные хука всё равно требуют абсолютный `AFM_ROOT_DIR` по спеке — хелпер рядом с `resolveAgentRoot` (cmd/afm/agent_environment.go):

```go
// lifecycleRootDir — абсолютный корень для метаданных lifecycle-хуков
// (AFM_ROOT_DIR, CWD hook-команды). Пустой agentRootDir означает
// «наследовать CWD» — для контракта хуков это фактический CWD процесса.
func lifecycleRootDir(agentRootDir string) string {
	if agentRootDir != "" {
		return agentRootDir
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}
```

плюс мини-тест в `cmd/afm` (по образцу TestResolveAgentRoot_MatchesRunInlineLogic): непустой — passthrough; пустой — непустой абсолютный путь.

Блок после `memDir`-резолва (:232) / перед `orchOpts` (:275):

```go
			// Lifecycle hooks (Phase 1): слои global+project уже смёржены в
			// cfg.Hooks (config.LoadFrom), сюда добавляются flow- и stage-слои.
			// Dispatcher живёт на собственном ctx (Stop ниже) — хуки-наблюдатели
			// не зависят от отмены run-ctx (flow_interrupted должен уйти).
			runID := filepath.Base(runDir)
			hooksRootDir := lifecycleRootDir(agentRootDir)
			resumed := store.Snapshot().LastSeq > 0
			var hooksDisp *lifecyclehooks.Dispatcher
			var orchRef *orchestrator.Orchestrator
			layers := []lifecyclehooks.Layer{{Hooks: cfg.Hooks}, {Hooks: f.Hooks}}
			for _, st := range f.Stages {
				if len(st.Hooks) > 0 {
					layers = append(layers, lifecyclehooks.Layer{StageID: st.ID, Hooks: st.Hooks})
				}
			}
			if combined := lifecyclehooks.Combine(layers...); len(combined) > 0 {
				hooksDisp = lifecyclehooks.New(lifecyclehooks.DispatcherOptions{
					Config: lifecyclehooks.DispatcherConfig{
						FlowName: f.Name,
						RunID:    runID,
						RunDir:   runDir,
						RootDir:  hooksRootDir,
						Resumed:  resumed,
					},
					Hooks: combined,
					LogDir: filepath.Join(runDir, "hooks"),
					OnError: func(hookID, eventID string, err error) {
						if orchRef != nil {
							orchRef.PublishLifecycleHookFailure(hookID, eventID, err)
							return
						}
						fmt.Fprintf(os.Stderr, "warning: lifecycle hook %s failed (%s): %v\n", hookID, eventID, err)
					},
				})
				// Единая точка остановки: bounded flush, затем Stop. defer
				// исполняется на ЛЮБОМ выходе из runHandler (в т.ч. по ошибке
				// orch.Run — ранний return с пропущенным flush терял бы
				// terminal-события; codex MAJ#7).
				defer func() {
					if hooksDisp != nil {
						if !hooksDisp.Flush(lifecyclehooks.FlushTimeout) {
							fmt.Fprintf(os.Stderr, "warning: lifecycle hooks flush timed out\n")
						}
						hooksDisp.Stop()
					}
				}()
			}
```

В `orchOpts` (:275-290) добавить поля:

```go
				FlowName: f.Name,
				RunID:    runID,
				Resumed:  resumed,
				Hooks:    hooksDisp,
```

После `orch := orchestrator.New(orchOpts)` — связать orchRef и запустить воркеры (в том же блоке `if hooksDisp != nil`):

```go
			orch := orchestrator.New(orchOpts)
			if hooksDisp != nil {
				// orchRef замыкается в OnError выше; Start после New, чтобы
				// warning-notice уже имел живой orchestrator.
				// Присвоение orchRef делаем ДО Start (гонка Emit-до-Start
				// безопасна: очереди буферизуются).
			}
```

Фактически: вынести объявление `var orchRef *orchestrator.Orchestrator` выше конструкции хуков (как в первом снипете), а сразу после `orch := orchestrator.New(orchOpts)` поставить:

```go
			orchRef = orch
			if hooksDisp != nil {
				hooksDisp.Start()
			}
```

`runID` вычислен выше (Step 3b) — `filepath.Base(runDir)`.

Отдельный flush ПОСЛЕ `orch.Run(...)` не нужен: orchestrator флашит в `finalizeLifecycle` (Task 9), а defer выше ловит всё, что эмитилось после него (поздние продюсеры, flow_interrupted). Отмена run-ctx не убивает hook-команды — dispatcher живёт на собственном ctx.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./cmd/... && go test ./pkg/orchestrator/ -run TestIntegration_LifecycleHooks -count=1 -v && go vet ./...`
Expected: PASS; build чистый.

- [ ] **Step 5: Commit**

```bash
git add cmd/afm/run.go pkg/orchestrator/
git commit -m "feat(run): сборка lifecycle dispatcher из слоёв и flush после Run"
```

---

### Task 14: Документация AGENTS.md + финальная верификация

**Files:**
- Modify: `AGENTS.md` (новый раздел «Lifecycle hooks (Phase 1)»)

**Interfaces:**
- Consumes: всё.
- Produces: документация фичи в стиле существующих разделов.

- [ ] **Step 1: Написать раздел AGENTS.md**

Раздел разместить после «Pre-note» (перед «Stage buttons» или рядом — по хронологии разделов). Содержание (на русском, в стиле соседних разделов — плотно, с паттернами и ссылками на файлы):

- Назначение: observer-only уведомления/метрики; отличие от script_before/after (те блокируют, эти — нет; ошибка хука не трогает FSM).
- Конфигурация: 4 слоя (global config → project config → flow → stage), примеры YAML, `events: all | [list]`, `skip_events`, `timeout` (default 30s), `retries` (default 0).
- Каталог событий: 27 имён (5 flow + 13 stage + 9 script) — перечислить.
- Precedence по id: поздний слой заменяет ранний целиком (включая скоуп); разные id складываются; dup id в одном слое — ошибка парсинга; flow-события в stage hook — ошибка.
- Payload контракт: JSON в stdin, schema_version 1, event_id схемы (`:transition:seq:` / `:flow:` / `:stage:`), 10 AFM_* env.
- Runner: sh -c, CWD = эффективный root_dir, наследование окружения AFM (Phase 1), логи `.afm/runs/<run>/hooks/<id>.log`, process-group kill по таймауту.
- Dispatcher: per-hook последовательность / межхуковая параллельность, очередь 256, дроп с `lifecycle_hook_failed` notice, bounded flush 10s на выходах Run; собственный ctx (независим от run-ctx).
- Гарантии Phase 1: live best-effort; durable outbox и секреты — Phase 2/3 (спека `docs/superpowers/specs/2026-09-18-lifecycle-hooks-design.md`).
- Ключевые точки кода: pkg/lifecyclehooks/*, orchestrator.go (triggerWithSeq/Run), lifecycle_emit.go, run.go сборка.

- [ ] **Step 2: Финальная верификация**

```bash
make lint
make test
cd pkg/web/dashboard && npx vitest run
```

Expected: lint 0 issues; все Go-тесты зелёные; frontend-сьют зелёный.

- [ ] **Step 3: Commit**

```bash
git add AGENTS.md
git commit -m "docs: lifecycle-хуки Phase 1 в AGENTS.md"
```

---

## Self-Review

### Раунд codex (2026-09-18, 2 CRITICAL + 11 MAJOR) — все внесены в план

Приняты: path-traversal id (T2), Emit/Stop race (T5), overlay-валидация до merge (T6), racy from → FSM.Apply возвращает from (T8), Snapshot LastSeq + shutdown-порядок + классификатор выхода + ROOT_DIR/runID/компилябельность (T9/T13), started на каждую серию (T10), дубли авто-ответов + malformed (T11), потоковый лог вместо буфера (T4).

Единственное отклонение от рекомендации codex (MAJ#3): предложен scope-aware routing stage-override; вместо него — плоская замена по id (требование спеки «Наследование и порядок», одобрено пользователем) + новая ошибка валидации на одинаковый id в хуках разных стадий (молчаливое схлопывание — реальная проблема). Ruling: scope-aware routing противоречит утверждённой спеке; цена ошибки — флоу с одинаковыми id в разных стадиях потребует переименования (явная ошибка подскажет).

- **Spec coverage:** 4 слоя + keyed-merge id (Tasks 3, 6, 7, 13); каталог+`all`+`skip_events` (1, 2); payload stdin + env (1, 4); timeout/retries (4); последовательность/параллельность (5); логи (4); dashboard-warning (12); bounded flush (5, 9, 13); FSM-эмиссия через triggerWithSeq (8); flow-события+resumed (9, 13); script-события (10); диалоговые авто-ответы (11); schemacheck (6, 7). Не входит в Phase 1 по спеке: outbox, секреты, Docker, `[REDACTED]`, `inherit_env` — задач нет, и не должно быть.
- **Placeholder scan:** код во всех шагах полный; места, требующие сверки с соседним кодом (feed-view-model, тест-хелперы orchestrator), снабжены точным указанием, что сверять и по какому эталону.
- **Type consistency:** сигнатуры `runCommand(ctx, h, cfg, p, logPath)`, `RunFunc` в DispatcherOptions, `Emit(Event)`, `Flush(timeout) bool`, `Stop()`, `ValidateLayer(defs, stageScoped)`, `Combine(layers...)`, `emitStageEvent/emitLifecycleTransition` — одинаковы во всех задачах.
