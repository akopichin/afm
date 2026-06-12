# Backlog: архитектурные улучшения P2/P3

Пункты из архитектурного разбора 2026-06-10, отложенные за пределы текущей итерации (P0+P1, см. `2026-06-10-reliability-p0-p1-design.md`).

Не выкидывать — это материал к следующим раундам.

---

## P2. Полная декомпозиция `orchestrator.go`

**Контекст:** `pkg/orchestrator/orchestrator.go` сегодня — 1222 строки. В P0+P1 он сократится до ~500 (event-loop + handlers), часть логики переедет в `errors.go` и `retry.go`. Полный split откладываем.

**Желаемая структура:**

| Файл | Содержимое |
|---|---|
| `orchestrator.go` | event-loop + handlers, ~300 строк |
| `prompts.go` | `buildPlanningPrompt`, `buildRevisionPrompt`, `buildImplementationPrompt`, `buildReviewPrompt`, `interactivePlanningOverride` |
| `context.go` | `CollectArtifacts`, `CollectDependencyPlans`, `buildStageContext`, `resolveArtifactPath` |
| `retry.go` | `runWithRetry`, `RetryBackoff`, `isRetryableError`, `buildRetryContext` + `RetryPolicy interface` |
| `recovery.go` | `startPlanningForPending` (→ `resume`), `detectInterruptedPhase` — после WAL уже сильно похудеет |
| `runner_factory.go` | `runnerFor`, `runnerForFallback`, `actionPublisher` |

**Зависимости:** ничего не блокирует, делается независимо. Хорошая первая follow-up задача после P0+P1.

**Усилие:** ~1 день.

---

## P3. Actor per Stage

**Контекст:** сейчас один event-loop читает события и порождает горутины-агенты. State каждой стадии разделяется через `o.mu`. Это источник скрытой contention и потенциальных deadlock'ов при усложнении handler'ов.

**Идея:**
```go
type StageActor struct {
    id     string
    inbox  chan Event
    state  StageStatus  // приватное, читается только из run()
    fsm    *FSM
    runner Runner
}

func (a *StageActor) run(ctx) {
    for ev := range a.inbox {
        a.handle(ev)
    }
}
```

- Каждая стадия — отдельная горутина-актор с private state.
- Orchestrator — роутер: `EventApproved{StageID: "x"}` → `actors["x"].inbox <- ev`.
- Нет mutex'а на разделяемом state.
- Параллелизм естественный.
- Тестируется в изоляции: даёшь актору последовательность событий, проверяешь результат.

**Что устраняется:**
- `o.mu` исчезает.
- Save→publish race: каждый актор сам пишет в свой раздел WAL, sequence per-stage.
- Resume-логика упрощается: каждый актор сам читает свою WAL-проекцию на старте.

**Что усложняется:**
- Cross-stage coordination (зависимости, cascade failures) — нужна explicit messaging.
- Тесты intercepting cross-actor events требуют harness.

**Усилие:** ~1 неделя. Делается **после** того, как FSM-движок и WAL стабилизировались в проде — иначе режем по живому.

**Пререкизит:** P0+P1 + P2.

---

## P3. Event sourcing полностью

**Контекст:** в P0+P1 events.jsonl становится authoritative WAL, но `state.json` остаётся derived snapshot для быстрого `Snapshot()`. В event sourcing «полноценно» — snapshot строится лениво или по запросу, `state.json` исчезает.

**Зачем:**
- Time-travel debugging: показать состояние run'а в момент `Seq=42`.
- UI-timeline: построить визуализацию переходов из events.jsonl без отдельного API.
- Backup и репликация проще: один append-only файл.

**Что нужно:**
- Compaction strategy для events.jsonl (snapshot-checkpoint каждые N событий, удаление старых событий).
- Версионирование формата event'ов (для эволюции схемы).
- Recovery с компакта: `restore from checkpoint @ Seq=N, then replay tail`.

**Усилие:** ~3 дня. Зависит от того, нужны ли реально time-travel и timeline UI.

**Пререкизит:** P0+P1 + использование WAL в проде хотя бы месяц.

---

## P3. SQLite вместо JSON-файлов

**Контекст:** `state.json` + `events.jsonl` достаточно для текущего масштаба (одиночные run'ы, десятки стадий). Для concurrent flows / много параллельных run'ов / аналитики через CLI — SQL удобнее.

**Схема (черновик):**
```sql
CREATE TABLE runs (id, flow_name, started_at, status);
CREATE TABLE stages (run_id, stage_id, status, plan_path, ...);
CREATE TABLE events (run_id, seq, time, stage_id, from_status, to_status, event, reason);
CREATE TABLE artifacts (run_id, stage_id, name, path, ...);
CREATE TABLE sessions (run_id, stage_id, phase, session_id, mcp_config_path);
```

**Преимущества:**
- ACID, нет ручного fsync-протокола.
- `flowmanager query` для аналитики (как часто стадия `backend` фейлит, медианный retry-rate, etc).
- Concurrent reads без файловых блокировок.

**Минусы:**
- Зависимость на `mattn/go-sqlite3` (CGO) или `modernc.org/sqlite` (pure Go, медленнее).
- Сложнее «руками глянуть что в state'е» — JSON был для этого хорош.

**Усилие:** ~3-5 дней миграции + конвертер старых run'ов.

**Пререкизит:** реальная потребность (multi-flow / аналитика).

---

## Concurrent flows в одном проекте

**Контекст:** сейчас `.flowManager/runs/<flow>-<ts>/` подразумевает один активный run на flow. Если запустить два `flowmanager run` параллельно — будут гонки за `state.json`, MCP-port коллизии, конфликт за бинарный `bin/flowmanager` процесс.

**Что нужно:**
- Lockfile `.flowManager/runs/<run>/lock` через `flock` (уже есть `pkg/progress/flock_*` — переиспользовать).
- Уникальный MCP-port на run (берётся динамически, пишется в run-state).
- Уникальный dashboard port на run (или общий dashboard, который умеет показывать несколько run'ов — большая задача).
- WebSocket-роутинг по run-id.

**Усилие:** ~2-3 дня для базы (lock + динамические порты). Multi-run dashboard — отдельный проект.

---

## Compaction events.jsonl

**Контекст:** длинные run'ы с многократными retry/revise могут раздуть events.jsonl. Сейчас не критично, но при доминированном использовании может стать.

**Стратегия:**
- При `Close()` (или фоном раз в N минут): если `Len(events) > threshold`, пишем `checkpoint.json` со снапшотом всех текущих stage states + truncate'им events.jsonl до пустоты.
- На `Open`: грузим checkpoint, затем replay tail events.jsonl поверх.
- Старые events можно держать архивом в `events.archive.jsonl` для аудита.

**Усилие:** ~1 день. Делается, когда упрёмся в размер.

---

## RetryPolicy как интерфейс

**Контекст:** в P0+P1 `runWithRetry` переезжает в `retry.go`. Хорошо бы заодно вытащить policy в интерфейс — будет легче тестировать и менять стратегии.

```go
type RetryPolicy interface {
    ShouldRetry(err error, attempt int) bool
    NextBackoff(attempt int) time.Duration
    MaxAttempts() int
}
```

Дефолтная реализация — текущая (5s/10s/30s, retry на rate-limit). Можно делать тестовую (instant retry, fixed attempts), кастомную (per-stage политика через YAML).

**Усилие:** ~2 часа. Можно сделать вместе с P2-декомпозицией.

---

## Property-based тесты на shape FSM

**Контекст:** добавили rapid-тест на liveness. Можно расширить:

- **Safety:** «никогда нельзя из терминального статуса в нетерминальный» (Done → ничего, Failed → только manual_retry).
- **Determinism:** «один и тот же seed событий даёт идентичную последовательность статусов».
- **Idempotency:** «двойной Apply одного и того же event возвращает applied=false второй раз».

**Усилие:** ~2 часа.

---

## Прометей-метрики

**Контекст:** сейчас единственная наблюдаемость — `log.Printf` и WS-стрим в дашборд. Для prod-использования полезно:

- `flowmanager_stages_total{flow,status}` — gauge текущих стадий по статусам.
- `flowmanager_stage_duration_seconds{flow,stage,phase}` — histogram.
- `flowmanager_retries_total{flow,stage,reason}` — counter.
- `flowmanager_uibus_dropped_total{subscriber_type}` — counter (от UIBus).
- `flowmanager_wal_writes_total`, `flowmanager_wal_fsync_duration_seconds` — для health WAL.

**Усилие:** ~1 день включая `/metrics` endpoint и базовый Grafana-дашборд.

**Когда:** когда оркестратор начинают использовать в команде > 1 человека.
