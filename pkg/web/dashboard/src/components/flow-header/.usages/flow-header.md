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
