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
- Каждый Stage в списке уже содержит нормализованные флаги interactive/autonomous (по умолчанию false, если
  стадия отсутствует в ответах stage_interactive/stage_autonomous) — готовы для логики видимости панелей
  диалога/плана без дополнительной нормализации на стороне потребителя.
- `description` — опциональное поле (undefined, если отсутствует в ответе); передаётся в FlowHeader как
  подзаголовок под именем флоу, чтобы отличать несколько параллельно запущенных пайплайнов.
