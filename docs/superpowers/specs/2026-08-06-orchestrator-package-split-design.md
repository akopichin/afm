# Разбить pkg/orchestrator по ответственности (P1 из strictacode-отчёта)

**Дата:** 2026-08-06
**Статус:** design (одобрен к реализации)

## Контекст

`strictacode analyze` на чистом git-снапшоте afm (без untracked `node_modules`, см. ниже) показал `pkg/orchestrator` единственным пакетом со статусом `critical`: `overengineering_pressure=91` при среднем по проекту `15`, при умеренном `refactoring_pressure=36` (`diff=55` — паттерн "Overengineering": много типов, мало переиспользования).

Исходная рекомендация отчёта — сгруппировать файлы по трём кластерам:
- `bus.go`+`fsm.go` → `orchestrator/bus/`
- `scheduling.go`+`recovery.go`+`retry.go` → `orchestrator/lifecycle/`
- `supervisor.go`+`supervisor_track.go`+`dialog_poller.go` → `orchestrator/supervisor/`

Эта рекомендация основана только на именах файлов, без анализа зависимостей. Ниже — верификация и уточнённый план.

### Побочная находка (для истории, не часть этого плана)

Первый прогон `strictacode analyze .` в корне afm полностью проигнорировал Go-код: автодетект языка посчитал файлы по всему дереву, включая untracked `pkg/web/dashboard/node_modules` (3053 JS-файла), и выбрал `javascript` вместо `golang` (156 файлов). Причина — баг в `strictacode`: `.gitignore`-паттерны с путями (`pkg/web/dashboard/node_modules/`) сравниваются только с именем директории, а не с полным путём, поэтому `node_modules` не исключается. Обошли прогоном на `git archive HEAD` (чистый снапшот без untracked-файлов) — конфиги afm не менялись.

## Верификация: что в исходном плане верно, а что нет

В Go метод обязан находиться в том же пакете, что и его receiver-тип — файл с методами `func (o *Orchestrator) ...` невозможно перенести в другой пакет без редизайна, это не файловая операция.

Проверка `grep -c "^func (o \*Orchestrator)" *.go` по пакету:

| Файл | Методов `Orchestrator` | Собственные типы |
|---|---|---|
| `bus.go` | 0 | `CriticalBus`, `UIBus` |
| `fsm.go` | 0 | `FSM` |
| `graph.go` | 0 | `Graph` |
| `supervisor.go` | 0 | `Supervisor` |
| `session.go`, `notices.go`, `plan_adopt.go`, `context.go`, `completion.go` | 0 | чистые функции по явным параметрам (`stageDir`/`runDir`/`projectDir`) |
| `errors.go` | 0 | типы ошибок |
| `agents.go` | 10 | — |
| `concurrency.go` | 7 | `semNop`, `semChan` |
| `control_api.go` | 10 | — |
| `dialog_poller.go` | 8 | — |
| `hooks.go` | 13 | — |
| `orchestrator.go` | 12 | `Orchestrator` |
| `recovery.go` | 4 | — |
| `retry.go` | 1 | — |
| `runner_factory.go` | 2 | — |
| `scheduling.go` | 10 | — |
| `supervisor_track.go` | 3 | — |

Итого 80 методов `Orchestrator`, размазанных по 11 файлам. Из них `scheduling.go`, `recovery.go`, `agents.go`, `control_api.go`, `orchestrator.go`, `hooks.go` вызывают друг друга и общее ядро (`Trigger`, `spawnAgent`, `graph`, `currentStatus`, `ui`/`critical`-шины) — это один связный конечный автомат стадии, а не три независимые подсистемы. Предложенная группировка `scheduling+recovery+retry` и `supervisor+supervisor_track+dialog_poller` невалидна как "перенос файлов": `supervisor.go` уже независим и не нуждается в группировке ни с чем, а `scheduling.go`/`recovery.go`/`retry.go`/`supervisor_track.go`/`dialog_poller.go` — методы `Orchestrator`, требующие декомпозиции самого типа, а не перемещения.

## Итоговый план

### Tier 1 — механический перенос (уже независимые типы/функции, ноль риска)

```
pkg/orchestrator/bus/          ← bus.go + fsm.go
pkg/orchestrator/graph/        ← graph.go
pkg/orchestrator/supervisor/   ← supervisor.go
pkg/orchestrator/stagefiles/   ← session.go + notices.go + plan_adopt.go + context.go + completion.go
```

`errors.go` **не переносится** — типы ошибок (`StorageError`, `IncompleteWorkError`, `MissingArtifactError`, `MissingSectionsError`) конструируются прямо в ядре (`agents.go`, `recovery.go`); перенос добавил бы импорт-шум на каждый call site почти без выигрыша в OP (это мелкие типы; "critical"/"warning"-статус у них в исходном отчёте — артефакт формулы `density=100%` на однострочных методах, не реальная проблема — см. Edge Case "No Problem Elements" в скилле strictacode).

`stagefiles` объединяет два разных по смыслу набора функций (файловая бухгалтерия диалога: `session.go`+`notices.go`; чтение результатов стадии: `context.go`+`plan_adopt.go`+`completion.go`) в один пакет ради меньшего числа новых пакетов — типов там мало, дробить дальше не даёт выигрыша по OP.

**Обязательное изменение при переносе `bus.go`:** `wakeEventLoop()` (сейчас в `concurrency.go`) напрямую пишет в приватное поле `critical.ch` неблокирующим `select{case ch<-ev: default:}` — семантика отличается от `CriticalBus.Publish` (тот блокируется до `ctx.Done()`). После переноса `bus.go` в отдельный пакет прямой доступ к `ch` невозможен (unexported field другого пакета). Добавить в `bus.CriticalBus` новый метод:

```go
// TryPublish публикует событие неблокирующим best-effort send; возвращает
// false, если буфер шины полон (вызывающий код тогда просто теряет толчок —
// см. вызов в concurrency.WakeEventLoop).
func (b *CriticalBus) TryPublish(ev Event) bool {
    select {
    case b.ch <- ev:
        return true
    default:
        return false
    }
}
```

Это единственное поведенческое изменение во всём Tier 1 — требует отдельного unit-теста (буфер полон → `TryPublish` возвращает `false`, не блокируется).

### Tier 2 — декомпозиция с новым типом (только там, где это безопасно)

```
pkg/orchestrator/concurrency/  ← concurrency.go
```

```go
package concurrency

type Manager struct {
    critical     *bus.CriticalBus // конкретный тип — bus уже независим (Tier 1)
    sems         map[string]semaphore
    activeAgents sync.Map
    agentWG      sync.WaitGroup
}

func New(critical *bus.CriticalBus, sems map[string]semaphore) *Manager
func (m *Manager) SpawnAgent(ctx context.Context, s flow.Stage, run func(context.Context, flow.Stage))
func (m *Manager) MarkActive(stageID string)
func (m *Manager) MarkDone(stageID string)
func (m *Manager) IsActive(stageID string) bool
func (m *Manager) WaitAgents()
func (m *Manager) WakeEventLoop() // критично: использует TryPublish, не Publish
```

`concurrency.go` не трогает `Trigger`/FSM вообще — только семафоры, `sync.Map`, `sync.WaitGroup`. Единственная его внешняя зависимость (`o.critical`) уже независимый тип после Tier 1 → перенос безопасен без интерфейсов и без изменения публичного API `Orchestrator`.

### Явно вне охвата: retry.go, hooks.go, dialog_poller.go, errors.go

`retry.go` (`runWithRetry`) вызывает `o.Trigger`, `o.triggerWithSeq`, `o.hasOpenQuestion`, `o.currentStatus`, `o.failBlockedStages` — управляет FSM стадии напрямую. Вынос в отдельный пакет потребовал бы инверсии зависимости: интерфейс `StageEngine`, определённый в новом пакете, реализуемый `Orchestrator` — но интерфейс с не-package-local методами обязан состоять из **экспортированных** методов, а `triggerWithSeq`/`hasOpenQuestion`/`currentStatus`/`failBlockedStages` сейчас приватные. Экспортировать их означало бы дать любому коду в бинаре возможность дёргать FSM стадии в обход event-loop — риск, неприемлемый ради метрики. **Решение: не выносить.** Внутренняя группировка `maxRetries`/`retryBackoff`/`interruptChans` в приватный тип внутри `pkg/orchestrator` рассматривалась и отклонена — не даёт эффекта на OP пакета (тип остаётся в той же package-границе), а трогать рабочий файл без выгоды — не оправдано.

`hooks.go` и `dialog_poller.go` — тот же паттерн (методы дёргают `o.Trigger`/`o.graph`/`o.critical`/`o.ui` напрямую), причём глубже переплетены с ядром, чем `retry.go`. По аналогии — не выносятся.

`agents.go`, `control_api.go`, `orchestrator.go`, `recovery.go`, `runner_factory.go`, `scheduling.go`, `supervisor_track.go` — ядро конечного автомата стадии, один связный компонент. Дальнейшее дробление не имеет естественных границ и рискует нарушить инварианты CAS/flock/recovery, описанные в `CLAUDE.md`, ради косметики метрики.

## Внешний impact

Проверены все 3 файла вне `pkg/orchestrator`, импортирующие пакет:

- **`cmd/afm/run.go`** — использует только `orchestrator.New`/`Options`/`Prompts`, которые остаются в ядре. **Изменений не требует.**
- **`pkg/server/server.go`** — поле `uiBus *orchestrator.UIBus` (2 места: приватное поле + поле в `Options`) → после Tier 1 нужен новый импорт `pkg/orchestrator/bus` и замена на `*bus.UIBus`.
- **`pkg/server/websocket.go`** — `writePump(..., ch <-chan orchestrator.Event, ...)` → замена на `<-chan bus.Event`.

## Порядок миграции

От самого изолированного файла к менее изолированному, с полным прогоном тестов после каждого шага (не одним махом в конце — красный тест = регрессия переноса конкретного шага, а не сюрприз в конце):

1. `graph.go` → `pkg/orchestrator/graph` (ноль внешних вызовов — тренировочный прогон процесса).
2. `supervisor.go` → `pkg/orchestrator/supervisor`.
3. `stagefiles` (session.go+notices.go+plan_adopt.go+context.go+completion.go) — больше call site'ов в ядре, но поведение не меняется.
4. `bus.go`+`fsm.go` → `pkg/orchestrator/bus` + новый `TryPublish` + правки `pkg/server/server.go`/`websocket.go`. Самый чувствительный шаг Tier 1.
5. `concurrency.go` → `pkg/orchestrator/concurrency` — последним, зависит от шага 4 (`*bus.CriticalBus` уже перенесён).

После каждого шага: `go build ./...` + полный `go test ./pkg/orchestrator/... ./pkg/server/...`.

После завершения Tier 1 и после завершения Tier 2 отдельно — контрольный прогон `strictacode analyze` (на чистом `git archive HEAD`-снапшоте, как в исходном отчёте) для проверки фактического снижения `overengineering_pressure` пакета `orchestrator`. Правило: **никаких дальнейших шагов ради метрики, если фактический эффект слабый** — план останавливается на том, что даёт реальную архитектурную пользу.

## Риски и митигации

| Риск | Митигация |
|---|---|
| Импорт-цикл (новый подпакет → `pkg/orchestrator`) | Правило: ни один новый пакет не импортирует `pkg/orchestrator`; только `pkg/flow`, `pkg/executor`, `pkg/state`, stdlib |
| `TryPublish` — новая семантика на `CriticalBus` | Unit-тест на неблокирующий drop при полном буфере, до переноса `concurrency.go` |
| Скрытая регрессия в существующих тестах пакета (44 test-файла) | Полный `go test ./pkg/orchestrator/...` после каждого из 5 шагов |
| OP-эффект слабее ожидаемого | Контрольный `strictacode analyze` после Tier 1 и после Tier 2 раздельно; при слабом эффекте — остановиться, не продолжать дробление ради дробления |

## Вне охвата этого плана

- Вынос `retry.go`, `hooks.go`, `dialog_poller.go`, `errors.go` — см. раздел "Явно вне охвата".
- Дальнейшее дробление ядра (`agents.go`/`control_api.go`/`orchestrator.go`/`recovery.go`/`runner_factory.go`/`scheduling.go`/`supervisor_track.go`) — один связный автомат, естественных границ нет.
- Экспорт FSM-control методов `Orchestrator` (`TriggerWithSeq`, `HasOpenQuestion`, `CurrentStatus`, `FailBlockedStages`) для нужд гипотетического будущего выноса — осознанно отклонено как небезопасное расширение публичного API.
