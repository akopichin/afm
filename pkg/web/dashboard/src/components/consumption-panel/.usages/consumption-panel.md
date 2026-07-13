Domain: рендер и управление панелью потребления ресурсов (метрики/фильтр/график) для корневой композиции App.

## Базовое использование

```tsx
import { ConsumptionPanel } from '../../components/consumption-panel'
import { useStatus } from '../../hooks/use-status'

function App() {
  const { stages } = useStatus()
  return <ConsumptionPanel stages={stages} />
}
```

## Особенности

- Панель сама вызывает useUsageData (по текущим metric/stageFilter) — вызывающему коду достаточно передать
  список stages для построения фильтра.
- Скрытие метрики «деньги» — решение на уровне этой панели, не самого хука useUsageData.
