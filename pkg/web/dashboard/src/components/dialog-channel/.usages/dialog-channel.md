Domain: рендер и управление диалоговым каналом стадии (история вопросов/ответов, ответы, отмена, комментарии к
строкам плана) для панели деталей дашборда.

## Базовое использование

```tsx
import { DialogChannel } from '../../components/dialog-channel'

function DetailPanel({ stageId }: { stageId: string }) {
  return <DialogChannel stageId={stageId} />
}
```

## Особенности

- selectOption/submitCustomAnswer — взаимоисключающие способы ответить на один и тот же currentQuestion;
  вызывающий UI решает, какой показать (опции vs поле свободного текста), в зависимости от числа опций в
  currentQuestion.
- cancel отменяет текущий диалог (POST /api/stages/{id}/dialog/cancel) — соответствует кнопке отмены в текущем
  app.js.
- addLineComment не зависит от наличия currentQuestion — доступен всегда, пока отображается план.
