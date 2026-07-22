# S2 — Split orchestrator.go God-File Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Разнести god-file `pkg/orchestrator/orchestrator.go` (~1625 строк, ~54 функции) по сфокусированным файлам одного пакета, чтобы в `orchestrator.go` осталось только ядро (struct/New/Run/event-loop/Trigger). Снижает когнитивную нагрузку для всех дальнейших правок.

**Architecture:** ЧИСТОЕ перемещение кода (code motion) внутри пакета `orchestrator`. Все декларации остаются в `package orchestrator`, поэтому перемещение функции в новый файл НЕ меняет ни одной ссылки — компилятор гарантирует корректность. Тела функций, комментарии и сигнатуры переносятся БАЙТ-В-БАЙТ, без изменений. Никакой логики не трогаем (переименования/дедуп/интерфейс семафора — это S3/Tier 4, НЕ здесь).

**Tech Stack:** Go, пакет `pkg/orchestrator`. Проверка — компилятор + существующие тесты (4757 строк тестов пакета).

## Global Constraints
- НЕ менять версию Go в go.mod.
- Коммиты на русском языке, без Co-Authored-By.
- ЧИСТОЕ перемещение: НИ ОДНА строка тела/сигнатуры/комментария не меняется; только файл, где декларация живёт. Никаких «заодно» правок.
- После каждой задачи: `go build ./...`, `go vet ./pkg/orchestrator/...`, `go test ./pkg/orchestrator/...` — всё зелёное.
- Новые файлы начинаются с `package orchestrator` и необходимого import-блока (только реально используемые импорты; `goimports`/компилятор подскажет).
- НЕ трогать `accept.yaml` (несвязанная незакоммиченная правка в рабочем дереве).

## Вне охвата
Константы имён артефактов (`plan.md` и т.д.) — отдельный follow-up (замена литералов = правка тел, несовместима с «чистым перемещением»). Именованный тип семафора, дедуп autonomous-блока, разбивка `retryStage`/`runWithRetry` — это S3/Tier 4.

## Декомпозиция: что куда переезжает

Каждая задача создаёт новый файл и УДАЛЯЕТ перечисленные декларации из `orchestrator.go` (перенос дословный). Порядок задач выбран так, чтобы группы были когерентны и независимы.

| Новый файл | Переносимые декларации (из orchestrator.go) |
|---|---|
| `concurrency.go` | `agentDrainTimeout` const; `type semNop`+методы; `type semChan`+методы; `semFor`; `spawnAgent`; `waitAgents`; `markAgentActive`; `markAgentDone`; `isAgentActive` |
| `dialog_poller.go` | `type violationCacheEntry`; `startQuestionPoller`; `pollQuestions`; `detectDialogViolation`; `jsonlFileForPhase`; `relocateMisplacedQuestions`; `pathInside`; `hasOpenQuestion`; `correctPhaseForState`; `popPreAskPhase` |
| `agents.go` | `planningContract` const; `sectionAssumptions` const; `requiredPlanSections` var; `runPlanningAgent`; `rePromptMissingSections`; `runPlanningWithFeedback`; `runImplementationAgent`; `runReviewAgent`; `runAutonomousAgent` |
| `runner_factory.go` | `wrapperDirFor`; `runnerFor`; `runnerForFallback`; `uiActionPublisher` |
| `scheduling.go` | `depsDone`; `tryActivatePrePlanned`; `startPlanningForUnblocked`; `startReadyStages`; `retryStage`; `failBlockedStages`; `allTerminal`; `shouldExit` |
| `control_api.go` | `FailStage`; `NotifyAnswer`; `approveStage`; `runContext`; `Approve`; `Revise`; `Retry` |
| `supervisor_track.go` | `isAutonomousStage`; `clearStaleAutonomousFlag`; `logSupervisorDecision`; `DetermineStagePhases` |

**Остаётся в `orchestrator.go` (ядро):** consts `keyAnswer/keyID/keyPhase`; `type Prompts`+`DefaultPrompts`; `type Options`; `type Orchestrator`; `setFatal`/`loadFatal`; `New`; `UIBus`; `Trigger`; `SetDashboardURL`; `Run`; `handleEvent`; `onAgentCompleted`; `onUserAnswered`; `currentStatus`; `resolvePlanSource`; `copyFile`; `StoreFromOrch`.

**Проверка полноты:** имена файлов `concurrency.go`, `dialog_poller.go`, `agents.go`, `runner_factory.go`, `scheduling.go`, `control_api.go`, `supervisor_track.go` НЕ существуют в пакете (проверено: текущие файлы — bus/completion/context/errors/fsm/graph/orchestrator/plan_adopt/recovery/retry/session/supervisor). `control_api.go` выбран вместо `api.go`, `supervisor_track.go` — чтобы не путать с существующим `supervisor.go`.

---

### Общий шаблон задачи (применять к каждой из Task 1–7)

Каждая задача идентична по форме. Ниже — общие шаги; в самой задаче указан только файл и список деклараций.

- [ ] **Step A: Создать новый файл** `pkg/orchestrator/<file>.go` с `package orchestrator`, перенеся В НЕГО перечисленные декларации из `orchestrator.go` — тела/сигнатуры/комментарии БАЙТ-В-БАЙТ без изменений. Добавить import-блок только с реально нужными пакетами.
- [ ] **Step B: Удалить** те же декларации из `pkg/orchestrator/orchestrator.go` (и импорты, ставшие в нём неиспользуемыми).
- [ ] **Step C: Собрать и проверить импорты.** `go build ./...` — если есть неиспользуемый/недостающий импорт в любом из двух файлов, поправить. `go vet ./pkg/orchestrator/...` — чисто.
- [ ] **Step D: Тесты.** `go test ./pkg/orchestrator/...` — всё зелёное (поведение не менялось).
- [ ] **Step E: Коммит** (сообщение на русском, без Co-Authored-By), напр.:
  `refactor(orchestrator): вынести <домен> в <file>.go`

---

### Task 1: concurrency.go

**Files:** Create `pkg/orchestrator/concurrency.go`; Modify `pkg/orchestrator/orchestrator.go`.

Перенести: `agentDrainTimeout` (const), `type semNop` + `acquire`/`release`, `type semChan` + `acquire`/`release`, `semFor`, `spawnAgent`, `waitAgents`, `markAgentActive`, `markAgentDone`, `isAgentActive`.

Применить общий шаблон Step A–E. Ожидаемые импорты нового файла: `context`, `sync`/`time` по мере использования, `github.com/akopichin/afm/pkg/flow` (spawnAgent принимает `flow.Stage`). Компилятор подтвердит точный набор.

Commit: `refactor(orchestrator): вынести семафоры и запуск агентских горутин в concurrency.go`

---

### Task 2: dialog_poller.go

**Files:** Create `pkg/orchestrator/dialog_poller.go`; Modify `orchestrator.go`.

Перенести: `type violationCacheEntry`, `startQuestionPoller`, `pollQuestions`, `detectDialogViolation`, `jsonlFileForPhase`, `relocateMisplacedQuestions`, `pathInside`, `hasOpenQuestion`, `correctPhaseForState`, `popPreAskPhase`.

Шаблон Step A–E. Вероятные импорты: `context`, `os`, `path/filepath`, `strings`, `time`, `log`, `pkg/flow`, `pkg/mcp`, `pkg/executor`, `pkg/state`. (Точный набор — по компилятору.)

Commit: `refactor(orchestrator): вынести поллер вопросов и обработку dialog-файлов в dialog_poller.go`

---

### Task 3: agents.go

**Files:** Create `pkg/orchestrator/agents.go`; Modify `orchestrator.go`.

Перенести: `planningContract` (const), `sectionAssumptions` (const), `requiredPlanSections` (var), `runPlanningAgent`, `rePromptMissingSections`, `runPlanningWithFeedback`, `runImplementationAgent`, `runReviewAgent`, `runAutonomousAgent`.

Шаблон Step A–E. Вероятные импорты: `context`, `os`, `path/filepath`, `strings`, `pkg/flow`, `pkg/prompts`, `pkg/state`. (По компилятору.)

Commit: `refactor(orchestrator): вынести агентские runner'ы в agents.go`

---

### Task 4: runner_factory.go

**Files:** Create `pkg/orchestrator/runner_factory.go`; Modify `orchestrator.go`.

Перенести: `wrapperDirFor`, `runnerFor`, `runnerForFallback`, `uiActionPublisher`.

Шаблон Step A–E. Вероятные импорты: `path/filepath`, `strings`, `pkg/executor`, `pkg/flow`. (По компилятору.)

Commit: `refactor(orchestrator): вынести конструирование runner'ов в runner_factory.go`

---

### Task 5: scheduling.go

**Files:** Create `pkg/orchestrator/scheduling.go`; Modify `orchestrator.go`.

Перенести: `depsDone`, `tryActivatePrePlanned`, `startPlanningForUnblocked`, `startReadyStages`, `retryStage`, `failBlockedStages`, `allTerminal`, `shouldExit`.

Шаблон Step A–E. Вероятные импорты: `context`, `os`, `path/filepath`, `pkg/flow`, `pkg/state`. (По компилятору.)

Commit: `refactor(orchestrator): вынести планирование стадий в scheduling.go`

---

### Task 6: control_api.go

**Files:** Create `pkg/orchestrator/control_api.go`; Modify `orchestrator.go`.

Перенести: `FailStage`, `NotifyAnswer`, `approveStage`, `runContext`, `Approve`, `Revise`, `Retry`.

Шаблон Step A–E. Вероятные импорты: `context`, `fmt`, `pkg/state`. (По компилятору.)

Commit: `refactor(orchestrator): вынести публичный control-API (Approve/Revise/Retry/NotifyAnswer) в control_api.go`

---

### Task 7: supervisor_track.go

**Files:** Create `pkg/orchestrator/supervisor_track.go`; Modify `orchestrator.go`.

Перенести: `isAutonomousStage`, `clearStaleAutonomousFlag`, `logSupervisorDecision`, `DetermineStagePhases`.

Шаблон Step A–E. Вероятные импорты: `context`, `os`, `path/filepath`, `encoding/json`/`time` по мере использования, `pkg/flow`. (По компилятору.)

Финальная проверка после Task 7: `wc -l pkg/orchestrator/orchestrator.go` — ядро должно ужаться примерно до ~550–650 строк; `go build ./... && go vet ./... && go test ./pkg/orchestrator/...` зелёное.

Commit: `refactor(orchestrator): вынести supervisor/autonomous-решение в supervisor_track.go`

---

## Self-Review

**Spec coverage (S2 = god-file split):** каждая из 7 групп деклараций (concurrency, dialog-poller, agents, runner-factory, scheduling, control-api, supervisor-track) закрыта отдельной задачей; ядро (New/Run/Trigger/event-handlers/struct) явно перечислено как остающееся. ✓

**Placeholder scan:** это motion-план — «код» каждого шага = перечень существующих деклараций для дословного переноса (тела уже в репозитории, менять их запрещено). Это НЕ placeholder: полная спецификация перемещения — точный список имён + правило «байт-в-байт». Импорты сознательно отданы компилятору (Step C) — единственный надёжный способ для motion, перечислять их вручную = риск рассинхрона. ✓

**Type/decl consistency:** все декларации взяты из реального инвентаря orchestrator.go (grep top-level). Ни одна не назначена дважды; сумма перенесённого + остающееся ядро = полный список из 54 деклараций. Имена новых файлов проверены на отсутствие клэша с 12 существующими файлами пакета. ✓

**Порядок/независимость:** задачи независимы (перенос одной группы не зависит от другой); каждая самостоятельно компилируется и тестируется. Выполнять последовательно (каждая правит orchestrator.go) — параллелить нельзя. ✓
