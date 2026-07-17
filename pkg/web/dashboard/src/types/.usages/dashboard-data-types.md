Domain: общие типы данных дашборда afm (`Stage`, `AfmEvent`, `DialogQuestion`, `DialogAnswer`,
`PlanComment`, `LogEntry`) — для клеток-хуков и клеток-компонентов, потребляющих данные бэкенда.

## Источники данных (соответствие app.js)

- `Stage` — поле stages ответа `GET /api/status` (объект, ключ — id стадии); там же `flow_name` и `started_at`.
- `LogEntry` — `GET /api/stages/{id}/log` (поллинг 3с).
- `AfmEvent` — WebSocket `/ws`.
- План и диалог — `GET /api/stages/{id}/plan` и `GET /api/stages/{id}/dialog`.

Полный набор статусов стадии: pending, planning, awaiting_approval, revising, ready, running, done, failed,
retrying, awaiting_user_input (done — завершена, не completed).

## Импорт типов

Все типы реэкспортируются через barrel index.ts клеточки:

```ts
import type { Stage, AfmEvent } from '../../types'
```

## Типизация ответа fetch

Типы описывают форму уже распарсенного JSON — единственная точка приведения типа должна находиться в
вызывающей клеточке (хуке), а не здесь:

```ts
async function fetchStatus(): Promise<StatusResponse> {
  const response = await fetch('/api/status')
  const data: unknown = await response.json()
  return data as StatusResponse
}
```

## Использование union-типов статусов/метрик/уровней

`Stage.status`, `LogEntry.level` — строковые литеральные union-типы, пригодны для
switch/сравнения без дополнительной валидации:

```ts
function isActive(status: Stage['status']): boolean {
  return status === 'planning' || status === 'running' || status === 'revising'
    || status === 'retrying' || status === 'awaiting_user_input'
}
```

## Готовые константы вместо ручных списков

Не дублируйте наборы статусов/типов событий вручную — используйте готовые константы клеточки:

```ts
import { ACTIVE_STAGE_STATUSES, STAGE_STATUS_LABELS, SIGNIFICANT_EVENT_TYPES, extractStageStatus } from '../../types'

const isActive = (status: Stage['status']) => ACTIVE_STAGE_STATUSES.has(status)
const label = STAGE_STATUS_LABELS[stage.status]
```

`extractStageStatus(payload)` — достаёт распознанный `StageStatus` из payload события `stage_status_changed`
(строка или `{ status }`), возвращает `null`, если payload не распознан.
