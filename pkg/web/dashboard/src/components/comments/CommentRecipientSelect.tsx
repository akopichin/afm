import type { ReactElement } from 'react'
import { useCommentRecipientChoice, CHOOSE_AGENT } from './comment-recipients'

// CommentRecipientSelect — выбор одного адресата: роли видны в списке, занятый Assistant помечен, а причина,
// по которой отправка пока невозможна, показана рядом.
export function CommentRecipientSelect({ recipientId, onChange }: { recipientId: string; onChange: (id: string) => void }): ReactElement {
  const { options, recipient, problem } = useCommentRecipientChoice(recipientId)
  return (
    <div className="comment-recipient">
      <select
        className="comment-recipient-select"
        aria-label="Recipient"
        value={recipient?.id ?? ''}
        disabled={options.length === 0}
        onChange={(e) => onChange(e.target.value)}
      >
        {recipient === null && <option value="">{CHOOSE_AGENT}</option>}
        {options.map((o) => (
          <option key={o.id} value={o.id}>
            {`${o.label} · ${o.role}${o.busy ? ' (busy)' : ''}`}
          </option>
        ))}
      </select>
      {problem !== null && <span className="comment-recipient-note">{problem}</span>}
    </div>
  )
}
