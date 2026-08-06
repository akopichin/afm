# Разбить pkg/orchestrator по ответственности — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Вынести из `pkg/orchestrator` пять уже-независимых или безопасно-декомпозируемых кластеров (`bus`, `graph`, `supervisor`, `stagefiles`, `concurrency`) в подпакеты, снизив overengineering_pressure пакета (91 в strictacode-отчёте) без изменения поведения.

**Architecture:** Механический перенос файлов с уже-независимыми типами (bus, fsm, graph, supervisor, stagefiles-функции) в подпакеты `pkg/orchestrator/<name>`; для `concurrency` — новый тип `Manager`, не требующий callback-интерфейса обратно в ядро (единственная его зависимость, `*bus.CriticalBus`, уже независима после переноса bus). Ядро (`orchestrator.go`, `scheduling.go`, `recovery.go`, `agents.go`, `control_api.go`, `runner_factory.go`, `supervisor_track.go`, `hooks.go`, `dialog_poller.go`, `retry.go`, `errors.go`) не трогается — это один связный конечный автомат, дробить дальше не в этом плане (см. спеку).

**Tech Stack:** Go, стандартный `testing`, без новых зависимостей.

## Global Constraints

- Ни один новый пакет (`bus`, `graph`, `supervisor`, `stagefiles`, `concurrency`) не импортирует `pkg/orchestrator` — только `pkg/flow`, `pkg/executor`, `pkg/state`, stdlib. Нарушение = ошибка компиляции (import cycle), но проверяй явно перед коммитом.
- Каждая задача завершается зелёным `go build ./...` и зелёным `go test ./pkg/orchestrator/... ./pkg/server/...` — это поведение-сохраняющий рефакторинг, красный тест = регрессия переноса, а не ожидаемое изменение (кроме нового теста на `TryPublish` в Задаче 4).
- Коммиты — на русском языке, без `Co-Authored-By`.
- НЕ экспортировать `TriggerWithSeq`/`HasOpenQuestion`/`CurrentStatus`/`FailBlockedStages` или любые другие FSM-control методы `Orchestrator` ради этого рефакторинга — решение отклонено в спеке.
- Порядок задач фиксирован: Задача 5 (concurrency) зависит от Задачи 4 (bus) — `concurrency.Manager` использует `*bus.CriticalBus`.

---

### Task 1: Вынести `pkg/orchestrator/graph`

**Files:**
- Create: `pkg/orchestrator/graph/graph.go` (содержимое `pkg/orchestrator/graph.go`, `package graph`)
- Create: `pkg/orchestrator/graph/graph_test.go` (содержимое `pkg/orchestrator/graph_test.go`, `package graph_test` — уже внешний тест-пакет, нужна только смена имени пакета)
- Delete: `pkg/orchestrator/graph.go`, `pkg/orchestrator/graph_test.go`
- Modify: `pkg/orchestrator/orchestrator.go:69` (тип поля `graph *Graph` → `graph *graph.Graph`), импорт `"github.com/akopichin/afm/pkg/orchestrator/graph"`
- Modify: `pkg/orchestrator/orchestrator.go:236` (`graph: NewGraph(opts.Stages)` → `graph: graph.NewGraph(opts.Stages)`)
- Modify: `pkg/orchestrator/approve_test.go` (вызов `NewGraph(stages)` → `graph.NewGraph(stages)`, добавить импорт)
- Modify: `pkg/orchestrator/runctx_test.go` (аналогично)

**Interfaces:**
- Produces: `graph.Graph` (тип), `graph.NewGraph(stages []flow.Stage) *graph.Graph`, методы `(*graph.Graph).ReadyStages(statuses map[string]state.StageStatus) []string`, `.Stage(id string) *flow.Stage`, `.AllIDs() []string` — сигнатуры не меняются, меняется только пакет.

- [ ] **Step 1: Создать директорию и переместить файлы**

```bash
mkdir -p pkg/orchestrator/graph
git mv pkg/orchestrator/graph.go pkg/orchestrator/graph/graph.go
git mv pkg/orchestrator/graph_test.go pkg/orchestrator/graph/graph_test.go
```

- [ ] **Step 2: Поменять package-декларации**

В `pkg/orchestrator/graph/graph.go` первая строка `package orchestrator` → `package graph`.
В `pkg/orchestrator/graph/graph_test.go` первая строка `package orchestrator_test` → `package graph_test`.

- [ ] **Step 3: Поправить orchestrator.go**

В `pkg/orchestrator/orchestrator.go` добавить импорт:
```go
"github.com/akopichin/afm/pkg/orchestrator/graph"
```
Строка 69: `graph    *Graph` → `graph    *graph.Graph`
Строка 236: `graph:          NewGraph(opts.Stages),` → `graph:          graph.NewGraph(opts.Stages),`

Проверить оставшиеся использования `o.graph` в `control_api.go`, `dialog_poller.go`, `hooks.go`, `scheduling.go` — они вызывают только методы (`o.graph.Stage(...)`, `.AllIDs()`, `.ReadyStages(...)`), тип методов не меняется, правок не требуют.

- [ ] **Step 4: Поправить approve_test.go и runctx_test.go**

В обоих файлах: добавить импорт `"github.com/akopichin/afm/pkg/orchestrator/graph"`, заменить `NewGraph(stages)` → `graph.NewGraph(stages)`.

- [ ] **Step 5: Собрать и прогнать тесты**

```bash
go build ./... 2>&1 | head -50
go test ./pkg/orchestrator/... ./pkg/orchestrator/graph/... -v 2>&1 | tail -60
```
Ожидание: компилируется, все тесты зелёные (поведение не менялось).

- [ ] **Step 6: Коммит**

```bash
git add -A pkg/orchestrator/
git commit -m "refactor(orchestrator): выносим Graph в pkg/orchestrator/graph"
```

---

### Task 2: Вынести `pkg/orchestrator/supervisor`

**Files:**
- Create: `pkg/orchestrator/supervisor/supervisor.go` (из `pkg/orchestrator/supervisor.go`, `package supervisor`)
- Create: `pkg/orchestrator/supervisor/supervisor_test.go` (из `pkg/orchestrator/supervisor_test.go`, `package supervisor`)
- Delete: `pkg/orchestrator/supervisor.go`, `pkg/orchestrator/supervisor_test.go`
- Modify: `pkg/orchestrator/orchestrator.go:102` (поле `supervisor *Supervisor` → `supervisor *supervisor.Supervisor`), импорт
- Modify: `pkg/orchestrator/orchestrator.go:229-231` (конструктор)

**Interfaces:**
- Consumes: `executor.Runner` (из `pkg/executor`, не меняется), `flow.Stage` (из `pkg/flow`, не меняется)
- Produces: `supervisor.Supervisor`, `supervisor.NewSupervisor(r executor.Runner) *supervisor.Supervisor`, `(*supervisor.Supervisor).EvaluateStage(ctx context.Context, stage flow.Stage, globalPrompt string) (*supervisor.EvaluationResult, error)` — `supervisor_track.go` вызывает `o.supervisor.EvaluateStage(...)` через `:=`, тип выводится автоматически, правок в `supervisor_track.go` не требуется.

- [ ] **Step 1: Переместить файлы**

```bash
mkdir -p pkg/orchestrator/supervisor
git mv pkg/orchestrator/supervisor.go pkg/orchestrator/supervisor/supervisor.go
git mv pkg/orchestrator/supervisor_test.go pkg/orchestrator/supervisor/supervisor_test.go
```

- [ ] **Step 2: Поменять package-декларацию**

Оба файла: `package orchestrator` → `package supervisor` (первая строка каждого).

- [ ] **Step 3: Поправить orchestrator.go**

Добавить импорт `"github.com/akopichin/afm/pkg/orchestrator/supervisor"`.
Строка 102: `supervisor *Supervisor` → `supervisor *supervisor.Supervisor`
Строки 229-231:
```go
var sup *supervisor.Supervisor
if opts.SupervisorRunner != nil {
    sup = supervisor.NewSupervisor(opts.SupervisorRunner)
}
```

Внимание: поле `Orchestrator.supervisor` и импортированный пакет `supervisor` теперь одноимённые в файле `orchestrator.go` — Go разрешает это (поле доступно только через `o.supervisor`, пакет — как `supervisor.Xxx`), но проверь при чтении, что компилятор не путается (не должен, это разные namespace'ы: селектор поля vs идентификатор пакета).

- [ ] **Step 4: Собрать и прогнать тесты**

```bash
go build ./... 2>&1 | head -50
go test ./pkg/orchestrator/... ./pkg/orchestrator/supervisor/... -v 2>&1 | tail -60
```

- [ ] **Step 5: Коммит**

```bash
git add -A pkg/orchestrator/
git commit -m "refactor(orchestrator): выносим Supervisor в pkg/orchestrator/supervisor"
```

---

### Task 3: Вынести `pkg/orchestrator/stagefiles`

**Files:**
- Create: `pkg/orchestrator/stagefiles/session.go`, `notices.go`, `plan_adopt.go`, `context.go`, `completion.go` (из одноимённых файлов `pkg/orchestrator/*.go`, `package stagefiles`)
- Create: `pkg/orchestrator/stagefiles/notices_test.go`, `plan_adopt_test.go`, `context_test.go`, `completion_test.go` (аналогично)
- Delete: соответствующие файлы в `pkg/orchestrator/`
- Modify: `pkg/orchestrator/agents.go`, `recovery.go`, `retry.go`, `scheduling.go`, `hooks.go`, `runner_factory.go` — вызовы функций stagefiles
- Modify: `pkg/orchestrator/orchestrator_test.go`, `plan_source_test.go` — вызовы `CollectDependencyPlans`/`CollectArtifacts`

**Interfaces:**
- Produces (все функции экспортируются — сейчас часть из них приватные):
  - `stagefiles.LoadOrCreateSession(stageDir, phase string) (string, error)` (было `loadOrCreateSession`)
  - `stagefiles.SessionFile(stageDir, phase string) string` (было `sessionFile`)
  - `stagefiles.SessionExists(stageDir, phase string) bool` (было `sessionExists`)
  - `stagefiles.AppendNotice(runDir, stageID, eventType string, data any)` (было `appendNotice`)
  - `stagefiles.AdoptWrittenPlan(logFile, outFile string) bool` (было `adoptWrittenPlan`)
  - `stagefiles.CheckPlanCompletion(stageDir string) error` (было `checkPlanCompletion`)
  - `stagefiles.CheckPlanCompletionFor(stageDir string, interactive bool) error` (было `checkPlanCompletionFor`)
  - `stagefiles.IsIncompleteWorkError(err error) bool` (было `isIncompleteWorkError`)
  - `stagefiles.CheckAutonomousCompletion(stageDir string) error` (было `checkAutonomousCompletion`)
  - `stagefiles.CheckCompletion(stageDir, projectDir string, stage flow.Stage) error` (было `checkCompletion`)
  - `stagefiles.RunVerify(projectDir, command string) error` (было `runVerify`, используется только внутри `completion.go` — экспортировать не обязательно, но экспортируем для консистентности пакета)
  - `stagefiles.CollectDependencyPlans(...)`, `stagefiles.CollectArtifacts(...)` (уже экспортированы, просто добавляется префикс пакета)
  - `stagefiles.ResolveArtifactPath(...)` (было `resolveArtifactPath`, используется внутри `completion.go` и `context.go` — оба переезжают, останется вызовом без префикса внутри пакета `stagefiles`; экспортировать не обязательно, оставь `resolveArtifactPath` приватным — оба вызывающих файла в одном пакете)
  - `newUUID` (используется только внутри `session.go`, остаётся приватной)
  - `phaseSession` (тип, используется только внутри `session.go`, остаётся приватным)
  - `noticeEntry` (тип, используется только внутри `notices.go`, остаётся приватным)

**Важно:** экспортируй ТОЛЬКО функции, вызываемые извне пакета (список выше с префиксом `stagefiles.`). `runVerify`/`resolveArtifactPath`/`newUUID` вызываются только внутри своего же файла в новом пакете — оставь их приватными (`runVerify`, `resolveArtifactPath`), НЕ экспортируй без нужды.

- [ ] **Step 1: Переместить файлы**

```bash
mkdir -p pkg/orchestrator/stagefiles
git mv pkg/orchestrator/session.go pkg/orchestrator/stagefiles/session.go
git mv pkg/orchestrator/notices.go pkg/orchestrator/stagefiles/notices.go
git mv pkg/orchestrator/notices_test.go pkg/orchestrator/stagefiles/notices_test.go
git mv pkg/orchestrator/plan_adopt.go pkg/orchestrator/stagefiles/plan_adopt.go
git mv pkg/orchestrator/plan_adopt_test.go pkg/orchestrator/stagefiles/plan_adopt_test.go
git mv pkg/orchestrator/context.go pkg/orchestrator/stagefiles/context.go
git mv pkg/orchestrator/context_test.go pkg/orchestrator/stagefiles/context_test.go
git mv pkg/orchestrator/completion.go pkg/orchestrator/stagefiles/completion.go
git mv pkg/orchestrator/completion_test.go pkg/orchestrator/stagefiles/completion_test.go
```

- [ ] **Step 2: Поменять package-декларацию во всех 9 файлах**

Каждый файл: первая строка `package orchestrator` → `package stagefiles`.

- [ ] **Step 3: Экспортировать функции в session.go**

В `pkg/orchestrator/stagefiles/session.go`:
```go
func sessionFile(stageDir, phase string) string       →  func SessionFile(stageDir, phase string) string
func loadOrCreateSession(stageDir, phase string) ...   →  func LoadOrCreateSession(stageDir, phase string) ...
func sessionExists(stageDir, phase string) bool        →  func SessionExists(stageDir, phase string) bool
```
`newUUID` и `phaseSession` — не трогать (остаются приватными, используются только внутри этого файла).

- [ ] **Step 4: Экспортировать функцию в notices.go**

```go
func appendNotice(runDir, stageID, eventType string, data any)  →  func AppendNotice(runDir, stageID, eventType string, data any)
```
`noticeEntry` — не трогать.

- [ ] **Step 5: Экспортировать функцию в plan_adopt.go**

```go
func adoptWrittenPlan(logFile, outFile string) bool  →  func AdoptWrittenPlan(logFile, outFile string) bool
```

- [ ] **Step 6: Поправить импорты в context.go**

`context.go` уже экспортирует `CollectDependencyPlans`/`CollectArtifacts` — без изменений сигнатур. `resolveArtifactPath` — не трогать (используется и в `context.go`, и в `completion.go`, оба в одном новом пакете).

- [ ] **Step 7: Экспортировать функции в completion.go**

```go
func checkPlanCompletion(stageDir string) error                              →  func CheckPlanCompletion(stageDir string) error
func checkPlanCompletionFor(stageDir string, interactive bool) error         →  func CheckPlanCompletionFor(stageDir string, interactive bool) error
func isIncompleteWorkError(err error) bool                                   →  func IsIncompleteWorkError(err error) bool
func checkAutonomousCompletion(stageDir string) error                        →  func CheckAutonomousCompletion(stageDir string) error
func checkCompletion(stageDir, projectDir string, stage flow.Stage) error    →  func CheckCompletion(stageDir, projectDir string, stage flow.Stage) error
func runVerify(projectDir, command string) error                             →  func RunVerify(projectDir, command string) error
```
Внутри `completion.go` два внутренних вызова между этими функциями (оба остаются без префикса пакета — вызов внутри самого пакета `stagefiles`, только с большой буквы):
- `CheckPlanCompletion` (строка 17) зовёт `checkPlanCompletionFor` → `CheckPlanCompletionFor`
- `CheckCompletion` зовёт `resolveArtifactPath` (не переименовывается, остаётся приватным) и `runVerify` → `RunVerify`

- [ ] **Step 8: Поправить call site'ы в ядре — agents.go**

В `pkg/orchestrator/agents.go` добавить импорт `"github.com/akopichin/afm/pkg/orchestrator/stagefiles"`, заменить:
- `appendNotice(...)` → `stagefiles.AppendNotice(...)`
- `adoptWrittenPlan(...)` → `stagefiles.AdoptWrittenPlan(...)`
- `checkPlanCompletionFor(...)` → `stagefiles.CheckPlanCompletionFor(...)`
- `checkAutonomousCompletion(...)` → `stagefiles.CheckAutonomousCompletion(...)`
- `checkCompletion(...)` → `stagefiles.CheckCompletion(...)`
- `CollectDependencyPlans(...)` → `stagefiles.CollectDependencyPlans(...)`
- `CollectArtifacts(...)` → `stagefiles.CollectArtifacts(...)`

- [ ] **Step 9: Поправить call site'ы — recovery.go**

Добавить импорт `stagefiles`, заменить `checkPlanCompletion(...)` → `stagefiles.CheckPlanCompletion(...)`, `checkAutonomousCompletion(...)` → `stagefiles.CheckAutonomousCompletion(...)`, `checkCompletion(...)` → `stagefiles.CheckCompletion(...)`.

- [ ] **Step 10: Поправить call site'ы — retry.go**

Добавить импорт `stagefiles`, заменить `sessionFile(...)` → `stagefiles.SessionFile(...)`, `appendNotice(...)` → `stagefiles.AppendNotice(...)`, `isIncompleteWorkError(...)` → `stagefiles.IsIncompleteWorkError(...)`.

- [ ] **Step 11: Поправить call site'ы — scheduling.go**

Добавить импорт `stagefiles`, заменить `sessionFile(...)` → `stagefiles.SessionFile(...)`.

- [ ] **Step 12: Поправить call site'ы — hooks.go**

Добавить импорт `stagefiles`, заменить `appendNotice(...)` → `stagefiles.AppendNotice(...)`.

- [ ] **Step 13: Поправить call site'ы — runner_factory.go**

Добавить импорт `stagefiles`, заменить `loadOrCreateSession(...)` → `stagefiles.LoadOrCreateSession(...)`, `sessionExists(...)` → `stagefiles.SessionExists(...)`.

- [ ] **Step 14: Поправить тесты ядра**

В `pkg/orchestrator/orchestrator_test.go` и `pkg/orchestrator/plan_source_test.go`: добавить импорт `stagefiles`, заменить `CollectDependencyPlans(...)` → `stagefiles.CollectDependencyPlans(...)`, `CollectArtifacts(...)` → `stagefiles.CollectArtifacts(...)`.

- [ ] **Step 15: integration_interactive_test.go — без изменений**

Строка 278 упоминает `loadOrCreateSession` только внутри комментария (реальный код рядом — `filepath.Join`/`os.Stat` по имени файла сессии, без вызова функции). Изменений не требуется, шаг только для явной фиксации — не пропусти файл молча.

- [ ] **Step 16: Собрать и прогнать тесты**

```bash
go build ./... 2>&1 | head -80
go test ./pkg/orchestrator/... ./pkg/orchestrator/stagefiles/... -v 2>&1 | tail -100
```

- [ ] **Step 17: Коммит**

```bash
git add -A pkg/orchestrator/
git commit -m "refactor(orchestrator): выносим файловые утилиты стадии в pkg/orchestrator/stagefiles"
```

---

### Task 4: Вынести `pkg/orchestrator/bus` (bus.go + fsm.go)

**Files:**
- Create: `pkg/orchestrator/bus/bus.go`, `pkg/orchestrator/bus/fsm.go` (из одноимённых, `package bus`)
- Create: `pkg/orchestrator/bus/bus_test.go`, `pkg/orchestrator/bus/fsm_test.go`
- Delete: `pkg/orchestrator/bus.go`, `fsm.go`, `bus_test.go`, `fsm_test.go`
- Modify: `pkg/orchestrator/orchestrator.go` (поля `critical`, `ui`, `fsm`; вызовы `Trigger`)
- Modify: `pkg/orchestrator/agents.go`, `control_api.go`, `dialog_poller.go`, `hooks.go`, `recovery.go`, `retry.go`, `scheduling.go`, `supervisor_track.go` (все использования `Ev*`/`Event*`/`GuardCtx`/`FSMEvent`)
- Modify (тесты ядра): `approve_test.go`, `fatal_test.go`, `hooks_test.go`, `runctx_test.go`, `shutdown_test.go`, `agent_suggest_race_test.go`, `auto_approve_test.go`, `integration_interactive_test.go`, `integration_hooks_test.go`, `poller_test.go`, `recovery_hooks_test.go`, `scenario_test.go`
- Modify: `pkg/server/server.go`, `pkg/server/websocket.go` (внешние потребители `orchestrator.UIBus`/`orchestrator.Event`)

**Интерфейс без изменений** (только добавляется префикс `bus.` и два новых метода на `CriticalBus`):
- `bus.EventType`, `bus.Event{Type, StageID, Data, Seq}`, все константы `bus.Event*` (`EventStageStatusChanged`, `EventAgentAction`, `EventAgentCompleted`, `EventApproved`, `EventRetryScheduled`, `EventRetryExhausted`, `EventAskUser`, `EventUserAnswered`, `EventSupervisorDecision`, `EventContextWarning`, `EventScriptOutput`, `EventHookFailed`, `EventHookResolved`)
- `bus.CriticalBus`, `bus.NewCriticalBus(buf int) *bus.CriticalBus`, `.Publish(ctx, ev) error`, `.Recv() <-chan bus.Event`
- `bus.UIBus`, `bus.NewUIBus() *bus.UIBus`, `.Subscribe`, `.Unsubscribe`, `.Publish`, `.DroppedCount`, `.SubscriberDroppedCount`
- `bus.FSMEvent`, все константы `bus.Ev*` (`EvStartPlanning`, `EvPlanReady`, `EvApprove`, `EvRevise`, `EvStartRun`, `EvComplete`, `EvFail`, `EvAskUser`, `EvUserAnswered`, `EvScheduleRetry`, `EvResumeAfterRetry`, `EvManualRetry`, `EvBlockedByDep`, `EvReady`, `EvSupervisorApproved`, `EvHookFailed`, `EvHookResolved`)
- `bus.GuardCtx`, `bus.FSM`, `bus.NewFSM(store *state.Store) *bus.FSM`, `.Apply(...)`, `bus.IsTerminal(s state.StageStatus) bool`
- Produces (новое): `(*bus.CriticalBus).TryPublish(ev bus.Event) bool` — неблокирующий best-effort send, `false` если буфер полон
- Produces (новое): `(*bus.CriticalBus).WakeEventLoop() bool` — обёртка над `TryPublish` с внутренним маркер-событием (замена текущего прямого доступа к приватному `ch` из `concurrency.go`)

- [ ] **Step 1: Переместить файлы**

```bash
mkdir -p pkg/orchestrator/bus
git mv pkg/orchestrator/bus.go pkg/orchestrator/bus/bus.go
git mv pkg/orchestrator/bus_test.go pkg/orchestrator/bus/bus_test.go
git mv pkg/orchestrator/fsm.go pkg/orchestrator/bus/fsm.go
git mv pkg/orchestrator/fsm_test.go pkg/orchestrator/bus/fsm_test.go
```

- [ ] **Step 2: Поменять package-декларацию в 4 файлах**

`package orchestrator` → `package bus` в каждом.

- [ ] **Step 3: Добавить TryPublish и WakeEventLoop, написать тест на TryPublish (TDD)**

Сначала тест в `pkg/orchestrator/bus/bus_test.go`:
```go
func TestCriticalBus_TryPublish_DropsWhenFull(t *testing.T) {
	b := NewCriticalBus(1)
	if !b.TryPublish(Event{Type: EventAgentCompleted}) {
		t.Fatal("first TryPublish on empty buffer should succeed")
	}
	if b.TryPublish(Event{Type: EventAgentCompleted}) {
		t.Fatal("TryPublish on full buffer should return false, not block")
	}
	<-b.Recv()
	if !b.TryPublish(Event{Type: EventAgentCompleted}) {
		t.Fatal("TryPublish should succeed again after buffer drains")
	}
}

func TestCriticalBus_WakeEventLoop_PublishesInternalMarker(t *testing.T) {
	b := NewCriticalBus(1)
	if !b.WakeEventLoop() {
		t.Fatal("WakeEventLoop on empty buffer should succeed")
	}
	ev := <-b.Recv()
	if ev.Type != eventAgentDrained {
		t.Fatalf("want eventAgentDrained, got %q", ev.Type)
	}
}
```

- [ ] **Step 4: Прогнать тест, убедиться что падает (методов ещё нет)**

```bash
cd pkg/orchestrator/bus && go test -run "TestCriticalBus_TryPublish|TestCriticalBus_WakeEventLoop" -v
```
Ожидание: FAIL, `TryPublish`/`WakeEventLoop` не определены.

- [ ] **Step 5: Реализовать TryPublish и WakeEventLoop в bus.go**

В `pkg/orchestrator/bus/bus.go`, сразу после `func (b *CriticalBus) Publish(...)`:
```go
// TryPublish публикует событие неблокирующим best-effort send; возвращает
// false, если буфер шины полон — вызывающий код тогда просто теряет толчок
// (см. WakeEventLoop).
func (b *CriticalBus) TryPublish(ev Event) bool {
	select {
	case b.ch <- ev:
		return true
	default:
		return false
	}
}

// WakeEventLoop будит select Run()'а неблокирующей отправкой внутреннего
// маркер-события — используется concurrency.Manager.WakeEventLoop после
// того, как after-hook горутина завершилась без движения FSM (script_after
// никогда не публикует EventAgentCompleted сама). Best-effort: если буфер
// полон, там уже стоят другие события — их обработка и так вызовет
// перепроверку состояния, потеря толчка безвредна.
func (b *CriticalBus) WakeEventLoop() bool {
	return b.TryPublish(Event{Type: eventAgentDrained})
}
```

- [ ] **Step 6: Прогнать тест, убедиться что проходит**

```bash
cd pkg/orchestrator/bus && go test -run "TestCriticalBus_TryPublish|TestCriticalBus_WakeEventLoop" -v
```
Ожидание: PASS.

- [ ] **Step 7: Поправить orchestrator.go**

Добавить импорт `"github.com/akopichin/afm/pkg/orchestrator/bus"`.

Поля структуры `Orchestrator`:
```go
critical *bus.CriticalBus
ui       *bus.UIBus
fsm      *bus.FSM
```

В `New()`:
```go
critical: bus.NewCriticalBus(...)   // там где сейчас NewCriticalBus(...)
ui:       bus.NewUIBus()
fsm:      bus.NewFSM(opts.Store)
```

Все обращения к `EvXxx`, `EventXxx`, `GuardCtx{...}`, `FSMEvent`, `IsTerminal(...)` в этом файле — добавить префикс `bus.` (например `o.Trigger(s.ID, EvComplete, GuardCtx{}, "")` → `o.Trigger(s.ID, bus.EvComplete, bus.GuardCtx{}, "")`).

- [ ] **Step 8: Поправить остальные файлы ядра — идентификатор за идентификатором**

В каждом из `agents.go`, `control_api.go`, `dialog_poller.go`, `hooks.go`, `recovery.go`, `retry.go`, `scheduling.go`, `supervisor_track.go`:
1. Добавить импорт `"github.com/akopichin/afm/pkg/orchestrator/bus"` (если файл использует хоть один из идентификаторов ниже).
2. Добавить префикс `bus.` перед каждым использованием: `EventType`, `Event{`, `EventStageStatusChanged`, `EventAgentAction`, `EventAgentCompleted`, `EventApproved`, `EventRetryScheduled`, `EventRetryExhausted`, `EventAskUser`, `EventUserAnswered`, `EventSupervisorDecision`, `EventContextWarning`, `EventScriptOutput`, `EventHookFailed`, `EventHookResolved`, `CriticalBus`, `UIBus`, `FSMEvent`, `GuardCtx{`, `FSM`, `EvStartPlanning`, `EvPlanReady`, `EvApprove`, `EvRevise`, `EvStartRun`, `EvComplete`, `EvFail`, `EvAskUser`, `EvUserAnswered`, `EvScheduleRetry`, `EvResumeAfterRetry`, `EvManualRetry`, `EvBlockedByDep`, `EvReady`, `EvSupervisorApproved`, `EvHookFailed`, `EvHookResolved`, `IsTerminal(`.

Практический способ: после Step 7 прогнать `go build ./pkg/orchestrator/...` — компилятор укажет каждую строку с неопределённым идентификатором (`undefined: EvFail` и т.п.). Пройти по списку ошибок файл за файлом, добавляя `bus.` и импорт — это гарантирует, что ни одно использование не пропущено (полнее, чем ручной grep).

- [ ] **Step 9: Поправить тесты ядра тем же методом**

Для `approve_test.go`, `fatal_test.go`, `hooks_test.go`, `runctx_test.go`, `shutdown_test.go`, `agent_suggest_race_test.go`, `auto_approve_test.go`, `integration_interactive_test.go`, `integration_hooks_test.go`, `poller_test.go`, `recovery_hooks_test.go`, `scenario_test.go`: тот же приём — `go vet ./pkg/orchestrator/...` или `go test ./pkg/orchestrator/... -run NONE` покажет неопределённые идентификаторы построчно; добавить импорт `bus` и префиксы.

Отдельно для `approve_test.go`, `fatal_test.go`, `hooks_test.go`, `runctx_test.go`, `shutdown_test.go` (конструируют `&Orchestrator{...}` напрямую) — поля `graph:`, `fsm:`, `ui:`, `critical:` должны использовать новые конструкторы:
```go
graph:    graph.NewGraph(stages),      // если ещё не поправлено в Task 1
fsm:      bus.NewFSM(store),
ui:       bus.NewUIBus(),
critical: bus.NewCriticalBus(16),
```

- [ ] **Step 10: Поправить pkg/server/server.go**

Добавить импорт `"github.com/akopichin/afm/pkg/orchestrator/bus"`.
Заменить оба вхождения `*orchestrator.UIBus` на `*bus.UIBus` (поле структуры `Server` и поле в `Options`).

- [ ] **Step 11: Поправить pkg/server/websocket.go**

Добавить импорт `bus`. В `writePump(conn *websocket.Conn, id uint64, ch <-chan orchestrator.Event, done <-chan struct{})` заменить `orchestrator.Event` → `bus.Event`.

- [ ] **Step 12: Собрать весь проект и прогнать тесты**

```bash
go build ./... 2>&1 | head -100
go test ./pkg/orchestrator/... ./pkg/orchestrator/bus/... ./pkg/server/... -v 2>&1 | tail -150
```
Ожидание: компилируется, все тесты (кроме двух новых из Step 3) зелёные без изменения поведения.

- [ ] **Step 13: Коммит**

```bash
git add -A pkg/orchestrator/ pkg/server/
git commit -m "refactor(orchestrator): выносим CriticalBus/UIBus/FSM в pkg/orchestrator/bus"
```

---

### Task 5: Вынести `pkg/orchestrator/concurrency`

**Зависит от Задачи 4** (использует `*bus.CriticalBus`).

**Files:**
- Create: `pkg/orchestrator/concurrency/concurrency.go` (новый тип `Manager`, логика из `pkg/orchestrator/concurrency.go` + логика построения `sems` из `orchestrator.go:195-226`)
- Create: `pkg/orchestrator/concurrency/concurrency_test.go` (новый файл — у `concurrency.go` не было отдельного теста, поведение проверялось косвенно через `approve_test.go`/`shutdown_test.go`; здесь пишем прямой unit-тест на `Manager`)
- Delete: `pkg/orchestrator/concurrency.go`
- Modify: `pkg/orchestrator/orchestrator.go` (поле `sems`+бизнес-логика построения семафоров заменяются на `concurrency *concurrency.Manager`)
- Modify: `pkg/orchestrator/agents.go`, `control_api.go`, `hooks.go`, `recovery.go`, `scheduling.go` (вызовы `o.spawnAgent` → `o.concurrency.SpawnAgent`)
- Modify: `pkg/orchestrator/control_api.go` (вызов `o.isAgentActive` → `o.concurrency.IsActive`)
- Modify: `pkg/orchestrator/hooks.go` (вызов `o.wakeEventLoop` → `o.concurrency.WakeEventLoop`)
- Modify: `pkg/orchestrator/approve_test.go`, `runctx_test.go`, `shutdown_test.go` (прямая конструкция `Orchestrator{sems: ...}` → `Orchestrator{concurrency: ...}`)

**Interfaces:**
- Produces:
  - `concurrency.Semaphore` (интерфейс, неэкспортированные методы `acquire()`/`release()` — реализуется только типами внутри пакета `concurrency`)
  - `concurrency.ChannelSemaphore` (экспортированный `type ChannelSemaphore chan struct{}`, реализует `Semaphore`) — нужен тестам ядра для конструирования блокирующего семафора напрямую через `make(concurrency.ChannelSemaphore, N)` и обычные операции с каналом (`<-`)
  - `concurrency.New(critical *bus.CriticalBus, stages []flow.Stage, defaultCommand string, globalMaxParallel int) *concurrency.Manager` — строит sems-карту из конфига стадий (логика 1:1 с текущей `orchestrator.go:195-226`)
  - `concurrency.NewWithSemaphores(critical *bus.CriticalBus, sems map[string]concurrency.Semaphore, defaultCommand string) *concurrency.Manager` — для тестов, которым нужен прямой контроль над семафорами
  - `(*concurrency.Manager).SpawnAgent(ctx context.Context, s flow.Stage, run func(context.Context, flow.Stage))`
  - `(*concurrency.Manager).IsActive(stageID string) bool`
  - `(*concurrency.Manager).WaitAgents()`
  - `(*concurrency.Manager).WakeEventLoop()`

- [ ] **Step 1: Написать concurrency.go в новом пакете (с построением sems внутри New)**

Создать `pkg/orchestrator/concurrency/concurrency.go`:
```go
package concurrency

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
)

// Semaphore — интерфейс командного семафора. Неэкспортированные методы:
// реализуется только типами этого пакета (noopSemaphore, ChannelSemaphore).
type Semaphore interface {
	acquire()
	release()
}

// noopSemaphore — семафор-заглушка для MaxParallel=0 (без ограничения).
type noopSemaphore struct{}

func (noopSemaphore) acquire() {}
func (noopSemaphore) release() {}

// ChannelSemaphore — реальный семафор на буферизованном канале. Экспортирован,
// чтобы тесты ядра (pkg/orchestrator) могли собрать блокирующий семафор для
// точного контроля таймингов через NewWithSemaphores (см.
// TestRevise_DurableTransition в approve_test.go).
type ChannelSemaphore chan struct{}

func (s ChannelSemaphore) acquire() { s <- struct{}{} }
func (s ChannelSemaphore) release() { <-s }

// agentDrainTimeout — сколько ждём завершения агентских горутин на выходе
// Run, прежде чем вернуться (агентские процессы уже убиты отменой ctx;
// ожидание защищает Store от использования после Close).
const agentDrainTimeout = 10 * time.Second

// Manager инкапсулирует конкурентность агентских горутин: семафоры на
// команду, учёт активных стадий, WaitGroup для чистого shutdown.
type Manager struct {
	critical     *bus.CriticalBus
	sems         map[string]Semaphore
	defaultCmd   string
	activeAgents sync.Map
	agentWG      sync.WaitGroup
}

// New строит Manager с семафорами на команду из конфигурации стадий:
// per-stage MaxParallel имеет приоритет над globalMaxParallel; MaxParallel<=0
// означает отсутствие ограничения (noopSemaphore).
func New(critical *bus.CriticalBus, stages []flow.Stage, defaultCommand string, globalMaxParallel int) *Manager {
	limits := make(map[string]int)
	cmds := make(map[string]bool)
	for _, s := range stages {
		cmd := s.Command
		if cmd == "" {
			cmd = defaultCommand
		}
		cmds[cmd] = true
		if s.MaxParallel <= 0 {
			continue
		}
		if cur, ok := limits[cmd]; !ok || s.MaxParallel < cur {
			limits[cmd] = s.MaxParallel
		}
	}
	sems := make(map[string]Semaphore)
	for cmd := range cmds {
		mp, ok := limits[cmd]
		if !ok {
			mp = globalMaxParallel
		}
		if mp > 0 {
			sems[cmd] = ChannelSemaphore(make(chan struct{}, mp))
		} else {
			sems[cmd] = noopSemaphore{}
		}
	}
	return &Manager{critical: critical, sems: sems, defaultCmd: defaultCommand}
}

// NewWithSemaphores строит Manager с готовой картой семафоров — используется
// тестами, которым нужен прямой контроль над блокировкой (см. ChannelSemaphore).
func NewWithSemaphores(critical *bus.CriticalBus, sems map[string]Semaphore, defaultCommand string) *Manager {
	return &Manager{critical: critical, sems: sems, defaultCmd: defaultCommand}
}

// markActive/markDone/semFor остаются приватными методами — вызываются только
// изнутри SpawnAgent.

func (m *Manager) markActive(stageID string) { m.activeAgents.Store(stageID, struct{}{}) }
func (m *Manager) markDone(stageID string)   { m.activeAgents.Delete(stageID) }

// IsActive сообщает, выполняется ли сейчас агентская горутина для стадии.
func (m *Manager) IsActive(stageID string) bool {
	_, ok := m.activeAgents.Load(stageID)
	return ok
}

func (m *Manager) semFor(s flow.Stage) Semaphore {
	cmd := s.Command
	if cmd == "" {
		cmd = m.defaultCmd
	}
	if sem, ok := m.sems[cmd]; ok {
		return sem
	}
	return noopSemaphore{}
}

// SpawnAgent запускает агентскую горутину под семафором команды, помечает
// стадию активной и учитывает горутину в WaitGroup. Единственная точка
// запуска — заменяет ~10 копий одинакового boilerplate и гарантирует чистый
// shutdown.
func (m *Manager) SpawnAgent(ctx context.Context, s flow.Stage, run func(context.Context, flow.Stage)) {
	m.agentWG.Add(1)
	go func() {
		defer m.agentWG.Done()
		sem := m.semFor(s)
		sem.acquire()
		m.markActive(s.ID)
		defer func() {
			m.markDone(s.ID)
			sem.release()
		}()
		run(ctx, s)
	}()
}

// WakeEventLoop будит select Run()'а неблокирующей отправкой внутреннего
// маркер-события через bus.CriticalBus.WakeEventLoop — используется
// maybeRunAfterHook после того, как after-hook горутина реально завершилась,
// т.к. script_after никогда не публикует EventAgentCompleted сама (не трогает
// FSM), так что без явного толчка Run() мог бы простаивать в select.
func (m *Manager) WakeEventLoop() {
	m.critical.WakeEventLoop()
}

// WaitAgents дожидается завершения всех агентских горутин (с ограничением),
// чтобы Run не вернулся, пока горутины ещё пишут в Store.
func (m *Manager) WaitAgents() {
	done := make(chan struct{})
	go func() {
		m.agentWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(agentDrainTimeout):
		log.Printf("WARN: agent drain timed out after %v", agentDrainTimeout)
	}
}
```

**Ловушка при переносе:** не добавляй импорт `"sync/atomic"` в этот файл. `pendingAfterHooks` (тип `atomic.Int32`) остаётся в `hooks.go` в ядре и НЕ переезжает сюда — `concurrency.go` использует только `sync.Map`/`sync.WaitGroup`, `atomic` ему не нужен.

- [ ] **Step 2: Написать unit-тест на Manager**

Создать `pkg/orchestrator/concurrency/concurrency_test.go`:
```go
package concurrency

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
)

func TestSpawnAgent_TracksActiveAndWaitGroup(t *testing.T) {
	m := New(bus.NewCriticalBus(16), nil, "", 0)
	var ran atomic.Bool
	done := make(chan struct{})
	m.SpawnAgent(context.Background(), flow.Stage{ID: "a"}, func(ctx context.Context, s flow.Stage) {
		if !m.IsActive("a") {
			t.Error("stage should be marked active while agent runs")
		}
		ran.Store(true)
		close(done)
	})
	<-done
	m.WaitAgents()
	if !ran.Load() {
		t.Fatal("agent function did not run")
	}
	if m.IsActive("a") {
		t.Fatal("stage should not be active after agent completes")
	}
}

func TestSpawnAgent_BlocksOnFullSemaphore(t *testing.T) {
	blockSem := ChannelSemaphore(make(chan struct{}, 1))
	blockSem <- struct{}{} // занят
	m := NewWithSemaphores(bus.NewCriticalBus(16), map[string]Semaphore{"": blockSem}, "")
	var ran atomic.Bool
	m.SpawnAgent(context.Background(), flow.Stage{ID: "a"}, func(ctx context.Context, s flow.Stage) {
		ran.Store(true)
	})
	time.Sleep(20 * time.Millisecond)
	if ran.Load() {
		t.Fatal("agent should be blocked on full semaphore")
	}
	<-blockSem // отпускаем
	m.WaitAgents()
	if !ran.Load() {
		t.Fatal("agent should run after semaphore released")
	}
}

func TestWakeEventLoop_PublishesToBus(t *testing.T) {
	cb := bus.NewCriticalBus(1)
	m := New(cb, nil, "", 0)
	m.WakeEventLoop()
	select {
	case <-cb.Recv():
	default:
		t.Fatal("WakeEventLoop should publish to critical bus")
	}
}
```

- [ ] **Step 3: Прогнать новые тесты**

```bash
cd pkg/orchestrator/concurrency && go test -v
```
Ожидание: PASS для всех трёх.

- [ ] **Step 4: Удалить старый concurrency.go**

```bash
git rm pkg/orchestrator/concurrency.go
```

- [ ] **Step 5: Поправить orchestrator.go**

Добавить импорт `"github.com/akopichin/afm/pkg/orchestrator/concurrency"`.

Убрать поле `sems map[string]interface{ acquire(); release() }`, добавить:
```go
concurrency *concurrency.Manager
```

В `New()` убрать весь блок построения `sems` (строки, эквивалентные текущим 195-226 — вычисление `limits`/`cmds`/цикл построения `sems`), заменить на:
```go
conc := concurrency.New(critical, opts.Stages, opts.Config.Client.Command, opts.Config.Executor.MaxParallel)
```
В финальном `return &Orchestrator{...}` заменить `sems: sems,` на `concurrency: conc,`.

Как и с `supervisor` в Задаче 2: поле `Orchestrator.concurrency` и импортированный пакет `concurrency` теперь одноимённые в `orchestrator.go` — это валидный Go (поле доступно как `o.concurrency`, пакет — как `concurrency.Xxx`, разные namespace'ы), просто не удивляйся при чтении файла.

- [ ] **Step 6: Поправить call site'ы в ядре**

Во всех файлах, где встречается `o.spawnAgent(` (`agents.go`, `control_api.go`, `hooks.go`, `recovery.go`, `scheduling.go`, `orchestrator.go`) — заменить на `o.concurrency.SpawnAgent(`.

В `control_api.go` — заменить `o.isAgentActive(` на `o.concurrency.IsActive(`.

В `hooks.go` — заменить `o.wakeEventLoop()` на `o.concurrency.WakeEventLoop()`.

В `orchestrator.go` — заменить `o.waitAgents()` на `o.concurrency.WaitAgents()`.

- [ ] **Step 7: Поправить approve_test.go**

Строки 94-106 (конструкция `Orchestrator{...}` с `blockSem`/`sems`):
```go
cb := bus.NewCriticalBus(16)
blockSem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
blockSem <- struct{}{} // занят: следующий acquire() (в SpawnAgent) заблокируется
o := &Orchestrator{
	opts:        Options{RunDir: dir, Stages: stages, Store: store},
	graph:       graph.NewGraph(stages),
	runner:      noopPlanningRunner{},
	fsm:         bus.NewFSM(store),
	ui:          bus.NewUIBus(),
	critical:    cb,
	concurrency: concurrency.NewWithSemaphores(cb, map[string]concurrency.Semaphore{"": blockSem}, ""),
}
```
**Критично:** `critical: cb` и `concurrency.NewWithSemaphores(cb, ...)` должны получить ОДИН И ТОТ ЖЕ экземпляр `*bus.CriticalBus` — иначе `WakeEventLoop`/события конкурентности пойдут в другую шину, чем та, которую слушает основной event loop теста.

Далее в этом же тесте: `o.waitAgents()` → `o.concurrency.WaitAgents()`.

Добавить импорт `"github.com/akopichin/afm/pkg/orchestrator/concurrency"`.

- [ ] **Step 8: Поправить runctx_test.go**

Аналогично: заменить
```go
sems: map[string]interface {
	acquire()
	release()
}{},
```
на добавление поля
```go
concurrency: concurrency.NewWithSemaphores(cb, map[string]concurrency.Semaphore{}, ""),
```
где `cb` — та же переменная `*bus.CriticalBus`, что передана в поле `critical:` этого же struct-литерала (завести переменную `cb := bus.NewCriticalBus(16)` перед литералом, если её ещё нет, и использовать `critical: cb` вместо инлайн-вызова).

- [ ] **Step 9: Поправить shutdown_test.go**

В обоих тестах (`TestSpawnAgent_WaitAgentsBlocksUntilDone`, `TestRun_CancelDrainsAgents`):
```go
cb := bus.NewCriticalBus(16)
o := &Orchestrator{
	ui:          bus.NewUIBus(),
	critical:    cb,
	concurrency: concurrency.NewWithSemaphores(cb, map[string]concurrency.Semaphore{}, ""),
}
```
Заменить `o.spawnAgent(...)` → `o.concurrency.SpawnAgent(...)`, `o.waitAgents()` → `o.concurrency.WaitAgents()`.

- [ ] **Step 10: Собрать весь проект и прогнать полный test suite**

```bash
go build ./... 2>&1 | head -100
go test ./... -race 2>&1 | tail -200
```
Ожидание: всё зелёное. Это финальный шаг Tier 1 + Tier 2 — полный прогон, а не только `pkg/orchestrator`, т.к. `pkg/server` тоже менялся в Task 4.

- [ ] **Step 11: Коммит**

```bash
git add -A pkg/orchestrator/
git commit -m "refactor(orchestrator): выносим Concurrency-manager в pkg/orchestrator/concurrency"
```

---

### Task 6: Контрольный прогон strictacode и фиксация результата

**Files:**
- Нет изменений кода — только верификация эффекта и запись результата в спеку.

**Interfaces:** нет (аналитический шаг).

- [ ] **Step 1: Прогнать strictacode на чистом снапшоте**

```bash
SCRATCH=$(mktemp -d)
git archive HEAD | tar -x -C "$SCRATCH"
strictacode analyze "$SCRATCH" --details --format json --top-packages 5 --top-modules 5 --top-classes 10 --top-methods 15 --top-functions 15 > /tmp/strictacode-after.json
```

- [ ] **Step 2: Сравнить overengineering_pressure пакета orchestrator до/после**

Открыть `/tmp/strictacode-after.json`, найти `packages[].name == "orchestrator"`, сравнить `overengineering_pressure.score` с исходным 91 (из `docs/superpowers/specs/2026-08-06-orchestrator-package-split-design.md`).

- [ ] **Step 3: Дописать результат в спеку**

В `docs/superpowers/specs/2026-08-06-orchestrator-package-split-design.md` добавить в конец короткий раздел:
```markdown
## Результат (после реализации)

overengineering_pressure пакета orchestrator: 91 → <новое значение>.
<Одно предложение — эффект соответствует ожиданиям / слабее ожидаемого,
дальше не продолжаем (см. Global Constraints).>
```

- [ ] **Step 4: Финальный коммит**

```bash
git add docs/superpowers/specs/2026-08-06-orchestrator-package-split-design.md
git commit -m "docs: фиксируем результат strictacode после разбиения pkg/orchestrator"
```
