Domain: подключение поллинга метрик потребления ресурсов (GET /api/usage) к панели потребления дашборда.

## Базовое использование

```tsx
import { useUsageData } from '../../hooks/use-usage-data'

function ConsumptionChart({ metric, stageFilter }: { metric: string; stageFilter: string | null }) {
  const points = useUsageData(metric, stageFilter)
  return <UsageChart points={points} />
}
```

## Проверка доступности метрики «деньги»

Хук не решает, показывать ли переключатель метрики — это ответственность вызывающей клеточки. Проверка «есть ли
данные по cost» выполняется тем же хуком с metric='cost', интерпретация пустого ответа — на стороне потребителя:

```tsx
const costPoints = useUsageData('cost', null)
const costAvailable = costPoints.length > 0
```

## Особенности

- points — пустой массив до первого ответа или если у бэкенда нет данных для этой метрики/фильтра.
- Смена stageFilter на null означает «без фильтра», а не «не загружать данные».
