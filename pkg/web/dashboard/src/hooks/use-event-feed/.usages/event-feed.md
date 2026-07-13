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
