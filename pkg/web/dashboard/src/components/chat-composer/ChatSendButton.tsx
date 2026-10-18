import type { ReactElement } from 'react'

export type ChatSendButtonProps = {
  // label — доступное имя и подсказка; на самой кнопке только иконка.
  label: string
  disabled: boolean
  onClick: () => void
  // paused — стадия на паузе: кнопка янтарная, как и рамка строки ввода.
  paused?: boolean
}

// ChatSendButton — круглая кнопка отправки с самолётиком. Одна на все чат-композеры (лента, обсуждение,
// Team, боковой агент) и живёт внутри строки ввода .chat-composer-row.
export function ChatSendButton({ label, disabled, onClick, paused = false }: ChatSendButtonProps): ReactElement {
  return (
    <button
      type="button"
      className={`chat-send${paused ? ' paused' : ''}`}
      aria-label={label}
      title={label}
      disabled={disabled}
      onClick={onClick}
    >
      <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <path d="M22 2 11 13" />
        <path d="M22 2 15 22l-4-9-9-4 20-7Z" />
      </svg>
    </button>
  )
}
