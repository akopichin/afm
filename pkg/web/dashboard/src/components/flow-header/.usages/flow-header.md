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

## Индикатор attention

```tsx
import { FlowHeader } from '../../components/flow-header'
import { anyAwaiting } from '../../hooks/use-attention'
import type { Stage } from '../../types'

function App({ flowName, stages }: { flowName: string; stages: Stage[] }) {
  return <FlowHeader flowName={flowName} connected={true} attention={anyAwaiting(stages)} />
}
```

`attention` — необязательный проп (по умолчанию false); признак вычисляется вызывающим кодом, обычно через
`anyAwaiting(stages)` (см. хук use-attention), а не самим FlowHeader.

## Описание флоу (несколько параллельных пайплайнов)

```tsx
import { FlowHeader } from '../../components/flow-header'
import { useStatus } from '../../hooks/use-status'

function App() {
  const { flowName, connected, description } = useStatus()
  return <FlowHeader flowName={flowName} connected={connected} description={description} />
}
```

`description` — необязательный проп, подзаголовок под именем флоу (`#flow-description`); скрыт, если
не передан или пуст. Источник — поле `description` ответа GET /api/status (`useStatus`/`normalizeStatus`
читают его защитно: бэкенд пока это поле не отдаёт, но когда начнёт — подзаголовок появится без изменений
в этом компоненте).

## Особенности

- Чистый презентационный компонент — не открывает соединений и не хранит состояние сам, только отображает
  переданные flowName/connected/attention/description.
- Значение connected напрямую пробрасывается из useEventFeed, без промежуточной трансформации.
