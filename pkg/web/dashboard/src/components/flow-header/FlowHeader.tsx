import type { ReactElement } from 'react'

type FlowHeaderProps = {
  flowName: string
  connected: boolean
  attention?: boolean
}

// Шапка дашборда: декоративный логотип, имя флоу и индикатор WebSocket-соединения.
// id flow-name и ws-status и классы ws-status connected/disconnected сохранены для
// совместимости с темами (см. style.css / style-goga.css).
// Когда attention=true — хотя бы одна стадия ждёт действия пользователя — рядом с
// именем флоу загорается пульсирующая amber-точка.
export function FlowHeader({ flowName, connected, attention = false }: FlowHeaderProps): ReactElement {
  const statusText = connected ? 'LINK' : 'OFFLINE'
  const statusClass = connected ? 'connected' : 'disconnected'

  return (
    <header id="header">
      <span className="logo" aria-hidden="true">
        <span className="l-ring">
          <svg viewBox="0 0 24 24" fill="none" stroke="#6fd4cc" strokeWidth="1">
            <polygon points="12,1 22,7 22,17 12,23 2,17 2,7" />
          </svg>
        </span>
        <span className="l-arc">
          <svg viewBox="0 0 24 24" fill="none" stroke="#e5d442" strokeWidth="1" strokeLinecap="round">
            <path d="M 12 3 A 9 9 0 0 1 21 12" />
          </svg>
        </span>
        <span className="l-core">
          <svg viewBox="0 0 24 24" fill="#6fd4cc" stroke="none">
            <circle cx="12" cy="12" r="2.4" />
          </svg>
        </span>
      </span>
      <h1>afm</h1>
      <div id="flow-name" className="flow-name">{flowName}</div>
      {attention && <span className="attention-dot" aria-label="Нужно действие" />}
      <div id="ws-status" className={`ws-status ${statusClass}`} title="WebSocket">{statusText}</div>
    </header>
  )
}
