import type { ReactElement, ReactNode } from 'react'

// The pieces every chat composer (stage feed, Team conversation, assistant) shares: the card around the input,
// its "To" row and the hint line under it. Styles: chat-composer.css.

type ComposerCardProps = {
  // card — the bordered card with a recipients row; without it the plain input row of a legacy note composer.
  card?: boolean
  paused?: boolean
  children: ReactNode
}

export function ComposerCard({ card = true, paused = false, children }: ComposerCardProps): ReactElement {
  return <div className={`chat-composer-row${card ? ' chat-composer-card' : ''}${paused ? ' paused' : ''}`}>{children}</div>
}

type ComposerRecipientsProps = {
  // label — the row's lead word ("To", or "Loop" for a loop target).
  label?: string
  ariaLabel?: string
  className?: string
  children: ReactNode
}

export function ComposerRecipients({ label = 'To', ariaLabel = 'Recipients', className, children }: ComposerRecipientsProps): ReactElement {
  return (
    <div className={`mesh-recipients chat-composer-recipients${className !== undefined ? ` ${className}` : ''}`} role="group" aria-label={ariaLabel}>
      <span className="mesh-recipients-label">{label}</span>
      {children}
    </div>
  )
}

// ComposerHint — what the message does (note) beside the send shortcut.
export function ComposerHint({ note }: { note: string }): ReactElement {
  return (
    <div className="chat-composer-hint">
      <span>{note}</span>
      <span>Enter to send · Shift+Enter for a new line</span>
    </div>
  )
}
