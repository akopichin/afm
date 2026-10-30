import type { ReactElement, ReactNode } from 'react'

export type CommentContext = 'plan' | 'question' | 'file' | 'review'

// subjectOf — подпись для aria-label кнопок ("Remove comment on line 3"): явный label либо заголовок в нижнем регистре.
export function subjectOf(title: string, label?: string): string {
  return label ?? title.charAt(0).toLowerCase() + title.slice(1)
}

// CommentQuote — цитата строки-источника; всегда текст, не Markdown. Пустая цитата (пустая/hr-строка) ничего не рендерит.
export function CommentQuote({ quote }: { quote: string }): ReactElement | null {
  if (quote === '') return null
  return (
    <div className="line-comment-quote">
      <span className="line-comment-quote-src">{quote}</span>
    </div>
  )
}

export type CommentCardProps = {
  title: string
  label?: string
  caption?: string
  line?: number
  quote: string
  text: string
  draft?: boolean
  // status — подпись сохранённого комментария рядом с заголовком («Saved · not sent»); у черновика её заменяет Draft.
  status?: string
  onEdit?: () => void
  // onRemove — ✕ в шапке; без него (как у серверных review-заметок) удаление идёт только явной кнопкой в actions.
  onRemove?: () => void
  actions?: ReactNode
}

// CommentCard — сохранённый комментарий во всех контекстах: полный текст, цитата и явные действия.
export function CommentCard({
  title,
  label,
  caption,
  line,
  quote,
  text,
  draft = false,
  status,
  onEdit,
  onRemove,
  actions,
}: CommentCardProps): ReactElement {
  const subject = subjectOf(title, label)
  return (
    <div className="line-comment-form line-comment-display comment-card" data-comment-ui="" data-comment-line={line}>
      <div className="comment-display-header">
        <span className="comment-title">{title}</span>
        {draft ? <span className="comment-draft-badge">Draft</span> : status !== undefined && <span className="comment-status">{status}</span>}
        {onRemove !== undefined && (
          <button type="button" className="comment-remove" aria-label={`Remove ${subject}`} title="Remove comment" onClick={onRemove}>
            ✕
          </button>
        )}
      </div>
      {caption !== undefined && <div className="comment-caption">{caption}</div>}
      <CommentQuote quote={quote} />
      <div className="comment-text">{text}</div>
      {(onEdit !== undefined || actions !== undefined) && (
        <div className="comment-actions">
          {onEdit !== undefined && (
            <button type="button" className="btn btn-edit" data-comment-edit="" aria-label={`Edit ${subject}`} onClick={onEdit}>
              Edit
            </button>
          )}
          {actions}
        </div>
      )}
    </div>
  )
}
