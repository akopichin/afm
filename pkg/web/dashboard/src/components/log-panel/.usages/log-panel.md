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
