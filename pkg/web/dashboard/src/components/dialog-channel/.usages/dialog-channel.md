Domain: рендер и управление диалоговым каналом стадии (история вопросов/ответов по фазам, текущий вопрос,
ответ, отмена) для панели деталей дашборда.

## Базовое использование

```tsx
import { DialogChannel } from '../../components/dialog-channel'
import type { Stage } from '../../types'

function DetailPanel({ stage }: { stage: Stage }) {
  return <DialogChannel stage={stage} attention={false} />
}
```

## Особенности

- Выбор опции (`selectOption`) и ввод свободного текста — взаимоисключающие способы ответить на один и тот же
  pending-вопрос; при непустом свободном тексте выбранная опция сбрасывается.
- `sendAnswer` не принимает параметров — берёт customText/selectedOption из внутреннего состояния компонента.
- `cancel` отменяет текущий диалог (POST /api/stages/{id}/dialog/cancel) после `window.confirm('Cancel stage?')`
  — соответствует кнопке отмены в текущем app.js.
- Комментарии к строкам плана здесь не реализованы — эта функциональность (`addLineComment` и т.п.), ранее
  упомянутая в этом документе, в коде отсутствует.
