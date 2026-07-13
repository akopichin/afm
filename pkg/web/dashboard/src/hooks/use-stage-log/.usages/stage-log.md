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
