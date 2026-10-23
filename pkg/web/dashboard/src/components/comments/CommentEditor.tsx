import { useEffect, useRef, type KeyboardEvent, type ReactElement } from 'react'
import { handleTextareaSubmit } from '../../lib/textarea-submit'
import { PasteableTextarea } from '../pasteable-textarea/PasteableTextarea'
import { CommentQuote, subjectOf, type CommentContext } from './CommentCard'

export type CommentEditorProps = {
  context: CommentContext
  // stageId нужен только plan/question: вложения привязаны к стадии, у файлового комментария её нет.
  stageId?: string
  title: string
  label?: string
  line?: number
  quote: string
  value: string
  onChange: (value: string) => void
  onSave: () => void
  onFold: () => void
  onDiscard?: () => void
  saveLabel: string
  discardLabel?: string
  saveDisabled?: boolean
  error?: string | null
  allowAttachments?: boolean
  placeholder?: string
}

// CommentEditor — единый редактор комментария. Escape/крестик только сворачивают (текст остаётся у владельца),
// удаление — явная кнопка.
export function CommentEditor({
  context,
  stageId,
  title,
  label,
  line,
  quote,
  value,
  onChange,
  onSave,
  onFold,
  onDiscard,
  saveLabel,
  discardLabel = 'Discard',
  saveDisabled = false,
  error = null,
  allowAttachments = true,
  placeholder,
}: CommentEditorProps): ReactElement {
  const wrapRef = useRef<HTMLDivElement | null>(null)
  const subject = subjectOf(title, label)
  const submit = saveDisabled ? undefined : onSave
  const withAttachments = allowAttachments && stageId !== undefined && (context === 'plan' || context === 'question')

  // Форма контейнера (списка/таблицы) появляется под всем блоком — подтягиваем её в вид.
  useEffect(() => {
    const el = wrapRef.current
    if (el !== null && typeof el.scrollIntoView === 'function') el.scrollIntoView({ block: 'nearest' })
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  function handleKeyDown(event: KeyboardEvent<HTMLTextAreaElement>): void {
    if (event.key === 'Escape') {
      // Иначе Escape закрыл бы родительскую модалку (Files, review) вместе с редактором.
      event.preventDefault()
      event.stopPropagation()
      onFold()
    }
  }

  return (
    <div
      className="line-comment-form comment-editor"
      data-comment-ui=""
      data-comment-context={context}
      data-comment-line={line}
      ref={wrapRef}
    >
      <div className="comment-display-header">
        <span className="comment-title">{title}</span>
        <button type="button" className="comment-remove" aria-label={`Close ${subject}`} title="Close" onClick={onFold}>
          ✕
        </button>
      </div>
      <CommentQuote quote={quote} />
      {withAttachments ? (
        <PasteableTextarea
          stageId={stageId}
          placeholder={placeholder}
          value={value}
          onChange={onChange}
          autoFocus
          allowFileReferences
          onSubmit={submit}
          onKeyDown={handleKeyDown}
        />
      ) : (
        <textarea
          value={value}
          placeholder={placeholder}
          autoFocus
          onChange={(event) => onChange(event.target.value)}
          onKeyDown={(event) => {
            if (submit !== undefined && handleTextareaSubmit(event, submit)) return
            handleKeyDown(event)
          }}
        />
      )}
      {error !== null && (
        <p className="comment-error" role="alert">
          {error}
        </p>
      )}
      <div className="comment-actions">
        <button className="btn btn-send" type="button" disabled={saveDisabled} onClick={onSave}>
          {saveLabel}
        </button>
        {onDiscard !== undefined && (
          <button className="btn btn-cancel" type="button" onClick={onDiscard}>
            {discardLabel}
          </button>
        )}
      </div>
    </div>
  )
}
