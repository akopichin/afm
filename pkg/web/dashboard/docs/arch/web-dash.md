# [ARCHITECTURE_PLAN] web-dash

## Topic

`web-dash` — переписывание `pkg/web/dashboard` на React/Vite/TypeScript SPA с полным поведенческим паритетом
текущему `app.js`. Документ сохранён по пути `docs/arch/web-dash.md`.

Источник задачи: `docs/tasks/web-dash.md`. Существующая клеточка `.` (`pkg/web/dashboard`, контракт
`DashboardAssets`) этим планом **не изменяется** — план описывает новые клетки внутри `src/`, дочерние по
отношению к текущему `pkg/web/dashboard`.

> **Примечание о ревизии.** План переработан по итогам архитектурной проверки (`goga-review-arch`):
> (1) data-слой приведён в соответствие реальным эндпоинтам `app.js` — состояние флоу из `GET /api/status`,
> лог стадии из `GET /api/stages/{id}/log`, план из `GET /api/stages/{id}/plan`, диалог из
> `GET /api/stages/{id}/dialog`; (2) `Stage.status` развёрнут до полного реального набора статусов;
> (3) elapsed вынесен в отдельный хук `useElapsed` с собственным 1-секундным таймером; (4) добавлены
> автовыбор активной стадии, действие `dialog/cancel` и роль WS как канала обновления состояния;
> (5) добавлен раздел **Build & tooling layer** с критичными сборочными ограничениями; (6) annotations
> очищены до прохождения `goga lint` (см. Verification Checklist).

## Implementation Order

Клетки проектируются и реализуются от листьев к корню:

1. **`src/types`** — не имеет зависимостей (лист). Общие типы данных (`Stage`, `AfmEvent`, `UsagePoint`,
   `DialogQuestion`, `DialogAnswer`, `PlanComment`, `LogEntry`), используемые всеми остальными клетками.
2. **`src/hooks/use-status`** — зависит только от `src/types` (`Stage`). Poll `GET /api/status`.
3. **`src/hooks/use-event-feed`** — зависит только от `src/types` (`AfmEvent`). WebSocket `/ws`.
4. **`src/hooks/use-usage-data`** — зависит только от `src/types` (`UsagePoint`). Poll `GET /api/usage`.
5. **`src/hooks/use-stage-log`** — зависит только от `src/types` (`LogEntry`). Poll `GET /api/stages/{id}/log`.
6. **`src/hooks/use-elapsed`** — не зависит от других клеток (примитивы string/number). Собственный 1с таймер.
7. **`src/components/flow-header`** — не зависит ни от одной другой новой клеточки (только примитивы).
8. **`src/components/stages-list`** — зависит от `src/types` (`Stage`).
9. **`src/components/log-panel`** — зависит от `src/types` (`LogEntry`).
10. **`src/components/event-feed`** — зависит от `src/types` (`AfmEvent`).
11. **`src/components/plan-panel`** — зависит от `src/types` (`Stage`, `PlanComment`); содержит `MarkdownRenderer`
    (обёртка над npm-пакетом `markdown-it`, используется только здесь).
12. **`src/components/dialog-channel`** — зависит от `src/types` (`Stage`, `DialogQuestion`).
13. **`src/components/footer`** — зависит от `src/types` (`Stage`); принимает готовый `elapsedMs` от корня.
14. **`src/components/consumption-panel`** — зависит от `src/types` (`Stage`, `UsagePoint`) и от
    `src/hooks/use-usage-data` (`useUsageData`) — единственная компонентная клеточка второго уровня зависимостей.
15. **`src/app`** — корень: зависит от всех 14 клеток выше.

Клеточка `.` (`pkg/web/dashboard`, контракт `DashboardAssets`) — вне порядка реализации данного плана, её
описание и корневые конфиги сборки уточняются на стадии design/apply (см. раздел **Build & tooling layer**).

## Artifacts

Все 15 клеток — **новые** (created). Артефакт `pkg/web/dashboard` (`.`) — **существующий, не модифицируется**
данным планом (контракт `DashboardAssets` сохраняется).

### 1. `src/types` (created)

**CODEMANIFEST:**
```yaml
Usages:
  conventions: .goga/usages/conventions.md
  typescript: .goga/usages/cooks/typescript.md

Annotations: |
  Use `conventions` for code writing rules and testing. Use `typescript` for TypeScript conventions (strict
  mode, naming, single external-JSON assertion point). This cell is a deliberate deviation from the project's
  general JS/Jest conventions in favor of TypeScript + Vitest, scoped only to the dashboard rewrite. All types
  here are plain data shapes with no behavior, shared across every dashboard component cell. Re-exported
  through a barrel index.ts per the project's facade convention.

---

"Stage()":
  location: stage.ts
  annotations: |
    Данные одной стадии afm-флоу. Источник — поле stages ответа GET /api/status (объект по идентификатору
    стадии), потребляется хуком состояния флоу и передаётся в список стадий, футер и панель потребления.
    Набор значений статуса совпадает с реальными статусами стадий afm (см. statusLabels в текущем app.js):
    pending, planning, awaiting_approval, revising, ready, running, done, failed, retrying, awaiting_user_input.
  properties:
    "id -> string": |
      Уникальный идентификатор стадии.
    "name -> string": |
      Отображаемое имя стадии.
    "status -> 'pending' | 'planning' | 'awaiting_approval' | 'revising' | 'ready' | 'running' | 'done' | 'failed' | 'retrying' | 'awaiting_user_input'": |
      Текущий статус стадии из полного набора статусов afm.
    "updatedAt -> string": |
      Время последнего обновления стадии в формате ISO 8601.

"AfmEvent()":
  location: afm-event.ts
  annotations: |
    Событие, приходящее через WebSocket /ws, потребляется хуком подписки на WebSocket-соединение и
    отображается в ленте событий. Полный набор type (stage_status_changed, approved, revised, retry_scheduled,
    retry_exhausted, manual_retry, ask_user, user_answered, agent_action, agent_completed) используется корневой
    композицией как канал обновления состояния, а не только как лента для отображения. Неизвестные типы
    переносятся лентой, не обрывая её (default-ветка как в текущем app.js).
  properties:
    "type -> string": |
      Тип события из набора типов WS-событий afm.
    "payload -> unknown": |
      Произвольные данные события (поле data сервера), специфичные для type.
    "stageId -> string": |
      Идентификатор стадии, к которой относится событие (поле stage_id сервера).
    "timestamp -> string": |
      Время приёма события на клиенте в формате ISO 8601 (сервер время события не присылает).

"UsagePoint()":
  location: usage-point.ts
  annotations: |
    Точка временного ряда потребления ресурсов из GET /api/usage (с параметром metric и опционально stage),
    потребляется хуком поллинга метрик потребления и отображается в панели потребления. Сервер отдаёт объекты
    { timeBucket, value }; приведение к UsagePoint выполняется в хуке: timestamp ← timeBucket, metric ← параметр
    запроса (один на весь ряд), value ← value.
  properties:
    "timestamp -> string": |
      Бакет времени точки в RFC 3339 (поле timeBucket сервера), подпись оси X графика.
    "metric -> 'tokens' | 'cost' | 'kb'": |
      Метрика ряда (проставляется хуком по параметру запроса, одна на весь ряд).
    "value -> number": |
      Значение метрики в этой точке.

"DialogQuestion()":
  location: dialog-question.ts
  annotations: |
    Вопрос в диалоговом канале стадии — с опциями либо ожидающий свободный ответ. Источник — GET
    /api/stages/{id}/dialog (последняя запись с answer=null; поле question сервера).
  properties:
    "id -> string": |
      Уникальный идентификатор вопроса (поле id сервера).
    "phase -> string": |
      Фаза диалога (поле phase сервера), для группировки истории.
    "text -> string": |
      Текст вопроса (поле question сервера).
    "options -> Array<string>": |
      Предопределённые опции ответа (массив строк сервера — label одновременно значение); пустой массив —
      ожидается только свободный текст.
    "allowCustom -> boolean": |
      Разрешён ли свободный ответ (поле allow_custom сервера).

"DialogAnswer()":
  location: dialog-answer.ts
  annotations: |
    Ответ пользователя на вопрос диалогового канала — выбор опции или свободный текст. Отправляется POST
    /api/stages/{id}/dialog/answer.
  properties:
    "questionId -> string": |
      Идентификатор вопроса, на который дан ответ.
    "value -> string": |
      Значение ответа: идентификатор опции либо введённый текст.

"PlanComment()":
  location: plan-comment.ts
  annotations: |
    Комментарий к строке плана, создаётся в диалоговом канале, отображается в панели плана. Один и тот же
    импортированный тип используется и для создания, и для отображения; координация между клетками идёт через
    корневую композицию.
  properties:
    "line -> number": |
      Номер строки плана.
    "text -> string": |
      Текст комментария.

"LogEntry()":
  location: log-entry.ts
  annotations: |
    Строка операционного лога выбранной стадии. Источник — GET /api/stages/{id}/log (отдельный поллинг, не
    WebSocket-события), отображается в окне лога.
  properties:
    "timestamp -> string": |
      Время записи в формате ISO 8601.
    "message -> string": |
      Текст сообщения.
    "level -> 'debug' | 'info' | 'warn' | 'error'": |
      Уровень важности записи.

---

Author: Goga
CreatedAt: 10/07/26
Description: |
  Общие типы данных дашборда afm.
```

**.usages/ files:**

`src/types/.usages/dashboard-data-types.md`:
```md
Domain: общие типы данных дашборда afm (`Stage`, `AfmEvent`, `UsagePoint`, `DialogQuestion`, `DialogAnswer`,
`PlanComment`, `LogEntry`) — для клеток-хуков и клеток-компонентов, потребляющих данные бэкенда.

## Источники данных (соответствие app.js)

- `Stage` — поле stages ответа `GET /api/status` (объект, ключ — id стадии); там же `flow_name` и `started_at`.
- `LogEntry` — `GET /api/stages/{id}/log` (поллинг 3с).
- `UsagePoint` — `GET /api/usage?metric=...&stage=...`.
- `AfmEvent` — WebSocket `/ws`.
- План и диалог — `GET /api/stages/{id}/plan` и `GET /api/stages/{id}/dialog`.

Полный набор статусов стадии: pending, planning, awaiting_approval, revising, ready, running, done, failed,
retrying, awaiting_user_input (done — завершена, не completed).

## Импорт типов

Все типы реэкспортируются через barrel index.ts клеточки:

```ts
import type { Stage, AfmEvent, UsagePoint } from '../../types'
```

## Типизация ответа fetch

Типы описывают форму уже распарсенного JSON — единственная точка приведения типа должна находиться в
вызывающей клеточке (хуке), а не здесь:

```ts
// FlowStatus объявлен в хуке use-status (не в этой клеточке типов) и описывает
// уже нормализованный результат.
async function fetchStatus(): Promise<FlowStatus> {
  const response = await fetch('/api/status')
  const data: unknown = await response.json()
  return data as FlowStatus
}
```

## Использование union-типов статусов/метрик/уровней

`Stage.status`, `UsagePoint.metric`, `LogEntry.level` — строковые литеральные union-типы, пригодны для
switch/сравнения без дополнительной валидации:

```ts
function isActive(status: Stage['status']): boolean {
  return status === 'planning' || status === 'running' || status === 'revising'
    || status === 'retrying' || status === 'awaiting_user_input'
}
```
```

### 2. `src/hooks/use-status` (created)

**CODEMANIFEST:**
```yaml
Imports:
  - Types: [Stage]
    Usages: [dashboard-data-types]
    From: src/types

Usages:
  conventions: .goga/usages/conventions.md
  react: .goga/usages/cooks/react.md
  typescript: .goga/usages/cooks/typescript.md

Annotations: |
  Use `conventions` for code writing rules and testing. Use `react` for hook structure and the cancelled-flag
  guard under React StrictMode. Use `typescript` for the single assertion point for external JSON. Use `Stage`
  from Imports as the stage shape. Use `dashboard-data-types` from Imports for guidance on the /api/status
  source and the full status set.

---

"useStatus() -> status: { flowName: string, stages: Stage[], startedAt: string, refresh: () => void }":
  location: use-status.ts
  annotations: |
    Хук состояния флоу через GET /api/status: периодический опрос плюс возможность немедленного обновления.
    Возвращает объект `status` — единый источник этих данных для всей композиции (как loadState в текущем
    app.js). Поля: flowName — имя флоу (поле flow_name ответа), stages — список стадий (нормализованный из
    объекта по id с учётом stage_order и stage_names, как в app.js), startedAt — время старта в ISO 8601,
    refresh — функция немедленного ре-запроса состояния.

    `refresh`: триггер немедленного обновления; вызывается корневой композицией при значимых WebSocket-событиях
    (WS работает как канал обновления состояния — см. корневую клеточку App)

    Algorithm:
    1. При монтировании выполнить запрос GET /api/status
    2. Распарсить JSON, нормализовать к форме status (см. typescript): stages — объект по id → массив в порядке
       stage_order (fallback Object.keys(stages).sort), имена — из stage_names, время — из updated_at
    3. Если хук не отменён — обновить состояние
    4. Запланировать повторный опрос по таймеру (3000мс)
    5. При размонтировании — выставить флаг отмены и остановить таймер

    Requirements:
    - Использовать флаг отмены для игнорирования устаревших ответов после размонтирования
    - stages — пустой массив до первого успешного ответа, а не undefined/null
    - refresh() инициирует внеочередной запрос, не дожидаясь таймера (источник правды для WS-обновлений)

    Constraints:
    - Не запускать параллельные пересекающиеся запросы поверх уже запланированного таймера

---

Author: Goga
CreatedAt: 10/07/26
Description: |
  Хук состояния флоу (GET /api/status).
```

**.usages/ files:**

`src/hooks/use-status/.usages/status.md`:
```md
Domain: подключение периодического опроса состояния флоу (GET /api/status) к React-компонентам дашборда.

## Базовое использование

```tsx
import { useStatus } from '../../hooks/use-status'
import { StagesList } from '../../components/stages-list'
import { FlowHeader } from '../../components/flow-header'

function Dashboard() {
  const { flowName, stages, startedAt } = useStatus()
  return (
    <>
      <FlowHeader flowName={flowName} connected={false} />
      <StagesList stages={stages} selectedStageId={null} onSelect={() => {}} />
    </>
  )
}
```

## Особенности

- Хук сам управляет своим жизненным циклом (запуск/остановка таймера, отмена устаревших ответов) — вызывающему
  компоненту не нужен дополнительный useEffect вокруг него.
- Список stages — пустой массив до первого успешного ответа, а не undefined/null.
- Это единственный источник flowName, stages и startedAt в композиции (другие клетки их не запрашивают).
```

### 3. `src/hooks/use-event-feed` (created)

**CODEMANIFEST:**
```yaml
Imports:
  - Types: [AfmEvent]
    Usages: [dashboard-data-types]
    From: src/types

Usages:
  conventions: .goga/usages/conventions.md
  react: .goga/usages/cooks/react.md
  typescript: .goga/usages/cooks/typescript.md

Annotations: |
  Use `conventions` for code writing rules and testing. Use `react` for the WebSocket hook structure with
  exponential-backoff reconnect. Use `typescript` for the single type-assertion point for inbound message
  data. Use `AfmEvent` from Imports. Use `dashboard-data-types` from Imports for guidance on the WS event
  types and their role as a state-refresh channel.

---

"useEventFeed(url: string) -> feed: { events: AfmEvent[], connected: boolean }":
  location: use-event-feed.ts
  annotations: |
    Хук подписки на WebSocket /ws с автопереключением, возвращает накопленные события и статус соединения.

    `url`: адрес WebSocket-эндпоинта

    Возвращает объект `feed`: events — накопленный список событий, connected — true, если соединение сейчас
    открыто.

    Algorithm:
    1. При монтировании открыть WebSocket по url
    2. При открытии соединения — connected true, сброс задержки переподключения
    3. При входящем сообщении — распарсить JSON, привести к AfmEvent, добавить в events
    4. При закрытии соединения — connected false; если не отменён — запланировать переподключение, удвоить
       задержку с ограничением сверху
    5. При размонтировании — отмена, остановка таймера, закрытие соединения

    Requirements:
    - Начальная задержка 1000мс, удвоение, потолок 10000мс; сброс к начальной после успешного открытия
    - Единственная точка приведения типа для данных входящего сообщения
    - events ограничены 200 последними записями (как обрезка $feedContent в текущем app.js)
    - Значимые события (смена статуса стадии, approved, revised, retry_scheduled, retry_exhausted, manual_retry,
      ask_user, user_answered, agent_completed и т.д.) — канал обновления состояния: корневая композиция по ним
      ре-запрашивает состояние флоу и ре-рендерит

    Constraints:
    - Не открывать более одного активного соединения на один вызов хука

---

Author: Goga
CreatedAt: 10/07/26
Description: |
  Хук WebSocket-подписки с backoff-реконнектом.
```

**.usages/ files:**

`src/hooks/use-event-feed/.usages/event-feed.md`:
```md
Domain: подключение WebSocket-ленты событий afm с индикацией статуса соединения к React-компонентам дашборда.

## Базовое использование

```tsx
import { useEventFeed } from '../../hooks/use-event-feed'
import { FlowHeader } from '../../components/flow-header'
import { EventFeedPanel } from '../../components/event-feed'

function Dashboard({ flowName }: { flowName: string }) {
  const { events, connected } = useEventFeed('/ws')
  return (
    <>
      <FlowHeader flowName={flowName} connected={connected} />
      <EventFeedPanel events={events} />
    </>
  )
}
```

## Особенности

- connected переключается в false сразу при разрыве и обратно в true только после успешного переподключения —
  подходит напрямую для индикатора LINK/OFFLINE.
- events — растущий список за всё время жизни компонента (не сбрасывается при переподключении).
- Один вызов хука = одно WebSocket-соединение; для нескольких независимых лент вызывать хук отдельно на каждый
  url.
- WS-события в afm несут не только ленту для отображения, но и сигнал обновить состояние (смена статуса стадии,
  approved/revised/retry/ask_user) — корневая композиция по значимым событиям ре-запрашивает состояние флоу.
```

### 4. `src/hooks/use-usage-data` (created)

**CODEMANIFEST:**
```yaml
Imports:
  - Types: [UsagePoint]
    Usages: [dashboard-data-types]
    From: src/types

Usages:
  conventions: .goga/usages/conventions.md
  react: .goga/usages/cooks/react.md
  typescript: .goga/usages/cooks/typescript.md

Annotations: |
  Use `conventions` for code writing rules and testing. Use `react` for the cancelled-flag guard. Use
  `typescript` for the single assertion point for external JSON. Use `UsagePoint` from Imports. Use
  `dashboard-data-types` from Imports for guidance on consuming `UsagePoint`.

---

"useUsageData(metric: string, stageFilter: string | null) -> points: UsagePoint[]":
  location: use-usage-data.ts
  annotations: |
    Хук поллинга GET /api/usage для заданной метрики и (опционально) фильтра по стадии.

    `metric`: запрашиваемая метрика
    `stageFilter`: идентификатор стадии либо null
    `points`: список точек временного ряда

    Algorithm:
    1. При монтировании и изменении metric/stageFilter выполнить запрос GET /api/usage с этими параметрами
    2. Распарсить JSON (массив объектов { timeBucket, value }); привести к UsagePoint: timestamp ← timeBucket,
       metric ← параметр запроса, value ← value
    3. Если не отменён — обновить состояние
    4. Запланировать повторный опрос (интервал 10000мс, как usageRefreshTick в текущем app.js)
    5. При размонтировании или смене metric/stageFilter — отмена, остановка таймера

    Requirements:
    - Пустой массив в ответе — валидный результат, без интерпретации на уровне хука (интерпретацию «скрыть
      метрику деньги при пустом ответе» выполняет клеточка панели потребления)
    - Живой апдейт временного ряда каждые 10000мс, пока хук смонтирован

    Constraints:
    - Не выполнять новый запрос по таймеру, если metric/stageFilter не изменились — следующий тик использует те же параметры

---

Author: Goga
CreatedAt: 10/07/26
Description: |
  Хук поллинга метрик потребления.
```

**.usages/ files:**

`src/hooks/use-usage-data/.usages/usage-data.md`:
```md
Domain: подключение поллинга метрик потребления ресурсов (GET /api/usage) к панели потребления дашборда.

## Базовое использование

```tsx
import { useUsageData } from '../../hooks/use-usage-data'

function ConsumptionChart({ metric, stageFilter }: { metric: string; stageFilter: string | null }) {
  const points = useUsageData(metric, stageFilter)
  return <UsageChart points={points} />
}
```

## Проверка доступности метрики «деньги»

Хук не решает, показывать ли переключатель метрики — это ответственность вызывающей клеточки. Проверка «есть ли
данные по cost» выполняется тем же хуком с metric='cost', интерпретация пустого ответа — на стороне потребителя:

```tsx
const costPoints = useUsageData('cost', null)
const costAvailable = costPoints.length > 0
```

## Особенности

- points — пустой массив до первого ответа или если у бэкенда нет данных для этой метрики/фильтра.
- Смена stageFilter на null означает «без фильтра», а не «не загружать данные».
```

### 5. `src/hooks/use-stage-log` (created)

**CODEMANIFEST:**
```yaml
Imports:
  - Types: [LogEntry]
    Usages: [dashboard-data-types]
    From: src/types

Usages:
  conventions: .goga/usages/conventions.md
  react: .goga/usages/cooks/react.md
  typescript: .goga/usages/cooks/typescript.md

Annotations: |
  Use `conventions` for code writing rules and testing. Use `react` for the cancelled-flag guard and for
  restarting the timer when stageId changes. Use `typescript` for the single assertion point for external JSON.
  Use `LogEntry` from Imports. Use `dashboard-data-types` from Imports for guidance on the /api/stages/{id}/log
  source.

---

"useStageLog(stageId: string | null) -> entries: LogEntry[]":
  location: use-stage-log.ts
  annotations: |
    Хук поллинга операционного лога выбранной стадии через GET /api/stages/{stageId}/log.

    `stageId`: идентификатор выбранной стадии либо null
    `entries`: список строк лога

    Algorithm:
    1. При монтировании и изменении stageId выполнить запрос GET /api/stages/{stageId}/log
    2. Прочитать ответ как текст; оставить только строки вида «HH:MM:SS text …» (фильтрация tool/banner-строк
       как в renderLog текущего app.js) и разобрать их в LogEntry (timestamp, message, level='info')
    3. Если не отменён — обновить состояние
    4. Запланировать повторный опрос (интервал 3000мс)
    5. При размонтировании или смене stageId — отмена, остановка таймера

    Requirements:
    - stageId равный null — пустой массив entries без запроса (нет выбранной стадии)
    - Эндпоинт лога отдаёт plain text, не JSON — парсинг строковый, единственная точка приведения типа
    - Использовать флаг отмены для игнорирования устаревших ответов
    - Источник лога — отдельный поллинг этого эндпоинта, а не фильтрация WebSocket-событий

    Constraints:
    - Не выполнять запрос и не держать таймер, пока stageId равен null
    - В текущем app.js таймер лога ставится только для активных статусов стадии
      (planning/running/revising/retrying/awaiting_user_input); хук опрашивает безусловно — осознанное
      упрощение (дополнительные запросы для завершённых стадий), зафиксировано как известное отклонение от app.js

---

Author: Goga
CreatedAt: 10/07/26
Description: |
  Хук поллинга лога стадии (GET /api/stages/{id}/log).
```

**.usages/ files:**

`src/hooks/use-stage-log/.usages/stage-log.md`:
```md
Domain: подключение поллинга операционного лога выбранной стадии (GET /api/stages/{id}/log) к панели деталей.

## Базовое использование

```tsx
import { useStageLog } from '../../hooks/use-stage-log'
import { LogPanel } from '../../components/log-panel'

function DetailLog({ stageId }: { stageId: string | null }) {
  const entries = useStageLog(stageId)
  return <LogPanel entries={entries} />
}
```

## Особенности

- Лог стадии — отдельный источник данных (поллинг этого эндпоинта каждые 3000мс), а не производная от ленты
  WebSocket-событий; это совпадает с поведением loadLog в текущем app.js.
- entries — пустой массив, пока stageId равен null или лог ещё не поступил.
```

### 6. `src/hooks/use-elapsed` (created)

**CODEMANIFEST:**
```yaml
Usages:
  conventions: .goga/usages/conventions.md
  react: .goga/usages/cooks/react.md
  typescript: .goga/usages/cooks/typescript.md

Annotations: |
  Use `conventions` for code writing rules and testing. Use `react` for the interval-based effect and the
  cleanup on unmount. Use `typescript` for typing.

---

"useElapsed(startedAt: string) -> elapsedMs: number":
  location: use-elapsed.ts
  annotations: |
    Хук секундомера прошедшего времени от startedAt; тикает собственным интервалом раз в секунду.

    `startedAt`: время старта флоу в ISO 8601
    `elapsedMs`: прошедшее время в миллисекундах от startedAt до текущего момента

    Algorithm:
    1. При монтировании вычислить elapsedMs как разницу текущего времени и startedAt
    2. Запустить интервал 1000мс, на каждом тике перевычислять elapsedMs
    3. При размонтировании — остановить интервал

    Requirements:
    - Собственный односекундный интервал (как elapsedTimer в текущем app.js), не привязанный к циклу поллинга
      стадий, чтобы счётчик обновлялся плавно каждую секунду
    - При пустом/невалидном startedAt elapsedMs = 0; отображение «--» в этом случае — ответственность
      потребителя (футера), как updateElapsed в текущем app.js

    Constraints:
    - Не зависит от ре-рендера родителя для обновления значения

---

Author: Goga
CreatedAt: 10/07/26
Description: |
  Хук секундомера прошедшего времени (1с интервал).
```

**.usages/ files:**

`src/hooks/use-elapsed/.usages/elapsed.md`:
```md
Domain: секундомер прошедшего от старта времени для футера дашборда.

## Базовое использование

```tsx
import { useElapsed } from '../../hooks/use-elapsed'
import { Footer } from '../../components/footer'

function DashboardFooter({ stages, startedAt }: { stages: Stage[]; startedAt: string }) {
  const elapsedMs = useElapsed(startedAt)
  return <Footer stages={stages} elapsedMs={elapsedMs} />
}
```

## Особенности

- Хук сам держит односекундный интервал и перевычисляет значение каждую секунду — счётчик не «застывает» между
  тиками поллинга стадий (полное соответствие elapsedTimer в текущем app.js).
- Если startedAt пустой/невалидный — elapsedMs равен 0.
```

### 7. `src/components/flow-header` (created)

**CODEMANIFEST:**
```yaml
Usages:
  conventions: .goga/usages/conventions.md
  react: .goga/usages/cooks/react.md
  typescript: .goga/usages/cooks/typescript.md

Annotations: |
  Use `conventions` for code writing rules and testing. Use `react` — only functional components, no state
  management library. Use `typescript` for naming and strict typing.

---

"FlowHeader(flowName: string, connected: boolean) -> element: ReactElement":
  location: FlowHeader.tsx
  annotations: |
    Шапка дашборда: название флоу и индикатор состояния WebSocket-соединения.

    `flowName`: отображаемое имя флоу
    `connected`: true, если соединение открыто
    `element`: разметка шапки

    Requirements:
    - Индикатор отображает текст LINK, когда connected истинно, и OFFLINE — когда ложно
    - Разметка сохраняет id flow-name и id ws-status для совместимости с темой goga

---

Author: Goga
CreatedAt: 10/07/26
Description: |
  Шапка дашборда с именем флоу и индикатором WS-соединения.
```

**.usages/ files:**

`src/components/flow-header/.usages/flow-header.md`:
```md
Domain: рендер шапки дашборда (имя флоу + индикатор WebSocket-соединения) для корневой композиции App.

## Базовое использование

```tsx
import { FlowHeader } from '../../components/flow-header'
import { useEventFeed } from '../../hooks/use-event-feed'

function App({ flowName }: { flowName: string }) {
  const { connected } = useEventFeed('/ws')
  return <FlowHeader flowName={flowName} connected={connected} />
}
```

## Особенности

- Чистый презентационный компонент — не открывает соединений и не хранит состояние сам, только отображает
  переданные flowName/connected.
- Значение connected напрямую пробрасывается из useEventFeed, без промежуточной трансформации.
```

### 8. `src/components/stages-list` (created)

**CODEMANIFEST:**
```yaml
Imports:
  - Types: [Stage]
    Usages: [dashboard-data-types]
    From: src/types

Usages:
  conventions: .goga/usages/conventions.md
  react: .goga/usages/cooks/react.md
  typescript: .goga/usages/cooks/typescript.md

Annotations: |
  Use `conventions` for code writing rules and testing. Use `react` — only functional components. Use
  `typescript` for naming and strict typing. Use `Stage` from Imports. Use `dashboard-data-types` from
  Imports for guidance on consuming `Stage`.

---

"StagesList(stages: Stage[], selectedStageId: string | null, onSelect: (stageId: string) -> void) -> element: ReactElement":
  location: StagesList.tsx
  annotations: |
    Список стадий с возможностью выбора активной.

    `stages`: список стадий
    `selectedStageId`: id выбранной стадии либо null
    `onSelect`: коллбэк выбора
    `element`: разметка

    Requirements:
    - Стадия с идентификатором, равным selectedStageId, выделяется классом selected
    - Сохраняется id stages-list
    - Отображаемые подписи статусов берутся из полного набора статусов afm (см. dashboard-data-types)

---

Author: Goga
CreatedAt: 10/07/26
Description: |
  Список стадий с выбором активной.
```

**.usages/ files:**

`src/components/stages-list/.usages/stages-list.md`:
```md
Domain: рендер списка стадий с выбором активной стадии для корневой композиции App.

## Базовое использование

```tsx
import { useState } from 'react'
import { StagesList } from '../../components/stages-list'
import { useStatus } from '../../hooks/use-status'

function StagesContainer() {
  const { stages } = useStatus()
  const [selectedStageId, setSelectedStageId] = useState<string | null>(null)

  return (
    <StagesList stages={stages} selectedStageId={selectedStageId} onSelect={setSelectedStageId} />
  )
}
```

## Особенности

- Чистый презентационный компонент — не опрашивает состояние сам, ожидает готовый список stages от вызывающей
  клеточки.
- onSelect вызывается синхронно при клике, без debounce/throttle.
```

### 9. `src/components/log-panel` (created)

**CODEMANIFEST:**
```yaml
Imports:
  - Types: [LogEntry]
    Usages: [dashboard-data-types]
    From: src/types

Usages:
  conventions: .goga/usages/conventions.md
  react: .goga/usages/cooks/react.md
  typescript: .goga/usages/cooks/typescript.md

Annotations: |
  Use `conventions` for code writing rules and testing. Use `react` — only functional components. Use
  `typescript` for naming and strict typing. Use `LogEntry` from Imports. Use `dashboard-data-types` from
  Imports for guidance on consuming `LogEntry`.

---

"LogPanel(entries: LogEntry[]) -> element: ReactElement":
  location: LogPanel.tsx
  annotations: |
    Окно операционного лога выбранной стадии.

    `entries`: список строк лога
    `element`: разметка лога

    Requirements:
    - Хронологический порядок, без пересортировки
    - Уровень записи визуально различим

---

Author: Goga
CreatedAt: 10/07/26
Description: |
  Окно операционного лога стадии.
```

**.usages/ files:**

`src/components/log-panel/.usages/log-panel.md`:
```md
Domain: рендер окна операционного лога стадии для панели деталей дашборда.

## Базовое использование

```tsx
import { LogPanel } from '../../components/log-panel'
import { useStageLog } from '../../hooks/use-stage-log'

function DetailLog({ stageId }: { stageId: string | null }) {
  const entries = useStageLog(stageId)
  return <LogPanel entries={entries} />
}
```

## Особенности

- Чистый презентационный компонент — не хранит и не сортирует записи сам, полностью доверяет порядку entries.
- Пустой массив entries — валидное состояние (лог ещё не поступал или стадия не выбрана), отображается как
  пустой список без ошибки.
```

### 10. `src/components/event-feed` (created)

**CODEMANIFEST:**
```yaml
Imports:
  - Types: [AfmEvent]
    Usages: [dashboard-data-types]
    From: src/types

Usages:
  conventions: .goga/usages/conventions.md
  react: .goga/usages/cooks/react.md
  typescript: .goga/usages/cooks/typescript.md

Annotations: |
  Use `conventions` for code writing rules and testing. Use `react` — only functional components. Use
  `typescript` for naming and strict typing. Use `AfmEvent` from Imports. Use `dashboard-data-types` from
  Imports for guidance on consuming `AfmEvent`.

---

"EventFeedPanel(events: AfmEvent[]) -> element: ReactElement":
  location: EventFeedPanel.tsx
  annotations: |
    Лента событий флоу.

    `events`: список событий
    `element`: разметка ленты

    Requirements:
    - Хронологический порядок, без пересортировки
    - Тип события визуально различим

---

Author: Goga
CreatedAt: 10/07/26
Description: |
  Лента событий флоу.
```

**.usages/ files:**

`src/components/event-feed/.usages/event-feed-panel.md`:
```md
Domain: рендер ленты событий флоу для корневой композиции App.

## Базовое использование

```tsx
import { EventFeedPanel } from '../../components/event-feed'
import { useEventFeed } from '../../hooks/use-event-feed'

function App() {
  const { events } = useEventFeed('/ws')
  return <EventFeedPanel events={events} />
}
```

## Особенности

- Чистый презентационный компонент — растущий список events рендерится как есть, без виртуализации/пагинации
  (как в текущем app.js).
```

### 11. `src/components/plan-panel` (created)

**CODEMANIFEST:**
```yaml
Imports:
  - Types: [Stage, PlanComment]
    Usages: [dashboard-data-types]
    From: src/types

Usages:
  conventions: .goga/usages/conventions.md
  react: .goga/usages/cooks/react.md
  typescript: .goga/usages/cooks/typescript.md

Annotations: |
  Use `conventions` for code writing rules and testing. Use `react` (раздел markdown-it фиксирует конфиг и
  переход на npm-зависимость; поведение режима ревью плана перенесено из renderPlanReview/formatLine/
  decorateCheckboxes текущего app.js). Use `typescript` for naming and strict typing. Use `Stage`, `PlanComment`
  from Imports. Use `dashboard-data-types` from Imports for guidance on consuming `Stage`/`PlanComment`.

---

"MarkdownRenderer(source: string) -> element: ReactElement":
  location: MarkdownRenderer.tsx
  annotations: |
    Обёртка над npm-пакетом markdown-it (см. react).

    `source`: markdown-текст
    `element`: разметка с HTML

    Requirements:
    - Конфигурация парсера html=false, linkify=true
    - Экземпляр создаётся один раз на модуль

    Constraints:
    - Никакой другой сторонней библиотеки рендера markdown, кроме npm-пакета markdown-it, не используется

"PlanPanel(stage: Stage) -> element: ReactElement":
  location: PlanPanel.tsx
  annotations: |
    Панель плана стадии: загрузка markdown плана, рендер (обычный или режим ревью), действия Approve/Send
    revision/Retry, комментарии к строкам плана. Поведение перенесено из loadPlan/renderPlanReview/
    onPlanLineClick текущего app.js.

    `stage`: выбранная стадия (полный объект — статус определяет режим ревью, видимость действий и mark-done)
    `element`: разметка панели

    Разметка плана загружается запросом GET /api/stages/{stage.id}/plan (plain text) и рендерится: в обычном
    режиме — через MarkdownRenderer, в режиме ревью (stage.status === 'awaiting_approval') — построчно с
    номерами строк и комментариями.

    Requirements:
    - Режим ревью (awaiting_approval): спецсекции «## Assumptions»/«## Acceptance Criteria» сворачиваемые, каждая
      строка с номером, чекбоксы [x]/[ ] стилизованы, code-блоки (```) отдельными блоками; клик по строке
      открывает форму комментария
    - При stage.status === 'done' все «- [ ]» заменяются на «- [x]» (mark-all-done, как в текущем app.js)
    - Действия Approve/Send revision показываются только для awaiting_approval; Retry — только для failed
  properties:
    "planMarkdown -> string": |
      Markdown-текст плана, загруженный из GET /api/stages/{stage.id}/plan (внутреннее состояние).
    "comments -> PlanComment[]": |
      Комментарии к строкам плана (внутреннее клиентское состояние режима ревью; накапливаются и отправляются
      одним feedback-полем ревизии, а не через отдельный API комментариев).
  methods:
    "approve() -> done:Promise<void>": |
      Отправляет подтверждение плана (POST /api/stages/{stage.id}/approve).

      Обновление состояния — через хук состояния флоу, без оптимистичного апдейта.
    "sendRevision() -> done:Promise<void>": |
      Отправляет запрос на доработку (POST /api/stages/{stage.id}/revise). Собирает feedback из накопленных
      комментариев к строкам (формат «Line N: text», как buildFeedbackString в текущем app.js).
    "retry() -> done:Promise<void>": |
      Запрашивает повтор стадии (POST /api/stages/{stage.id}/retry).

---

Author: Goga
CreatedAt: 10/07/26
Description: |
  Панель плана стадии с markdown и действиями Approve/Revise/Retry.
```

**.usages/ files:**

`src/components/plan-panel/.usages/plan-panel.md`:
```md
Domain: рендер панели плана (загрузка markdown, действия Approve/Revise/Retry, комментарии к строкам) для
панели деталей дашборда.

## Базовое использование

```tsx
import { PlanPanel } from '../../components/plan-panel'

function DetailPanel({ stageId }: { stageId: string }) {
  return <PlanPanel stageId={stageId} />
}
```

## Рендер markdown отдельно

```tsx
import { MarkdownRenderer } from '../../components/plan-panel'

<MarkdownRenderer source="# Заголовок плана" />
```

## Особенности

- Панель сама загружает markdown плана по выбранной стадии (GET /api/stages/{id}/plan), как loadPlan в текущем
  app.js; planMarkdown — её внутреннее состояние.
- approve/sendRevision/retry — асинхронные сетевые операции; после успешного действия состояние флоу обновляется
  через хук состояния, не оптимистично.
- comments — внутреннее клиентское состояние режима ревью (awaiting_approval): накапливаются по клику на строки
  плана и отправляются одним полем feedback в sendRevision (как buildFeedbackString в app.js), отдельного API
  комментариев нет.
```

### 12. `src/components/dialog-channel` (created)

**CODEMANIFEST:**
```yaml
Imports:
  - Types: [Stage, DialogQuestion]
    Usages: [dashboard-data-types]
    From: src/types

Usages:
  conventions: .goga/usages/conventions.md
  react: .goga/usages/cooks/react.md
  typescript: .goga/usages/cooks/typescript.md

Annotations: |
  Use `conventions` for code writing rules and testing. Use `react`. Use `typescript` for naming and strict
  typing. Use `Stage`, `DialogQuestion` from Imports. Use `dashboard-data-types` from Imports for guidance on
  consuming these types and the /api/stages/{id}/dialog source.

---

"DialogChannel(stage: Stage) -> element: ReactElement":
  location: DialogChannel.tsx
  annotations: |
    Диалоговый канал стадии: история вопросов/ответов по фазам, текущий вопрос (опции и/или свободный ответ),
    отмена. Поведение перенесено из loadDialog/renderDialog/renderPendingQuestion текущего app.js.

    `stage`: выбранная стадия (полный объект — статус влияет на видимость канала)
    `element`: разметка канала

    История и текущий вопрос загружаются запросом GET /api/stages/{stage.id}/dialog (JSON-массив записей с
    полями phase/type/question/answer/id/options/allow_custom). Текущий вопрос — последняя запись с answer=null
    (записи agent_text вопросом не считаются).

    Requirements:
    - История группируется по phase (разделители фаз); записи agent_text рендерятся как сообщения агента
    - Опции currentQuestion — массив строк (label одновременно является значением ответа); allow_custom разрешает
      свободный текст; выбор опции и ввод текста взаимоисключающие
    - Видимость: канал показан, если есть история либо stage.status === 'awaiting_user_input'
  properties:
    "history -> DialogQuestion[]": |
      История вопросов диалога, загруженная из GET /api/stages/{stage.id}/dialog.
    "currentQuestion -> DialogQuestion | null": |
      Активный вопрос (последняя запись без ответа) либо null.
  methods:
    "answer(questionId: string, value: string, fromOptions: boolean) -> done:Promise<void>": |
      Отправляет ответ (POST /api/stages/{stage.id}/dialog/answer) телом { id, phase, answer, from_options }:
      либо выбранная опция (fromOptions=true, value — строка опции), либо свободный текст (fromOptions=false).

      `questionId`: идентификатор вопроса (поле id текущего вопроса)
      `value`: значение ответа — строка выбранной опции либо введённый текст
      `fromOptions`: true, если value — выбранная опция; false — свободный текст
    "cancel() -> done:Promise<void>": |
      Отменяет текущий диалог (POST /api/stages/{stage.id}/dialog/cancel) — соответствует кнопке отмены в
      текущем app.js.

---

Author: Goga
CreatedAt: 10/07/26
Description: |
  Диалоговый канал стадии.
```

**.usages/ files:**

`src/components/dialog-channel/.usages/dialog-channel.md`:
```md
Domain: рендер и управление диалоговым каналом стадии (история вопросов/ответов, ответы, отмена, комментарии к
строкам плана) для панели деталей дашборда.

## Базовое использование

```tsx
import { DialogChannel } from '../../components/dialog-channel'

function DetailPanel({ stageId }: { stageId: string }) {
  return <DialogChannel stageId={stageId} />
}
```

## Особенности

- Выбор опции и ввод свободного текста — взаимоисключающие способы ответить на currentQuestion; ответ
  отправляется одним вызовом answer (POST /api/stages/{id}/dialog/answer телом { id, phase, answer, from_options }).
- cancel отменяет текущий диалог (POST /api/stages/{id}/dialog/cancel) — соответствует кнопке отмены в текущем
  app.js.
- Комментирование строк плана сюда не относится — это режим ревью панели плана (PlanPanel), а не диалоговый канал.
```

### 13. `src/components/footer` (created)

**CODEMANIFEST:**
```yaml
Imports:
  - Types: [Stage]
    Usages: [dashboard-data-types]
    From: src/types

Usages:
  conventions: .goga/usages/conventions.md
  react: .goga/usages/cooks/react.md
  typescript: .goga/usages/cooks/typescript.md

Annotations: |
  Use `conventions` for code writing rules and testing. Use `react`. Use `typescript` for naming and strict
  typing. Use `Stage` from Imports. Use `dashboard-data-types` from Imports for guidance on consuming `Stage`.

---

"Footer(stages: Stage[], elapsedMs: number) -> element: ReactElement":
  location: Footer.tsx
  annotations: |
    Футер: прогресс, время старта, elapsed.

    `stages`: список стадий для прогресса
    `elapsedMs`: прошедшее время в миллисекундах от старта (готовое значение от хука секундомера)
    `element`: разметка футера

    Requirements:
    - Прогресс — доля стадий со статусом done от общего числа stages
    - startedAt и elapsed отображаются в человекочитаемом формате; elapsed пересчитывается вызывающим хуком
      секундомера каждую секунду (см. useElapsed)

---

Author: Goga
CreatedAt: 10/07/26
Description: |
  Футер дашборда.
```

**.usages/ files:**

`src/components/footer/.usages/footer.md`:
```md
Domain: рендер футера дашборда (прогресс/старт/elapsed) для корневой композиции App.

## Базовое использование

```tsx
import { Footer } from '../../components/footer'
import { useStatus } from '../../hooks/use-status'
import { useElapsed } from '../../hooks/use-elapsed'

function App() {
  const { stages, startedAt } = useStatus()
  const elapsedMs = useElapsed(startedAt)
  return <Footer stages={stages} elapsedMs={elapsedMs} />
}
```

## Особенности

- Footer — чистый презентационный компонент; elapsed приходит уже готовым (elapsedMs) от хука useElapsed,
  который сам тикает каждую секунду. Таким образом elapsed обновляется плавно раз в секунду (полное
  соответствие elapsedTimer в текущем app.js), а не только при ре-рендере родителя.
- Прогресс считается по стадиям со статусом done.
```

### 14. `src/components/consumption-panel` (created)

**CODEMANIFEST:**
```yaml
Imports:
  - Types: [Stage, UsagePoint]
    Usages: [dashboard-data-types]
    From: src/types
  - Types: [useUsageData]
    Usages: [usage-data]
    From: src/hooks/use-usage-data

Usages:
  conventions: .goga/usages/conventions.md
  react: .goga/usages/cooks/react.md
  typescript: .goga/usages/cooks/typescript.md

Annotations: |
  Use `conventions` for code writing rules and testing. Use `react`. Use `typescript` for naming and strict
  typing. Use `useUsageData` from Imports. Use `Stage`, `UsagePoint` from Imports. Use `dashboard-data-types`
  from Imports for guidance on consuming `Stage`/`UsagePoint`. Use `usage-data` from Imports for guidance on
  consuming `useUsageData`.

---

"ConsumptionPanel(stages: Stage[]) -> element: ReactElement":
  location: ConsumptionPanel.tsx
  annotations: |
    Выезжающая слева панель потребления: метрики tokens/cost/kb, фильтр по стейджам, hand-rolled SVG-график.
    Поведение перенесено из loadUsage/probeUsageCost/renderUsageChart/openUsagePanel текущего app.js.

    `stages`: список стадий для построения фильтра
    `element`: разметка панели (aside#usage-panel + кнопка-тоггл #usage-toggle)

    Данные временного ряда получаются через useUsageData из Imports по текущим metric/stageFilter; опция cost
    скрывается, если пробный запрос useUsageData('cost', null) пуст.

    Requirements:
    - Панель выезжает/прячется по кнопке-стрелке (#usage-toggle) — внутреннее состояние open; className «open»
      на aside#usage-panel, aria-hidden на тело панели
    - Опция cost скрывается, если данные по метрике cost пусты (проверка через useUsageData('cost', null) при
      монтировании); если cost был активен, а оказался недоступен — откат на tokens
  properties:
    "open -> boolean": |
      Признак открытой панели (управляется кнопкой-тогглом).
    "metric -> 'tokens' | 'cost' | 'kb'": |
      Текущая метрика.
    "stageFilter -> string | null": |
      Текущий фильтр по стадии.
    "points -> UsagePoint[]": |
      Данные временного ряда через useUsageData.
  methods:
    "switchMetric(metric: 'tokens' | 'cost' | 'kb')":
      annotations: |
        Переключает метрику.

        `metric`: новая метрика

        Requirements:
        - Опция cost скрывается, если данные по метрике cost (запрос useUsageData с metric='cost') пусты;
          проверка выполняется при монтировании
    "setStageFilter(stageId: string | null)":
      annotations: |
        Устанавливает фильтр по стадии.

        `stageId`: идентификатор стадии либо null

---

Author: Goga
CreatedAt: 10/07/26
Description: |
  Панель потребления ресурсов.
```

**.usages/ files:**

`src/components/consumption-panel/.usages/consumption-panel.md`:
```md
Domain: рендер и управление панелью потребления ресурсов (метрики/фильтр/график) для корневой композиции App.

## Базовое использование

```tsx
import { ConsumptionPanel } from '../../components/consumption-panel'
import { useStatus } from '../../hooks/use-status'

function App() {
  const { stages } = useStatus()
  return <ConsumptionPanel stages={stages} />
}
```

## Особенности

- Панель сама вызывает useUsageData (по текущим metric/stageFilter) — вызывающему коду достаточно передать
  список stages для построения фильтра.
- Скрытие метрики «деньги» — решение на уровне этой панели, не самого хука useUsageData.
```

### 15. `src/app` (created)

**CODEMANIFEST:**
```yaml
Imports:
  - Types: [FlowHeader]
    From: src/components/flow-header
  - Types: [StagesList]
    From: src/components/stages-list
  - Types: [PlanPanel]
    From: src/components/plan-panel
  - Types: [DialogChannel]
    From: src/components/dialog-channel
  - Types: [LogPanel]
    From: src/components/log-panel
  - Types: [EventFeedPanel]
    From: src/components/event-feed
  - Types: [ConsumptionPanel]
    From: src/components/consumption-panel
  - Types: [Footer]
    From: src/components/footer
  - Types: [useStatus]
    From: src/hooks/use-status
  - Types: [useEventFeed]
    From: src/hooks/use-event-feed
  - Types: [useStageLog]
    From: src/hooks/use-stage-log
  - Types: [useElapsed]
    From: src/hooks/use-elapsed
  - Types: [Stage]
    Usages: [dashboard-data-types]
    From: src/types

Usages:
  conventions: .goga/usages/conventions.md
  react: .goga/usages/cooks/react.md
  typescript: .goga/usages/cooks/typescript.md

Annotations: |
  Use `conventions` for code writing rules and testing. Use `react` — только функциональные компоненты,
  состояние в хуках верхнего компонента, без Redux/Zustand/Context. Use `typescript` for naming and strict
  typing. Use `dashboard-data-types` from Imports for guidance on consuming `Stage`.

---

"App() -> element: ReactElement":
  location: App.tsx
  annotations: |
    Корневая композиция: шапка, список стадий, панель деталей, лента событий, футер, панель потребления;
    владеет состоянием выбора текущей стадии.

    `element`: разметка всей страницы

    Algorithm:
    1. Получить состояние флоу через `useStatus` (flowName, stages, startedAt, refresh), ленту событий и статус
       соединения через `useEventFeed`, лог выбранной стадии через `useStageLog`, прошедшее время через
       `useElapsed`
    2. Хранить selectedStageId; при его отсутствии — автовыбор активной стадии (статусы planning, running,
       revising, retrying, awaiting_user_input), иначе первая стадия со статусом failed
    3. Распределить данные по клеткам: flowName во `FlowHeader`, stages в `StagesList`/`Footer`/
       `ConsumptionPanel`, события в `EventFeedPanel`, лог в `LogPanel`, план через `PlanPanel` и диалог через
       `DialogChannel` — по выбранной стадии, elapsedMs в `Footer`
    4. При значимых WebSocket-событиях (смена статуса стадии, approved, revised, retry_scheduled,
       retry_exhausted, manual_retry, ask_user, user_answered, agent_completed и т.д.) вызывать
       метод refresh() хука `useStatus` для ре-запроса состояния флоу и ре-рендера

    Requirements:
    - Лог стадии — отдельный источник (useStageLog), а не фильтрация WebSocket-событий

---

Author: Goga
CreatedAt: 10/07/26
Description: |
  Корневая композиция дашборда afm.
```

**.usages/ files:**

`src/app/.usages/app-composition.md`:
```md
Domain: точка входа дашборда afm — как собираются все клетки в единую страницу.

## Базовое использование

```tsx
// src/main.tsx
import { createRoot } from 'react-dom/client'
import { App } from './app'

createRoot(document.getElementById('root')!).render(<App />)
```

## Особенности

- App — единственное место, где вызываются useStatus/useEventFeed/useStageLog/useElapsed; дочерние клетки
  данные сами не запрашивают (кроме consumption-panel, которая сама вызывает useUsageData, и plan-panel/
  dialog-channel, которые сами загружают план/диалог по выбранной стадии).
- Нет глобального состояния (Redux/Zustand/Context) — весь стейт живёт в App и спускается пропсами.
- При значимых WebSocket-событиях App ре-запрашивает состояние флоу (WS — канал обновления, не только лента).
```

## Build & tooling layer

Корневые файлы сборки (`package.json`, `vite.config.ts`, `tsconfig.json`, конфигурация Vitest) живут в клетке
`.` (`pkg/web/dashboard`) и уточняются на стадии **design/apply**. Этот раздел фиксирует критичные ограничения,
которые обязательны к учёту и не должны быть потеряны между стадиями:

- **`vite.config.ts`**: `build.outDir='.'` и `build.emptyOutDir=false` — сборка кладёт `index.html` + `assets/*`
  прямо в корень `dashboard/`, чтобы `pkg/web/embed.go` (`//go:embed dashboard/*`) остался без правок.
  `emptyOutDir=false` обязателен, иначе Vite сотрёт `src/`, `public/`, конфиги при пересборке.
- **Очистка `dashboard/assets/*` перед сборкой**: npm-скрипт `clean:assets` (или `predist`), удаляющий только
  сгенерированную папку `assets/`. Необходим, т.к. при `emptyOutDir=false` хешированные по содержимому
  `assets/*.js`/`assets/*.css` от прошлых сборок не удаляются автоматически и молча накапливаются, раздувая
  бинарник через `//go:embed dashboard/*`. Запускать перед каждым `npm run build`.
- **npm-зависимости**: `react`, `react-dom`, `markdown-it` (рендер markdown — единственная сторонняя
  рантайм-библиотека вместе с React); devDependencies — `vite`, `@vitejs/plugin-react`, `typescript`,
  `vitest`, `@testing-library/react`. Вендоренный `public/markdown-it.min.js` и тег `<script>` в `index.html`
  удаляются (см. usage `react`, раздел markdown-it).
- **Отступление от conventions.md**: проектный `conventions` предписывает JS + Jest; клетка `.` (и весь
  dashboard) использует TypeScript + Vitest. Отступление зафиксировано в usage `typescript`/`vitest` и
  распространяется только на эту клеточку. Usages `vite` и `vitest` подключаются к клетке `.`/`src/app` при
  оформлении CODEMANIFEST клетки `.` на стадии design/apply.
- **Без SSR, без сторонних state-менеджеров** (Redux/Zustand/Context), без новых charting-библиотек
  (SVG-график собственный).

## Dependency Map

```
src/types (лист, без зависимостей)
  │ Stage         → src/hooks/use-status
  │ Stage         → src/components/stages-list
  │ Stage         → src/components/footer
  │ Stage         → src/components/consumption-panel
  │ UsagePoint    → src/components/consumption-panel
  │ LogEntry      → src/hooks/use-stage-log
  │ LogEntry      → src/components/log-panel
  │ AfmEvent      → src/hooks/use-event-feed
  │ AfmEvent      → src/components/event-feed
  │ Stage, PlanComment → src/components/plan-panel
  │ Stage, DialogQuestion → src/components/dialog-channel

src/hooks/use-usage-data
  │ useUsageData  → src/components/consumption-panel

src/components/{flow-header, stages-list, plan-panel, dialog-channel,
                log-panel, event-feed, consumption-panel, footer}
src/hooks/{use-status, use-event-feed, use-stage-log, use-elapsed}
src/types (Stage — для проброса пропсов)
  │  всё это импортируется →  src/app (App)

"." (pkg/web/dashboard, DashboardAssets) — вне графа, не изменяется данным планом
    (корневые конфиги сборки уточняются на design/apply, см. Build & tooling layer)
```

Циклов нет: `src/types` — единственный лист без входящих зависимостей от других новых клеток; `src/app` —
единственный корень, ничего не импортирует из него ни одна другая клеточка. Направление зависимостей
(листья → корень) совпадает с порядком реализации.

## Verification Checklist

После реализации каждого артефакта проверить:

- [ ] Каждый CODEMANIFEST синтаксически корректен по DSL-спецификации (`goga-cell`): секции Header/Body/Footer
  разделены `---`, ключи в точном регистре, `location` — файл на том же уровне директории, без поддиректорий.
- [ ] `goga lint` проходит без ошибок для всех новых CODEMANIFEST (exit 0): annotations не содержат
  неразрешимых backtick-ссылок, у возвращаемых типов есть семантическая метка.
- [ ] Все `Imports` в клетках-потребителях корректно указывают на `From: src/types` /
  `From: src/hooks/use-usage-data` / `From: src/components/*` — путей без опечаток.
- [ ] Нет циклических импортов между клетками (клеточка `src/app` — корень, ни одна клеточка не импортирует из неё).
- [ ] Каждый `.usages/<domain>.md` файл существует по заявленному пути и содержит примеры потребления,
  соответствующие сигнатурам из CODEMANIFEST той же клеточки.
- [ ] Перед `npm run build` выполняется очистка `dashboard/assets/*` (npm-скрипт `clean:assets`/`predist`).
- [ ] `npm run build` в `dashboard/` производит бандл (`index.html` + `assets/*`) прямо в корне `dashboard/`
  (без `dist/`), совместимый с `pkg/web/embed.go` (`build.outDir='.'`, `emptyOutDir=false`).
- [ ] `npx vitest run` проходит без ошибок для тестов, покрывающих все клетки (компоненты + хуки).
- [ ] Поведенческий паритет с текущим `app.js`:
  - backoff-реконнект WS (1000мс → удвоение → потолок 10000мс, сброс после успешного подключения);
  - данные флоу/stages/flow_name/started_at из `GET /api/status`; лог стадии из `GET /api/stages/{id}/log`
    (поллинг 3000мс, не фильтрация WS); план из `GET /api/stages/{id}/plan`; диалог из `GET
    /api/stages/{id}/dialog`;
  - полный набор статусов стадии (pending/planning/awaiting_approval/revising/ready/running/done/failed/
    retrying/awaiting_user_input), прогресс считается по статусу done;
  - скрытие метрики «деньги» при пустом ответе `GET /api/usage?metric=cost`;
  - elapsed тикает собственным односекундным таймером (useElapsed), а не только при ре-рендере родителя;
  - автовыбор активной стадии (иначе первая failed); действие dialog/cancel; WS как канал обновления состояния;
  - double-mount под React StrictMode не приводит к дублирующимся побочным эффектам (через флаг отмены в хуках).
- [ ] Обе темы (`novacorps`, `goga`) визуально идентичны текущим — CSS-классы и id (flow-name, ws-status,
  stages-list и т.д.), зафиксированные в Requirements аннотаций, сохранены в реализации.
- [ ] `pkg/web/embed.go` и клеточка `pkg/web` не затронуты (контракт `DashboardAssets()` без изменений сигнатуры).
