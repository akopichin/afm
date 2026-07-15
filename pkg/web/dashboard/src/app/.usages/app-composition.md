Domain: точка входа дашборда afm — как собираются все клетки в единую страницу.

## Базовое использование

```tsx
// src/main.tsx
import { createRoot } from 'react-dom/client'
import { App } from './app'

createRoot(document.getElementById('root')!).render(<App />)
```

## Особенности

- App — единственное место, где вызываются useStatus/useEventFeed/useStageLog/useElapsed; дочерние клетки
  данные сами не запрашивают (кроме plan-panel/dialog-channel, которые сами загружают план/диалог по
  выбранной стадии).
- Нет глобального состояния (Redux/Zustand/Context) — весь стейт живёт в App и спускается пропсами.
- При значимых WebSocket-событиях App ре-запрашивает состояние флоу (WS — канал обновления, не только лента).
