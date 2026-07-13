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
