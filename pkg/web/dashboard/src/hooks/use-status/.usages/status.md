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
