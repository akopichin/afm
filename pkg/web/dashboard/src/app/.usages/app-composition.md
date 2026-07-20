Domain: точка входа дашборда afm — как собираются все клетки в единую страницу.

## Базовое использование

```tsx
// src/main.tsx
import { createRoot } from 'react-dom/client'
import { App } from './app'

createRoot(document.getElementById('root')!).render(<App />)
```

## Особенности

- App — единственное место, где вызываются useStatus/useEventFeed/useStageLog/useElapsed/useAttention/
  useTitleFlash; дочерние клетки данные сами не запрашивают (кроме plan-panel/dialog-channel и
  supervisor-decision, которые сами загружают своё содержимое по выбранной стадии).
- Нет глобального состояния (Redux/Zustand/Context) — весь стейт живёт в App и спускается пропсами.
- При значимых WebSocket-событиях App ре-запрашивает состояние флоу (WS — канал обновления, не только лента).
- `FlowHeader` и `Footer` рендерятся напрямую как соседи `<main>`, а не через `DashboardLayout`. Внутри `<main>`
  разметка центральной части собирается через `DashboardLayout`/`MaximizeProvider` (src/components/layout) —
  resizable-колонки с сохранением layout в localStorage; App передаёт им готовые панели (stages, stageHeader,
  plan, dialog, log, feed) через слоты.
- `PlanPanel`/`DialogChannel` не смонтированы безусловно: их видимость решают `showPlan`/`showDialog` — план
  скрыт для автономной стадии (нет plan.md), диалог скрыт только когда стадия не interactive и не autonomous.
  Пока стадия не выбрана, показаны обе панели с нейтральным sentinel-объектом стадии вместо `null`.
