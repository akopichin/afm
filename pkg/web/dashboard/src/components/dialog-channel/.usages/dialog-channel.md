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
- Markdown-рендер (`MarkdownRenderer`) применяется к вопросам в истории; ответы в истории всегда выводятся
  как обычный текст («→ answer»), без markdown-рендера.
- Комментарии к строкам pending-вопроса (killer feature #2) реализованы по тому же паттерну, что и комментарии
  к плану в `PlanPanel`: строки вопроса (`question.split('\n')`, каждая через `formatLine`) рендерятся как
  кликабельные `plan-line` — клик открывает форму add/update/delete комментария. Как только появляется хотя бы
  один комментарий, опции и свободный `dialog-custom` textarea скрываются, а вместо кнопки «▸ SEND» показывается
  одна кнопка «Send feedback (N)». Её клик собирает комментарии в текст (цитата строки вопроса + «Line N: …»,
  отсортировано, блоки через пустую строку — аналог `buildFeedback` в PlanPanel) и отправляет его тем же
  `POST /api/stages/{id}/dialog/answer` с `from_options: false`, что и обычный `sendAnswer`. Ctrl/Cmd+Enter в
  форме комментария сохраняет комментарий (не отправляет весь feedback). Комментарии — внутреннее клиентское
  состояние (`comments`/`activeCommentLine`/`draft`), сбрасываются при смене pending-вопроса и после отправки.
- При разворачивании панели на весь экран (кнопка PanelFrame, `maximizeId="dialog"`) канал прокручивается
  к хвосту диалога (`feed.jumpToBottom()` через requestAnimationFrame) — так же, как при появлении нового
  pending-вопроса. Признак maximized читается напрямую из `useMaximize()` (Maximizable), без изменений в
  самом Maximizable.
