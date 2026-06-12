# Interactive Stage — диалог агента с пользователем

**Дата:** 2026-06-08
**Статус:** Design approved, ready for implementation plan
**Контекст:** brainstorm в текущей сессии (см. `git log` за этот день)

## Проблема

В flowManager сейчас агент работает one-shot: получает prompt на stdin, стримит `stream-json` в stdout, завершается. Если стейдж требует от агента уточнения у пользователя по ходу работы (например, sub-agent или skill, который должен собрать требования или выбрать вариант реализации), такой агент не может «спросить» — flowManager ему просто не предоставляет канала обратной связи.

Нужно:

- Помечать стейдж как диалоговый одним флагом в YAML.
- Веб-UI: в правой части стейджа всплывают вопросы агента с предложенными вариантами и полем для собственного ответа; история диалога свёрнута/раскрывается.
- При перезапуске flowManager диалог продолжается ровно с того места, где остановился: предыдущие ответы видны агенту, незаданные вопросы перезадаются (или ответ доставляется мгновенно если он уже есть).

## Решение (high-level)

Один булевый флаг `interactive: true` на стейдже. Когда он есть:

1. flowManager перед запуском агента генерирует временный MCP-конфиг (`mcp.json`) с одним tool — `ask_user`.
2. Агент запускается через `claude --print --mcp-config <path> --session-id <uuid>` (или для resume — `--resume <uuid>`).
3. Когда агент вызывает `ask_user(id, question, options?, allow_custom?)`, MCP-сервер (встроенный в flowManager HTTP) блокирует tool-ответ, публикует событие в EventBus, переводит стейдж в статус `awaiting_user_input` и ждёт ответа от UI.
4. Пользователь отвечает через UI — MCP возвращает результат агенту, агент продолжает работу в той же сессии claude.
5. Диалог пишется в `dialog.jsonl` (append-only, парные записи Q/A с одним id). При рестарте MCP отвечает на повторные `ask_user(id)` мгновенно из этого файла — никаких дублей вопросов пользователю.

Стейдж завершается обычным образом: `.done` или артефакты. Никакого специального `finish_dialog` не нужно.

## 1. YAML и модель Stage

```yaml
- id: discovery
  name: "Сбор требований"
  description: "Опроси пользователя про auth-флоу, БД, роли. Собери requirements.md."
  agents: [my-requirements-skill]   # любой агент: built-in, sub-agent, skill
  interactive: true                  # ← NEW
  artifacts:
    - name: requirements
      path: ./requirements.md
```

Поведение:

- `interactive: true` прокидывает MCP-tool `ask_user` всем агентам стейджа (planning, implementation, review — если они есть).
- В UI стейджа появляется секция «Диалог» (история + текущий вопрос).
- Стейдж завершается по обычным правилам (`.done` или artifacts). Агенту в промпте просто указывается: «когда соберёшь информацию — запиши артефакт и заверши работу».
- `interactive: false` (по умолчанию) — поведение и UI **полностью идентичны** текущему. Backwards-compatible.

В `pkg/flow/flow.go`:

```go
type Stage struct {
    // … существующие …
    Interactive bool `yaml:"interactive"`
}
```

Никаких новых валидаций — флаг по умолчанию false. Никакого нового agent-type — `agents:` принимает то же что и сейчас.

## 2. MCP-сервер и tool `ask_user`

flowManager поднимает MCP-сервер на отдельном пути дашборд-сервера (HTTP-transport, в том же процессе). Это:

- общая память с orchestrator (читает/пишет `<phase>.dialog.jsonl`, публикует Event в EventBus);
- работает поверх существующего HTTP-сервера — не нужен дополнительный listener;
- легко отлаживается (`curl` + логи).

### Регистрация для агента

Перед запуском `claude --print` для каждой фазы interactive-стейджа executor создаёт временный `mcp.json` в stage-директории и передаёт его через `--mcp-config`:

```json
{
  "mcpServers": {
    "flowmanager": {
      "type": "http",
      "url": "http://127.0.0.1:9876/mcp/<stage-id>/<phase>"
    }
  }
}
```

URL содержит `<stage-id>/<phase>` — это позволяет MCP-серверу знать, какому стейджу и какой фазе адресован вопрос, без передачи лишних параметров в каждый tool-вызов. Файл `<phase>.dialog.jsonl` MCP читает/пишет на основании URL.

### Схема tool `ask_user`

```json
{
  "name": "ask_user",
  "description": "Ask the human user a question and wait for their answer. Use this when you need clarification, a choice between alternatives, or any decision that requires human input.",
  "inputSchema": {
    "type": "object",
    "required": ["id", "question"],
    "properties": {
      "id":           {"type": "string", "description": "Stable, deterministic id for this question (e.g. q1, q2, …). Used for idempotent replay after restart."},
      "question":     {"type": "string"},
      "options":      {"type": "array", "items": {"type": "string"}, "description": "Optional suggested answers. User can pick one or write their own."},
      "allow_custom": {"type": "boolean", "default": true, "description": "Whether the user may type a freeform answer instead of picking an option."}
    }
  }
}
```

Возврат tool: `{"answer": "<строка>", "from_options": true|false}`.

### Логика обработки tools/call

При входящем `tools/call` для `ask_user`:

1. MCP-сервер читает `<phase>.dialog.jsonl` (фаза известна из URL), ищет запись с этим `id`.
2. Если запись есть **и в ней есть `answer`** — мгновенно возвращает её агенту (idempotent replay).
3. Если записи нет — создаёт её, публикует `Event{Type: EventAskUser, StageID, Data: {id, phase, question, options, allow_custom}}`, ставит стейдж в статус `awaiting_user_input`, и блокирует tool-ответ до прихода ответа от UI или отмены.
4. Когда `POST /api/stages/<id>/dialog/answer` приходит — дописывает `answer` в ту же запись `<phase>.dialog.jsonl`, возвращает tool-результат, переводит стейдж обратно в предыдущий статус (`planning` для planning-фазы, `running` для implementation/review).

### Промпт стейджа

Дополняется одной короткой инструкцией про детерминированный id:

> «У тебя есть tool `ask_user`. Каждому вопросу присваивай стабильный id (`q1`, `q2`, …) в порядке возрастания. ВАЖНО: если ты задаёшь тот же вопрос повторно после рестарта, используй ТОТ ЖЕ id — иначе пользователь увидит дубль.»

В реальности claude отлично с этим справляется через `--resume`: после рестарта tool-вызов восстановится с тем же id (потому что claude вспомнит свой предыдущий вызов), и MCP отдаст сохранённый ответ.

## 3. Persistence и файловый layout

Добавляются файлы в существующую стейдж-директорию `.flowManager/runs/<run>/<stage-id>/`. У каждой фазы (planning, implementation, review) — **свой** запуск claude со своим `--session-id` и своим логом диалога, поэтому файлы per-phase:

```
<stage-id>/
  plan.md                          # как и раньше
  planning.log                     # как и раньше
  planning.jsonl
  implementation.log
  implementation.jsonl

  mcp.json                         # NEW — единый MCP-config (URL содержит phase), перегенерируется при старте каждой фазы
  planning.session.json            # NEW — { "session_id": "<uuid>" } для planning-агента
  planning.dialog.jsonl            # NEW — append-only лог диалога planning-фазы
  implementation.session.json      # NEW — аналогично для implementation
  implementation.dialog.jsonl
  review.session.json              # NEW — аналогично для review
  review.dialog.jsonl
```

Файлы фазы создаются лениво — только если эта фаза действительно использовала `ask_user`.

### Формат `<phase>.dialog.jsonl`

Одна JSON-запись на строку, append-only. Раздельные записи на вопрос и ответ. Фаза подразумевается из имени файла, поэтому поле `phase` в записях не нужно:

```jsonl
{"id":"q1","ts":"2026-06-08T15:31:02Z","question":"Использовать Redis для блеклиста токенов?","options":["да","нет"],"allow_custom":true}
{"id":"q1","ts":"2026-06-08T15:31:18Z","answer":"да","from_options":true}
{"id":"q2","ts":"2026-06-08T15:31:25Z","question":"Какой TTL для access-токена?","options":["15m","1h","24h"],"allow_custom":true}
{"id":"q2","ts":"2026-06-08T15:32:01Z","answer":"30m","from_options":false}
```

Раздельные строки на вопрос и ответ — два append-only события, никакого rewrite. Это ключ к надёжности: даже если flowManager упал между вопросом и ответом, файл остаётся консистентен. MCP-сервер при чтении группирует записи по `id`.

### Формат `<phase>.session.json`

```json
{ "session_id": "550e8400-e29b-41d4-a716-446655440000" }
```

Executor сейчас не сохраняет claude session-id. При первом запуске interactive-фазы executor генерирует UUID, передаёт его в `claude --session-id <uuid>` и сохраняет в `<phase>.session.json`. При резюме читает и передаёт `--resume <uuid>` (или эквивалентную комбинацию флагов — точная команда зависит от того, как именно claude CLI поддерживает resume сессии в `--print` режиме; см. раздел «Риски»).

### Атомарность записи `dialog.jsonl`

Обычный `O_APPEND` + строка с `\n` — POSIX гарантирует атомарность append-записей до `PIPE_BUF` (4096 байт). Одна запись помещается. Никаких temp+rename не нужно.

### Новый статус в state

```go
const (
    // … существующие …
    StatusAwaitingUserInput StageStatus = "awaiting_user_input"
)
```

`awaiting_user_input` — не терминальный и не failed. UI рисует другой цвет (например, фиолетовый) и подсветку «нужен ответ». В FSM-helpers (`pkg/orchestrator/fsm.go`) — добавляется как нетерминальный статус.

## 4. Restart-логика и event-loop

Изменения в `orchestrator.Orchestrator` минимальные. В `startPlanningForPending` есть switch по текущему статусу стейджа — добавляется одна ветка:

```go
case state.StatusAwaitingUserInput:
    // Стейдж был в диалоге. Перезапускаем агента через --resume —
    // MCP при повторном tools/call от агента вернёт сохранённые ответы.
    // Если пользователь уже ответил пока flowManager лежал, ответ есть
    // в dialog.jsonl и tool вернётся мгновенно.
    go func(st flow.Stage) {
        sem := o.semFor(st)
        sem.acquire()
        defer sem.release()
        o.resumeInteractiveAgent(ctx, st)
    }(s)
```

`resumeInteractiveAgent` определяет, какую фазу резюмить, по наличию `<phase>.session.json` + `<phase>.dialog.jsonl` с незакрытой парой. Затем вызывает тот же `runImplementationAgent` (или `runPlanningAgent`/review) с двумя отличиями:

1. передаёт `--resume <session_id>` вместо нового запуска (session_id берётся из соответствующего `<phase>.session.json`);
2. в промпте короткий префикс: «Сессия была прервана и продолжена. У тебя есть tool `ask_user`; если ты задавал вопросы — переспроси их с теми же id, ответы вернутся мгновенно из лога.»

В норме одновременно «открытой» (с незакрытым Q) может быть только одна фаза — фазы выполняются последовательно. Если по какой-то причине обнаружено несколько — резюмируется последняя по mtime файла.

### Event flow для одного диалогового цикла

```
agent → MCP /mcp/<stage>/<phase>     (tools/call: ask_user, id=q1)
                ↓
MCP-сервер:
  - читает <phase>.dialog.jsonl, q1 не найден
  - append: {id:q1, question, options, ts}
  - bus.Publish(Event{Type: EventAskUser, StageID, Data: {id, phase, question, options}})
  - запоминает prev-status стейджа и переводит stage → awaiting_user_input
  - блокирует tool-ответ на канале waiters[stage:phase:q1]
                ↓
WebSocket → UI: рисует вопрос в секции «Диалог», бейдж в stages-list
                ↓
user отвечает → POST /api/stages/<stage>/dialog/answer {id:q1, phase, answer:"…", from_options:false}
                ↓
HTTP handler:
  - append в <phase>.dialog.jsonl: {id:q1, answer, ts}
  - waiters[stage:phase:q1] <- answer
  - bus.Publish(Event{Type: EventUserAnswered, ...})
  - state: stage → (prev-status, восстановленный из памяти MCP-сервера: running для impl/review, planning для planning)
                ↓
MCP-сервер просыпается, возвращает tool result агенту
                ↓
agent → продолжает работу в той же сессии
```

«Prev-status» детерминирован через фазу из URL: planning → `StatusPlanning`, implementation/review → `StatusRunning`. MCP-сервер хранит in-memory мапу `stage-id → prev-status`, но даже при крашe это безопасно — на restart `resumeInteractiveAgent` сразу выставит правильный статус по `session.json`-файлам.

### Новые EventType

```go
const (
    EventAskUser      EventType = "ask_user"        // новый вопрос
    EventUserAnswered EventType = "user_answered"   // пришёл ответ
)
```

WebSocket-клиент обрабатывает их (см. секцию 5).

### Edge cases

- **Пользователь хочет «передумать».** UI добавляет в секцию «Диалог» кнопку «Отменить стейдж». Это переводит стейдж в `failed`, MCP-waiters получают сигнал отмены и возвращают агенту tool-error. Дальше — стандартный flow `retry` через существующий UI.
- **Одновременно несколько вопросов от агента.** Теоретически возможно (parallel tool calls), но в claude это редко. MCP-сервер их не блокирует друг другу: каждый id — отдельный waiter. UI показывает их в порядке прихода.

### Список изменяемых/новых файлов

| Файл | Тип | Изменения |
|---|---|---|
| `pkg/flow/flow.go` | edit | добавить `Interactive bool` (~3 строки) |
| `pkg/state/state.go` | edit | добавить `StatusAwaitingUserInput` (~1 строка) |
| `pkg/orchestrator/eventbus.go` | edit | два новых EventType (~2 строки) |
| `pkg/orchestrator/orchestrator.go` | edit | ветка `case StatusAwaitingUserInput` + метод `resumeInteractiveAgent` (~30 строк) |
| `pkg/executor/executor.go` | edit | поддержка `--session-id`/`--resume` и `--mcp-config` через расширение `Config` (~20 строк) |
| `pkg/mcp/server.go` | new | HTTP-handler `/mcp/<stage>/<phase>`, JSON-RPC MCP-протокол для одного tool `ask_user` (~200 строк) |
| `pkg/mcp/dialog.go` | new | append + чтение `dialog.jsonl`, idempotency-логика (~80 строк) |
| `pkg/server/handlers.go` | edit | новые `GET /api/stages/<id>/dialog`, `POST .../dialog/answer`, `POST .../dialog/cancel` (~50 строк) |
| `pkg/web/index.html` | edit | новая секция `#dialog-section` |
| `pkg/web/app.js` | edit | рендеринг диалога, обработка WS-событий `ask_user`/`user_answered` |
| `pkg/web/style.css` | edit | стили + анимация всплытия вопроса (30-40 строк) |

## 5. UI (web)

Vanilla JS, без фреймворков — продолжаем существующий стиль.

### Разметка

Новая секция в `detail-content`, между `actions-section` и блоком «Лог»:

```html
<div id="dialog-section" class="section hidden">
    <h3>Диалог <span id="dialog-status-badge" class="dialog-badge hidden"></span></h3>

    <div id="dialog-history" class="dialog-history">
        <!-- сюда рендерится список Q/A pairs из dialog.jsonl -->
    </div>

    <div id="dialog-pending" class="dialog-pending hidden">
        <div class="dialog-question"></div>
        <div class="dialog-options"></div>           <!-- кнопки-варианты -->
        <textarea class="dialog-custom" placeholder="Или вписать свой ответ…"></textarea>
        <div class="dialog-actions">
            <button class="btn btn-send">Отправить</button>
            <button class="btn btn-cancel-dialog">Отменить стейдж</button>
        </div>
    </div>

    <button id="dialog-toggle" class="dialog-toggle">Свернуть историю ▴</button>
</div>
```

В `stages-list` рядом с ID стейджа появляется бейдж 💬 когда статус = `awaiting_user_input`:

```html
<li><span class="status-dot ..."></span> backend-auth <span class="dialog-badge">💬</span></li>
```

### API-handlers (клиент)

```js
function loadDialog(stageID) {
    apiGet("/api/stages/" + stageID + "/dialog", function(err, json) {
        // json: [{id, phase, question, options?, allow_custom, answer?, from_options?, ts}, ...]
        renderDialogHistory(stageID, JSON.parse(json));
    });
}

function sendAnswer(stageID, qID, phase, answer, fromOptions) {
    apiPost("/api/stages/" + stageID + "/dialog/answer",
            {id: qID, phase: phase, answer: answer, from_options: fromOptions},
            ...);
}

function cancelDialog(stageID) {
    apiPost("/api/stages/" + stageID + "/dialog/cancel", null, ...);
}
```

История диалога в UI визуально сгруппирована по фазам (заголовок: «Planning», «Implementation», «Review»), но рендерится из одного списка.

### WebSocket-events

```js
case "ask_user":
    if (ev.stage_id === selectedStageID) {
        renderPendingQuestion(ev.data); // показать вопрос + варианты
        animateQuestionAppear();         // CSS transition slide-down + fade
    }
    addBadgeToStageInList(ev.stage_id);
    break;

case "user_answered":
    loadDialog(ev.stage_id);  // перечитать с сервера
    removeBadgeFromStageInList(ev.stage_id);
    break;
```

### Анимация всплытия вопроса

Блок `.dialog-pending` появляется с `transform: translateY(-8px); opacity: 0` → `transform: translateY(0); opacity: 1` через `transition: 280ms ease-out`. Варианты — кнопки с лёгким staggered fade-in (delay 40ms между ними) через `animation`. Никаких CSS-фреймворков — всё в `style.css`, 30-40 строк.

### История диалога

Список карточек `Q → A`. Свёртывание через `dialog-toggle` — последний (pending) вопрос всегда виден, история выше сворачивается. Состояние сохраняется в localStorage по ключу `dialog-history-collapsed-<stageID>`.

### Custom-ответ

Если пользователь начал печатать в textarea — кнопки-варианты затемняются (visual hint), при отправке `from_options=false`. Если кликнул вариант — `from_options=true`, textarea игнорируется.

Когда стейдж не interactive — `dialog-section` остаётся `hidden`, UI идентичен текущему. Никакой регрессии.

### REST-handlers на сервере

| Метод | Путь | Что делает |
|---|---|---|
| `GET` | `/api/stages/<id>/dialog` | Читает все `<phase>.dialog.jsonl` стейджа, склеивает в один список (с пометкой phase в каждом элементе), группирует по `(phase, qID)`, отдаёт массив в JSON в хронологическом порядке. |
| `POST` | `/api/stages/<id>/dialog/answer` | Принимает `{id, phase, answer, from_options}`, дописывает в `<phase>.dialog.jsonl`, будит waiter в MCP. |
| `POST` | `/api/stages/<id>/dialog/cancel` | Отмечает все pending waiters (всех фаз) как cancelled, переводит стейдж в `failed`. |

## 6. Тесты

Table-driven, как в проекте сейчас.

### `pkg/flow/flow_test.go` — расширение

- Parse YAML с `interactive: true` / без него / с явным `false`.
- Backwards-compat: существующие flow.yaml парсятся без изменений.

### `pkg/mcp/dialog_test.go` — новый

- Append записи Q, потом A — чтение возвращает сгруппированную пару.
- Несколько вопросов и ответов в произвольном порядке — группировка по id корректна.
- Чтение незакрытой пары (Q без A) — возвращает запись с `answer == nil`.
- Идемпотентность: повторный append того же `{id, question}` детектится как duplicate (no-op).
- Конкурентный append — `O_APPEND` достаточно, тест гоняет 10 горутин по 100 записей.

### `pkg/mcp/server_test.go` — новый

- `tools/list` возвращает `ask_user`.
- `tools/call ask_user` для нового id — блокирует, пока в waiter не прилетит ответ; тест проверяет тайминги через `time.After`.
- `tools/call ask_user` для уже отвеченного id — мгновенно возвращает результат (idempotency).
- `tools/call` после `cancel` — возвращает tool-error.

### `pkg/orchestrator/orchestrator_test.go` — расширение

- Resume стейджа в статусе `awaiting_user_input` — запускает агента через `--resume`.
- Cancel диалога переводит стейдж в `failed` и каскадно фейлит зависимые.

### `pkg/orchestrator/integration_test.go` — расширение

С fake-runner:

- Полный диалоговый цикл: stage start → agent calls ask_user → status awaiting_user_input → answer POST → status running → stage done.
- Crash mid-dialog: симулируем перезапуск orchestrator со state.json и dialog.jsonl на диске, проверяем что resume работает.
- Fake-runner, который имитирует MCP tool calls — не запускаем настоящий claude.

### `pkg/server/handlers_test.go` — расширение

- `GET /api/stages/<id>/dialog` — корректный JSON.
- `POST .../answer` без матчинга id в waiter — append всё равно делается (для late-answer случая); 200 OK.
- `POST .../cancel` для стейджа не в `awaiting_user_input` — 400.
- Path traversal в stageID — отклоняется (как уже есть для других хендлеров).

JS не имеет автотестов в проекте — для MVP проверяется ручным smoke-тестом через `make build && ./bin/flowmanager run example-flow-interactive.yaml`.

## Риски

| Риск | Митигация |
|---|---|
| claude `--print` не поддерживает `--resume` или `--session-id`. | На фазе implementation первым делом проверить exec-флаги (запустить вручную, прочитать `claude --help`). Если не поддерживает — fallback на текстовый replay в промпте (предыдущий диалог дописывается в prompt при новом запуске). Архитектура остального не меняется. |
| MCP HTTP-transport spec. | Использовать существующую Go-библиотеку (`github.com/modelcontextprotocol/go-sdk` или совместимую) вместо ручной имплементации JSON-RPC. Если подходящей нет — минимальная ручная имплементация JSON-RPC over HTTP+SSE: claude вызывает только `initialize`, `tools/list`, `tools/call` — узкая часть протокола. |
| Агент использует нестабильные id вопросов (вместо `q1, q2` пишет хэш или ts). | Системный промпт явно требует возрастающие `q1, q2, …`. Если агент всё же ошибается — duplicate-detection в MCP по `{question, options}` как fallback: найдём совпадающий незавершённый вопрос и переиспользуем его id. |
| Несколько параллельных interactive-стейджей. | URL-маршрут `/mcp/<stage-id>/<phase>` уже изолирует waiters per-stage и per-phase. EventBus уже multi-stage. UI — каждая «Диалог»-секция рендерится в своём detail-panel. |
| Race на старте: claude стартует раньше, чем HTTP-сервер готов принимать MCP-запросы. | Сейчас `server.Start()` блокирующий до листена. Гарантируем порядок: executor стартует ПОСЛЕ `server.Start()`. Это и сейчас так — порядок в `cmd/flowmanager/main.go` не меняем. |
| Старые runs без `session.json` / `dialog.jsonl`. | Файлы создаются лениво. Если стейдж не interactive — они не создаются вообще. Существующие runs продолжают работать без миграций. |

## Out of scope (явный YAGNI)

Не делаем в этой итерации:

- multi-select вопросы (выбор нескольких вариантов);
- attachments к ответам (файлы, картинки);
- инициирование диалога пользователем («хочу подкинуть агенту контекст по ходу»);
- markdown в вопросах (только plain text);
- voice/notifications;
- tool `finish_dialog` — обычные `.done`/артефакты справляются.

Если что-то из этого понадобится — добавится без переписывания: `dialog.jsonl` и MCP-схема расширяемы.
