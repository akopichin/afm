# Архитектурный обзор проекта afm

Проект в целом уже неплохо разложен по пакетам и хорошо покрыт тестами. Главная архитектурная проблема сейчас не в отсутствии слоёв, а в том, что runtime-модель распределена между event log, файловыми маркерами, конфигурацией flow и внутренними кешами оркестратора. Из-за этого новые сценарии требуют синхронных изменений сразу в нескольких местах.

## Что уже сделано хорошо

- `pkg/flow`, `pkg/state`, `pkg/prompts`, `pkg/docker` имеют понятные зоны ответственности.
- `events.jsonl` выбран источником истины, есть replay, CAS-проверки, fsync и блокировка run-директории.
- FSM отделена от event loop в `orchestrator/bus`.
- Исполнение абстрагировано через `executor.Runner`, поэтому сценарии можно тестировать без реальных subprocess.
- Есть многочисленные интеграционные тесты восстановления, retry, shutdown и конкурентных сценариев.
- Dashboard разбит на компоненты и hooks, а входящий JSON нормализуется в одном месте.
- В репозиторий не закоммичен `node_modules`.

## 1. Ввести полноценную доменную модель Run/Stage

Это самое важное улучшение.

Сейчас состояние стадии складывается из разных источников:

- статус — `events.jsonl` через `state.Store`;
- имя — текущий `flow.yaml`;
- `interactive` и `auto_approve` — конфигурация сервера;
- `autonomous` — наличие `autonomous.flag`;
- наличие диалога — сканирование `*.dialog.jsonl`;
- текущая фаза вопроса — `preAskPhase sync.Map`;
- исполняющийся агент — `interruptChans sync.Map`;
- состояние hook — FSM плюс `hookWaiters`.

Это хорошо видно в формировании HTTP-ответа: `pkg/server/handlers.go` строит одну стадию одновременно из snapshot, нескольких карт и файловой системы.

Предлагается ввести read model:

```go
type RunView struct {
    ID          RunID
    Definition  FlowDefinition
    Stages      []StageView
    LastEventID uint64
}

type StageView struct {
    ID           StageID
    Name         string
    Status       StageStatus
    Phase        Phase
    Mode         StageMode
    Capabilities StageCapabilities
    UpdatedAt    time.Time
}
```

Её должен строить отдельный `RunProjection` из событий и зафиксированного определения flow. HTTP, CLI `check`, scheduler и dashboard будут читать одну и ту же модель.

Особенно важно сохранять копию нормализованного flow в run-директории при создании запуска. Сейчас resume частично зависит от текущего flow-файла, который пользователь мог изменить.

## 2. Разделить Orchestrator на координирующие сервисы

`Orchestrator` владеет FSM, event buses, scheduler, retry, supervisor, hooks, диалогами, polling, recovery, subprocess interruption и lifecycle-контекстами. Наличие нескольких `sync.Map`, mutex, atomic-счётчика и специальных кешей в одном объекте — признак слишком широкой ответственности.

При этом сложность уже распределена по нескольким крупным файлам:

- `agents.go` — 558 строк;
- `orchestrator.go` — 494;
- `hooks.go` — 455;
- `dialog_poller.go` — 396;
- `scheduling.go` — 385;
- `recovery.go` — 275.

Полезная целевая декомпозиция:

- `RunCoordinator` — принимает команды и координирует lifecycle;
- `StageScheduler` — зависимости и готовность стадий;
- `StageWorker` — выполнение одной стадии;
- `AgentSession` — один subprocess, interrupt и retry;
- `HookRunner` — before/after hooks и их решения;
- `DialogService` — вопросы, ответы и protocol violations;
- `RecoveryPlanner` — преобразование восстановленного состояния в команды;
- `SupervisorPolicy` — выбор autonomous/regular track.

Координатор после этого не должен знать о `*.jsonl`, именах файлов, wrapper directory и деталях subprocess. Он должен обмениваться типизированными командами и результатами.

Разделять лучше по поведению, а не просто переносить методы в новые файлы: текущий пакет уже разбит по файлам, но продолжает разделять один большой mutable object.

## 3. Свести события, команды и статусы в одно доменное ядро

Сейчас FSM находится внутри `pkg/orchestrator/bus`, но использует типы `pkg/state` и `pkg/flow`. В результате пакет с названием `bus` одновременно отвечает за транспорт событий и правила доменных переходов.

Более ясная структура:

```text
pkg/domain
  run.go
  stage.go
  phase.go
  command.go
  event.go
  reducer.go

pkg/runstore
  eventlog.go
  projection.go

pkg/orchestrator
  coordinator.go
  handlers.go

pkg/eventbus
  critical.go
  ui.go
```

`StageStatus`, `Phase`, terminal semantics и transition rules должны жить вместе в `domain`. Хранилище должно лишь атомарно append-ить события и восстанавливать projection.

Это также устранит ручную синхронизацию статусов между Go и TypeScript. Сейчас полный список отдельно определён во frontend в `pkg/web/dashboard/src/types/stage.ts`. Следующее добавление статуса потребует обновить backend FSM, JSON API, TS union, labels, active statuses и UI-условия.

Практичный вариант — OpenAPI/JSON Schema плюс генерация TypeScript DTO. Генерировать весь Go-домен не обязательно.

## 4. Убрать callback-конструктор HTTP-сервера

`server.Config` содержит восемь callback-полей для approve, revise, retry, hooks и dialog. Это делает сервер вручную собранным адаптером к внутреннему API оркестратора и усложняет добавление каждой новой операции.

Лучше дать серверу два узких интерфейса:

```go
type RunQueryService interface {
    Status(context.Context) (RunView, error)
    Events(context.Context, EventCursor) ([]Event, error)
    StagePlan(context.Context, StageID) (Document, error)
    StageLog(context.Context, StageID) (Log, error)
}

type RunCommandService interface {
    Execute(context.Context, RunCommand) error
}
```

Тогда HTTP-слой будет отвечать только за:

1. декодирование HTTP;
2. вызов use case;
3. преобразование результата в DTO;
4. код ответа.

Он перестанет напрямую сканировать run-директорию и импортировать внутренний event bus.

Заодно ошибки вроде invalid transition, stale command и storage failure стоит представить типизированными application errors и централизованно переводить в HTTP-коды.

## 5. Выделить RunRepository вместо прямого доступа к файлам

Работа с run-директорией сейчас распределена по `state`, `orchestrator`, `server`, `mcp`, `stagefiles` и CLI. Например, status endpoint делает `os.Stat` для каждой стадии и каждой возможной фазы.

Нужен единый facade:

```go
type RunRepository interface {
    AppendEvent(context.Context, RunEvent) error
    Snapshot(context.Context) (RunProjection, error)
    ReadArtifact(context.Context, ArtifactRef) ([]byte, error)
    ReadLog(context.Context, StageID) (io.ReadCloser, error)
    Dialog(context.Context, StageID) (DialogHistory, error)
}
```

Это даст:

- единую политику безопасных путей;
- централизованную атомарную запись;
- возможность кешировать derived metadata;
- более простые тесты без знания имён файлов;
- возможность позже сменить layout run-директории;
- устранение скрытых источников истины вроде `autonomous.flag`.

Формат файлов при этом можно полностью сохранить — речь об инкапсуляции, а не о переходе на базу данных.

## 6. Сделать `cmd/afm` чистым composition root

Команда `run` сейчас одновременно:

- загружает и переопределяет конфигурацию;
- парсит flow;
- решает, нужен ли Docker re-exec;
- выбирает секреты и mounts;
- создаёт wrapper scripts;
- конфигурирует supervisor;
- вычисляет root directory;
- собирает orchestrator и server;
- управляет браузером и shutdown.

В результате главный сценарий трудно использовать не из Cobra — например, из другого бинарника или тестового harness.

Предлагается:

```go
type RunApplication struct { ... }

func BuildRunApplication(ctx context.Context, req RunRequest) (*RunApplication, error)
func (a *RunApplication) Start(ctx context.Context) error
```

Отдельно:

- `EffectiveConfigResolver`;
- `DockerBootstrapper`;
- `AgentRuntimeFactory`;
- `RunApplication`.

Cobra-команда должна только собрать `RunRequest`, вызвать application service и вывести результат.

## 7. Разделить executor на transport adapters

`pkg/executor/executor.go` содержит 666 строк и одновременно занимается:

- запуском процессов;
- Claude stream-json parsing;
- логированием;
- определением ошибок;
- idle timeout;
- interrupt/kill;
- transcript/debug output;
- tool-action formatting;
- Claude-specific arguments.

При этом проект поддерживает Claude, OpenAI-compatible, Cursor и Codex через wrapper-совместимость. Такой подход удобен, но протокол Claude фактически становится неявным внутренним SPI.

Стоит сделать его явным:

```go
type AgentAdapter interface {
    BuildInvocation(AgentRequest) Invocation
    DecodeEvent([]byte) (AgentEvent, error)
}

type ProcessRunner interface {
    Run(context.Context, Invocation, EventSink) error
}
```

Реализации: `ClaudeStreamAdapter`, `CodexAdapter`, `CursorAdapter`. Даже если wrappers останутся, эта граница изолирует protocol-specific parsing и позволит тестировать process lifecycle отдельно от JSON.

## 8. Упростить frontend-контракт и состояние

`App.tsx` сейчас одновременно является composition root, контроллером команд, автоматом выбора стадии и местом бизнес-условий отображения. Уже появляется sentinel `NO_STAGE`, который намеренно провоцирует запрос `/api/stages//plan`. Это рабочий обход, но он показывает, что контракты компонентов требуют фиктивную сущность.

Рекомендации:

- панели должны принимать `Stage | null`;
- сетевые команды вынести в `api/run-client.ts`;
- серверу отдавать массив готовых `StageView`, а не `stages` плюс пять параллельных maps;
- `showPlan`, `showDialog`, attention и допустимые actions желательно вычислять на backend как capabilities;
- выделить `useStageSelection(stages)` из `App`;
- использовать query/cache слой либо небольшой собственный store, чтобы polling и websocket не управлялись независимо.

Сейчас frontend вручную склеивает параллельные maps в `use-status.ts`, что повторяет серверную композицию и делает контракт хрупким.

## Рекомендуемый порядок

1. Зафиксировать `RunDefinition` внутри run-директории и ввести `RunView/StageView`.
2. Сделать `RunRepository` и перенести туда весь доступ к структуре run-файлов.
3. Перевести server и CLI status на единый query service.
4. Вынести доменные event/command/FSM из `orchestrator/bus`.
5. Разделить `Orchestrator` на coordinator, stage worker, dialog, hooks и recovery.
6. Превратить `cmd/afm/run.go` в composition root.
7. После стабилизации backend API сгенерировать TypeScript DTO и упростить frontend.
8. Последним разделить executor adapters — это полезно, но рискованнее и менее срочно, чем консолидация runtime-модели.

Не стоит начинать с массовой перестановки пакетов. Первый безопасный вертикальный шаг — добавить `RunQueryService`, заставить `/api/status` получать готовый `RunView`, а затем постепенно перенести построение этой модели из `server` в projection/repository.

## Проверка проекта

- Frontend: 26 test files, 214 тестов прошли.
- В тесте reconnect event feed есть два предупреждения React `act(...)`.
- Go-тесты локально не удалось запустить из-за toolchain/cache sandbox: модуль требует Go 1.26.4, локальный `GOTOOLCHAIN=local` видит 1.25.3, а загрузка toolchain заблокирована сетью.
