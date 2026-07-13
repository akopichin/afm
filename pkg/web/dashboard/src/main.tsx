import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './app'

// StrictMode намеренно включён: dev-режим дважды монтирует эффекты, и хуки
// дашборда (поллинг, WebSocket, секундомер) обязаны переносить это идемпотентно
// через флаг отмены. В продакшн-сборке StrictMode эффекты не дублирует.
const rootElement = document.getElementById('root')

if (rootElement === null) {
  throw new Error('root element #root not found in index.html')
}

createRoot(rootElement).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
