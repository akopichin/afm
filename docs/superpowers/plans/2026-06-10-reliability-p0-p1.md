# Reliability P0+P1 — Итоговый статус

**Дата:** 2026-06-10 (реализовано), обновлено 2026-06-16  
**Статус:** ✅ Выполнено полностью в ветке `planning-depends-on-ref`  
**Spec:** `docs/superpowers/specs/2026-06-10-reliability-p0-p1-design.md`

---

## Цель

Перевести ядро оркестратора на WAL-store, единый FSM-движок, раздельные шины (Critical/UI) и промпты с жёстким output-контрактом — фиксит тихие drop'ы критических событий, crash-unsafe state, ручные `setStatus` и слабый контракт промптов.

---

## История веток

Работа велась в ветке `reliability-improvements`. После финализации P0+P1 ветка была вмёржена в `master` (коммит `4684b04`). Текущая ветка `planning-depends-on-ref` ответвлена **после** этого мержа и содержит все P0+P1 улучшения как базу.

После мержа ветка `reliability-improvements` ушла в другую сторону: вернулась к MCP-диалогу и убрала `depends_on`-гейтинг. Эти изменения в текущую ветку **не переносились** — они идут вразрез с файловым протоколом диалога и фичей `planning-depends-on`.

---

## Что реализовано

### Phase 1: WAL Store (`pkg/state/store.go`)

- [x] `Store.Open` — создаёт/открывает `events.jsonl`, replay при рестарте
- [x] `Store.Apply` — append + fsync, валидация `From`-состояния
- [x] `Store.Snapshot` — копия для read-only потребителей
- [x] Replay из существующего `events.jsonl` при переоткрытии
- [x] Truncate битой хвостовой строки (crash-safety)
- [x] Derived snapshot `state.json` (fsync + rename)
- [x] Legacy fallback: загрузка старого `state.json` в `events.jsonl` со синтетическим событием `legacy_load`
- [x] `Save`/`Load` из `pkg/state/state.go` помечены deprecated и удалены

### Phase 2: Error Classifier (`pkg/orchestrator/errors.go`)

- [x] Типы `IncompleteWorkError`, `MissingArtifactError`, `MissingSectionsError`, `StorageError`
- [x] `Classify(err) Classification` — распознаёт Retryable/Incomplete/MissingArtifact/MissingSections/StorageFatal/Fatal
- [x] Паттерны: rate limit, overloaded, capacity, HTTP 5xx

### Phase 3: Раздельные шины (`pkg/orchestrator/bus.go`)

- [x] `CriticalBus` — blocking publish через `context.Context`; гарантирует доставку для FSM-событий
- [x] `UIBus` — fan-out с drop для медленных подписчиков; `DroppedCount()` для метрик
- [x] `eventbus.go` удалён

### Phase 4: FSM-движок (`pkg/orchestrator/fsm.go`)

- [x] Table-driven `FSM.Apply(stageID, FSMEvent, GuardCtx, reason)` — единственный путь смены статуса
- [x] `phaseDispatch` — `EvUserAnswered`/`EvResumeAfterRetry` → planning или running в зависимости от фазы
- [x] `IsTerminal(status)` — проверка финального состояния
- [x] Property-based тест liveness через `pgregory.net/rapid`
- [x] `setstatuslinter` (`tools/setstatuslinter/main.go`) — кастомный analyzer, запрещает прямой `Store.Apply` вне `fsm.go`

**Отличие от оригинала:** `EvAskUser` дополнительно разрешён из `StatusRetrying` и `StatusRevising` — агент может задавать вопросы в середине retry/revision цикла. Без этого переход молча отклонялся и стадия зависала.

**Полная таблица переходов:**

| Event | From | To |
|-------|------|----|
| `start_planning` | Pending, Retrying, Revising | Planning |
| `plan_ready` | Pending, Planning, Retrying | AwaitingApproval |
| `approve` | AwaitingApproval | Ready |
| `revise` | AwaitingApproval | Revising |
| `start_run` | Ready | Running |
| `complete` | Running, Planning, AwaitingApproval, Retrying | Done |
| `fail` | любой нетерминальный | Failed |
| `ask_user` | Planning, Running, **Retrying, Revising** | AwaitingUserInput |
| `user_answered` | AwaitingUserInput | Planning или Running |
| `schedule_retry` | Planning, Running | Retrying |
| `resume_after_retry` | Retrying | Planning или Running |
| `manual_retry` | Failed | Pending |
| `blocked_by_dep` | Pending | Failed |
| `ready` | Pending, Retrying | Ready |

### Phase 5: Промпты (`pkg/prompts/`)

- [x] `builder.go` — `Build(Inputs)` с XML-разделителями (`<system_rules>`, `<stage>`, `<plan>`, `<feedback>`, …)
- [x] `escapeTags` — нейтрализует попытки инъекции через пользовательский payload (`</stage>`, `<system_rules>` и др.)
- [x] `validator.go` — `ValidatePlan(md, required) PlanIssues` проверяет обязательные `##`-секции
- [x] `builder_test.go` — golden тест + injection тест
- [x] `assets/prompts/planning.md` / `implementation.md` / `review.md` / `summary.md` — добавлен Output Contract

**Отличие от оригинала:** `interactive_rules` в builder содержит инструкции файлового протокола диалога (запись `<phase>.qN.question.json`, bash-polling ответа), а не MCP-инструмент.

### Phase 6: Декомпозиция orchestrator.go

- [x] `retry.go` — логика retry (scheduleRetry, retryDelay, runRetryLoop)
- [x] `recovery.go` — startPlanningForPending, resumeInteractiveAgent, startPlanningForUnblocked, startReadyStages, failBlockedStages
- [x] `context.go` — buildDependencyContext, collectArtifacts (с обработкой ошибок вместо тихого игнорирования)
- [x] `orchestrator.go` — сведён к инициализации, event-loop, Trigger/NotifyAnswer

### Phase 7: Тесты

- [x] `integration_test.go` распилен на 4 домена:
  - `integration_resume_test.go`
  - `integration_retry_test.go`
  - `integration_interactive_test.go`
  - `integration_failure_test.go`
- [x] Общий `testharness` / `eagerProbeRunner` для integration-тестов
- [x] Crash-injection тест в `store_test.go`
- [x] Property-based liveness тест FSM через `pgregory.net/rapid`

### Phase 8: WebSocket (`pkg/server/websocket.go`)

- [x] Overflow буфера подписчика → `conn.Close(1008)` вместо тихого drop

---

## Что добавлено в текущей ветке поверх P0+P1

Эти компоненты не были в оригинальном плане, но являются частью надёжности системы:

### Файловый протокол диалога (планировалось отдельно, реализовано здесь)

- [x] `activeAgents sync.Map` в Orchestrator — отслеживает активные агент-горутины
- [x] `markAgentActive` / `markAgentDone` / `isAgentActive`
- [x] `NotifyAnswer` — если агент активен: FSM-переход + UI-публикация; если вышел: restart через critical bus
- [x] `startQuestionPoller` / `pollQuestions` — polling `*.question.json` каждую секунду
- [x] `pkg/mcp/dialog.go` — `FindUnansweredQuestions`, `QuestionFile`, `appendLine`
- [x] `handleDialogAnswer` в `handlers.go` — атомарная запись `answer.json` (O_EXCL)

### Гейтинг planning по depends_on

- [x] `stage.EagerPlanning` — флаг немедленного старта планирования
- [x] По умолчанию planning ждёт завершения `depends_on` стадий
- [x] `startPlanningForUnblocked` — запуск при recovery, когда зависимость уже done

---

## Acceptance Criteria (все выполнены)

- [x] `Store.Apply` + fsync гарантирует сохранность события до подтверждения
- [x] При crash и рестарте `events.jsonl` восстанавливает состояние без потерь
- [x] `CriticalBus.Publish` блокирует вместо drop — FSM-события не теряются
- [x] Все переходы статуса идут через `FSM.Apply`, не через прямой `setStatus`
- [x] `setstatuslinter` запрещает `Store.Apply` вне `fsm.go` на уровне линтера
- [x] Пользовательский payload не может инъецировать `<system_rules>` в промпт
- [x] `ValidatePlan` возвращает `MissingSections` для планов без обязательных секций
- [x] Все тесты проходят: `go test ./...` → OK
