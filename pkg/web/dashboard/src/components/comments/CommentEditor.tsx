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
  // draft — в заголовке бейдж Draft: введён текст, которого ещё нет среди сохранённых.
  draft?: boolean
  saveDisabled?: boolean
  error?: string | null
  allowAttachments?: boolean
  placeholder?: string
}

// Подсказка под полем ввода: Enter сохраняет, Shift+Enter — новая строка, Escape сворачивает без потери текста.
const EDITOR_HELP = 'Enter to save · Shift+Enter for a new line · Esc to collapse and keep draft'

// CommentEditor — единый редактор комментария. Escape/крестик/Cancel только сворачивают (текст остаётся у
// владельца), удаление — явная кнопка.
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
  draft = false,
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

  const toolbar = (
    <>
      {context !== 'review' && <span className="comment-actions-hint">Save here · send when ready</span>}
      <div className="comment-actions">
        <button className="btn btn-quiet" type="button" onClick={onFold}>
          Cancel
        </button>
        {onDiscard !== undefined && (
          <button className="btn btn-cancel" type="button" onClick={onDiscard}>
            {discardLabel}
          </button>
        )}
        <button className="btn btn-send" type="button" disabled={saveDisabled} onClick={onSave}>
          {saveLabel}
        </button>
      </div>
    </>
  )

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
        {draft && <span className="comment-draft-badge">Draft</span>}
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
          toolbar={toolbar}
        />
      ) : (
        <>
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
          <div className="pasteable-toolbar">{toolbar}</div>
        </>
      )}
      {error !== null && (
        <p className="comment-error" role="alert">
          {error}
        </p>
      )}
      <p className="comment-help">{EDITOR_HELP}</p>
    </div>
  )
}
