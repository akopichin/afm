Domain: рендер панели плана (загрузка markdown, действия Approve/Revise/Retry, комментарии к строкам) для
панели деталей дашборда.

## Базовое использование

```tsx
import { PlanPanel } from '../../components/plan-panel'
import type { Stage } from '../../types'

function DetailPanel({ stage }: { stage: Stage }) {
  return <PlanPanel stage={stage} attention={false} />
}
```

## Рендер markdown отдельно

```tsx
import { MarkdownRenderer } from '../../components/plan-panel'

<MarkdownRenderer source="# Заголовок плана" />
```

## Особенности

- Панель сама загружает markdown плана по выбранной стадии (GET /api/stages/{id}/plan), как loadPlan в текущем
  app.js; planMarkdown — её внутреннее состояние.
- approve/sendRevision/retry — асинхронные сетевые операции; вызывающий код сам решает, как обрабатывать
  состояние загрузки/ошибки (контракт клеточки этого не навязывает). После успешного действия состояние флоу
  обновляется через хук состояния, не оптимистично.
- comments — внутреннее состояние самой панели (Record<номер строки, текст>), не пропс; панель не запрашивает
  их извне и не делится ими с dialog-channel — комментарии живут только внутри PlanPanel до отправки revision.
