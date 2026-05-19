# Event-driven Redesign: flowManager v2

## Проблема

Текущая архитектура flowManager привязывает главный контекст Claude Code к ожиданию статусов.
Скилл `/flowmanager` запускает субагент-монитор, который поллит `state.json` каждые 3 секунды.
Каждый цикл монитора засоряет контекст, блокирует пользователя и неэффективно расходует токены
(LLM делает работу простого `sleep + cat`).

Дополнительно, оркестратор — последовательный: `PlanningPhase()` → `WaitForApprovals()` →
`ImplementationPhase()`. Нельзя начать implementation для одобренной стадии пока другие ждут approval.

## Цели

1. **Главный контекст свободен** — после `/flowmanager` пользователь работает, статус узнаёт когда захочет
2. **Event-driven оркестратор** — стадии работают параллельно и независимо через FSM
3. **Веб-дашборд с полным UI** — real-time статус, логи, кнопки Approve/Revise
4. **Терминальный check** — `flowmanager check` с наглядным форматированием
5. **Механизм фидбэка** — перезапуск planning агента с предыдущим планом + фидбэк

---

## 1. Event-driven FSM оркестратор

### FSM стадии

Каждая стадия — независимый автомат:

```
pending → planning → awaiting_approval ⇄ revising → ready → running → done
                                                                      ↘ failed
```

Новый статус `revising` — planning агент перезапускается с фидбэком.

### EventBus

Центральный канал событий:

```go
type EventType string

const (
    EventStageStatusChanged EventType = "stage_status_changed"
    EventAgentAction        EventType = "agent_action"
    EventFeedbackReceived   EventType = "feedback_received"
    EventAgentCompleted     EventType = "agent_completed"
)

type Event struct {
    Type    EventType
    StageID string
    Data    any
}

type EventBus struct {
    ch          chan Event
    subscribers []chan Event  // для WebSocket, логгера, и т.д.
}
```

Подписчики: оркестратор (реагирует на события), WebSocket-хаб (рассылает клиентам), логгер.

### Orchestrator Run Loop

Единый цикл вместо последовательных фаз:

```go
func (o *Orchestrator) Run(ctx context.Context) error {
    o.startReadyStages()  // запустить planning для стадий без depends_on

    for event := range o.bus.Events() {
        switch event.Type {
        case AgentCompleted:
            // planning done → awaiting_approval
            // implementation done → done
        case Approved:
            // awaiting_approval → ready, запустить implementation если deps готовы
        case Revised:
            // awaiting_approval → revising, перезапустить planning с фидбэком
        case StageStatusChanged:
            // проверить зависимости, запустить готовые стадии
        }

        if o.allTerminal() {
            break
        }
    }
    return o.summaryPhase(ctx)
}
```

Стадии работают параллельно и независимо. Backend-auth может выполняться пока frontend-login ждёт approval.

### Executor — интерфейс

```go
type Runner interface {
    RunPlanning(ctx context.Context, name, prompt, outFile, logFile string) error
    RunAgent(ctx context.Context, agentType, name, prompt, logFile string) error
}
```

Позволяет подменять для тестов.

### Что удаляется

- `PlanningPhase()`, `WaitForApprovals()`, `ImplementationPhase()` как отдельные методы
- Polling state.json изнутри оркестратора

### Что остаётся

- `executor.go` — запуск claude subprocess, парсинг stream-json
- `state.go` — структура RunState, Save/Load
- `graph.go` — граф зависимостей, ReadyStages
- `progress.go` — логирование в файлы

---

## 2. Веб-дашборд и HTTP API

### Запуск

`flowmanager run` поднимает HTTP-сервер вместе с оркестратором (foreground-процесс):

```
flowmanager run example-flow.yaml
# → запускает оркестратор + HTTP сервер
# → выводит: "Dashboard: http://localhost:9876"
# → логи стадий идут в терминал + файлы
```

### HTTP API

```
GET  /api/status              — RunState (все стадии, статусы, timestamps)
GET  /api/stages/:id/plan     — содержимое plan.md
GET  /api/stages/:id/log      — последние N строк лога
POST /api/stages/:id/approve  — одобрить план
POST /api/stages/:id/revise   — отправить фидбэк {feedback: "текст"}
```

Approve/revise через API генерируют события в EventBus.

### WebSocket

```
WS /ws — поток событий в реальном времени
```

События — `Event` из EventBus, сериализованный в JSON:

```json
{"type": "stage_status_changed", "stage_id": "backend-auth", "data": {"status": "awaiting_approval"}}
{"type": "agent_action", "stage_id": "backend-auth", "data": {"tool": "Edit", "detail": "internal/auth/jwt.go"}}
```

### Веб-UI

Единая HTML-страница с встроенным JS (vanilla JS, embedded в Go binary через `embed`).
Дизайн — через `frontend-design` скилл для качественного UI.

Элементы:
- **Левая панель** — список стадий с цветными индикаторами статуса
- **Правая панель** — детали выбранной стадии: план (markdown rendered), кнопки Approve/Revise с textarea для фидбэка, live-лог действий агента
- **Нижняя строка** — общий прогресс (время, кол-во завершённых стадий)

### Embed

```go
//go:embed web/*
var webFS embed.FS
```

Один HTML + один CSS + один JS файл. Без npm, бандлеров. Всё в бинарнике.

### Конфиг

```yaml
server:
  port: 9876          # порт дашборда (0 = выключен)
  open_browser: true  # автоматически открыть браузер
```

Отдельной команды `flowmanager serve` нет — сервер всегда запускается вместе с `flowmanager run`.
Если `port: 0` — сервер не поднимается (headless режим).

---

## 3. Скиллы Claude Code

### Удаляется

`flowmanager-monitor` — больше не нужен.

### `/flowmanager` — запуск flow

```
1. flowmanager list → выбрать flow
2. Запустить flowmanager run {flow} в фоне (! flowmanager run {flow})
3. Сказать: "Flow запущен. Дашборд: http://localhost:9876"
4. STOP. Контекст свободен.
```

Никаких субагентов, никакого ожидания.

### `/flowmanager-check` — статус

```
1. flowmanager check
2. Показать вывод. STOP.
```

Выводит стадии, статусы, последние действия агентов, кто ждёт approval.

### `/flowmanager-review <stage>` — новый скилл

```
1. Прочитать план: .flowManager/runs/{run}/{stage}/plan.md
2. Показать пользователю саммари плана
3. AskUserQuestion: "Одобрить или дать фидбэк?"
4a. Если ОК → flowmanager approve {stage} (или POST /api/stages/{stage}/approve)
4b. Если фидбэк → flowmanager revise {stage} --feedback "{текст}"
5. STOP. Контекст свободен.
```

### `/flowmanager-init` — без изменений

---

## 4. Механизм фидбэка

### Файловый протокол

При `flowmanager revise {stage} --feedback "текст"`:

1. Записать `.flowManager/runs/{run}/{stage}/feedback.md`
2. Обновить `state.json` — статус `revising`
3. EventBus → `Revised` событие

### Перезапуск planning агента

Оркестратор по событию `Revised`:

1. Переименовать `plan.md` → `plan.v{N}.md`
2. Перезапустить planning агента с промптом:

```
{planning_prompt}

## Stage: {name}
{description}

## Previous plan (needs revision)
{содержимое plan.md}

## Feedback
{содержимое feedback.md}

Revise the plan according to the feedback above.
```

3. По завершении: статус → `awaiting_approval`

### Множественные итерации

Feedback накапливается с разделителями:

```markdown
--- revision 1 | 2026-04-17 15:30 ---
Добавь Redis для блеклиста токенов

--- revision 2 | 2026-04-17 15:45 ---
Ещё добавь TTL для записей в Redis
```

---

## 5. Тестирование

### Уровень 1: Юнит-тесты

Полное покрытие:
- **FSM** — все переходы, невалидные переходы, edge cases
- **EventBus** — подписка, доставка, множественные подписчики
- **Graph** — зависимости, ReadyStages, циклы
- **State** — Save/Load, конкурентный доступ, атомарность
- **HTTP API** — каждый эндпоинт через `httptest`
- **Executor** — парсинг stream-json (существующие тесты)

### Уровень 2: Интеграционные тесты (Go)

Оркестратор + mock-executor (через `Runner` интерфейс).
Mock пишет фиксированный план, симулирует задержку.
Проверка полного lifecycle: planning → approval → revise → re-approval → implementation → done.

### Уровень 3: E2E тест с реальным Claude

Реальный flow из `../testFlow/flow.yaml`:

```yaml
stages:
  - id: init         # init react app
  - id: frontend     # landing BMW F900XR vs R1300GS
    depends_on: [init]
  - id: test         # запуск + визуальная проверка
    depends_on: [init, frontend]
```

**Сценарий:**

1. `flowmanager run ../testFlow/flow.yaml`
2. Для каждой стадии:
   a. Дождаться `awaiting_approval`
   b. Прочитать `plan.md`, оценить адекватность
   c. Если план слабый — `revise` с фидбэком, дождаться нового плана
   d. `approve`
3. Дождаться ALL_DONE
4. Финальная проверка:
   - React-приложение инициализировано (package.json, node_modules)
   - Лендинг: компоненты BMW F900XR и R1300GS
   - SSR + Node.js сервер настроен
   - state.json: все стадии done
   - Логи не пустые
   - Дашборд корректно работал
5. **Визуальная верификация:**
   - Запустить сервер лендинга
   - Открыть в браузере через MCP (chrome-devtools)
   - Сделать скриншот
   - Сравнить с планом стадии "frontend":
     - Есть лендинг BMW F900XR
     - Есть сравнение с BMW R1300GS
     - Есть фоновая анимация/видео
     - Есть фото обоих мотоциклов
     - Есть плюсы и минусы каждого
   - Если не соответствует — задокументировать и починить
6. **Сборка и линт:**
   - `make lint` — без ошибок
   - `make build` — без ошибок

### Уровень 4: Веб-дашборд

Через `frontend-design` скилл + ручная проверка в браузере:
Approve/Revise кнопки, WebSocket-обновления, отображение логов.

---

## 6. Структура файлов (итоговая)

```
pkg/
  orchestrator/
    orchestrator.go    — event-driven run loop (переписать)
    fsm.go             — FSM переходы стадий (новый)
    eventbus.go        — EventBus (новый)
    graph.go           — граф зависимостей (без изменений)
  executor/
    executor.go        — запуск claude, парсинг stream (минимальные изменения)
    runner.go          — Runner интерфейс (новый)
  state/
    state.go           — RunState + статус revising (обновить)
  progress/
    progress.go        — логирование (без изменений)
  server/
    server.go          — HTTP сервер + API (новый)
    websocket.go       — WebSocket хаб (новый)
    handlers.go        — HTTP handlers (новый)
  web/
    index.html         — дашборд UI (новый, через frontend-design)
    style.css
    app.js
cmd/
  flowmanager/
    run.go             — обновить: запуск сервера вместе с оркестратором
    main.go            — без изменений
    ...
assets/
  claude/skills/
    flowmanager/SKILL.md          — переписать (fire-and-forget)
    flowmanager-check/SKILL.md    — обновить
    flowmanager-review/SKILL.md   — новый скилл
    flowmanager-init/SKILL.md     — без изменений
    flowmanager-monitor/          — удалить
```
