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
