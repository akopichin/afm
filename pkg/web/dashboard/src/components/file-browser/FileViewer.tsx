import { Fragment, useEffect, useLayoutEffect, useMemo, useRef, type ReactElement } from 'react'
import type { FileContent } from '../../api/files-client'
import { CommentCard } from '../comments/CommentCard'
import { CommentEditor } from '../comments/CommentEditor'
import { useComments } from '../comments/CommentProvider'
import { CommentRecipientSelect } from '../comments/CommentRecipientSelect'
import { isDraft, type CommentEntry } from '../comments/comment-state'
import { newFileCommentKey, rawSourceLines, sourceDrift, type SourceDrift } from './draft-comments'
import { highlight, splitHighlightedLines } from './highlight'

type FileViewerProps = {
  content: FileContent | null
  loading: boolean
  error: string | null
  // Без root предпросмотр только для чтения: комментарию нужен корень, чтобы привязаться к файлу.
  root?: string
  // Строка, к которой прокрутить после загрузки файла (переход из списка комментариев).
  focus?: { root: string; path: string; line: number } | null
  // Вызывается после прокрутки к focus: владелец сбрасывает его, чтобы Reload не возвращал вид к строке.
  onFocusHandled?: () => void
  // Открывает диалог отправки одного комментария; opener — кнопка Send, на неё вернётся фокус. Без обработчика
  // карточки без отправки (предпросмотр вне Files).
  onSend?: (key: string, opener: HTMLElement) => void
}

// Показывает содержимое выбранного файла построчно; комментарии лежат в run-scoped CommentProvider,
// поэтому переживают смену файла, вкладку DIFF и закрытие Files.
export function FileViewer({ content, loading, error, root, focus = null, onFocusHandled, onSend }: FileViewerProps): ReactElement {
  const { comments: all, open, edit, saveAndFold, fold, foldFile, discard, reanchor, setRecipient, send } = useComments()

  const lines = useMemo(() => {
    if (content === null) return []
    return splitHighlightedLines(highlight(content.language, content.content), content.content)
  }, [content])
  const rawLines = useMemo(() => (content === null ? [] : rawSourceLines(content.content)), [content])

  const path = content?.path
  const entries = useMemo(() => {
    const byLine = new Map<number, CommentEntry>()
    for (const entry of all) {
      const { anchor } = entry
      if (anchor.kind === 'file' && anchor.root === root && anchor.path === path) byLine.set(anchor.line, entry)
    }
    return byLine
  }, [all, root, path])

  // Уход из файла (другой файл, DIFF, закрытие Files) сворачивает его формы: при возврате видна карточка Draft.
  useEffect(() => {
    if (root === undefined || path === undefined) return
    return () => foldFile(root, path)
  }, [root, path, foldFile])

  const sourceRef = useRef<HTMLDivElement | null>(null)
  // content может ещё быть старым файлом, пока грузится новый, поэтому сверяем путь и корень.
  useLayoutEffect(() => {
    const source = sourceRef.current
    if (source === null || focus === null || content === null || focus.root !== root || focus.path !== content.path) return
    // Строки уже нет в новой версии файла — комментарий показан карточкой под текстом.
    const target = source.querySelector(`[data-line="${focus.line}"]`) ?? source.querySelector(`[data-comment-line="${focus.line}"]`)
    target?.scrollIntoView({ block: 'center' })
    onFocusHandled?.()
  }, [focus, content, root, onFocusHandled])

  // После закрытия формы или карточки фокус иначе падает на body; у строки без номера (orphan) — на контейнер.
  function focusLine(line: number): void {
    const source = sourceRef.current
    const button = source?.querySelector<HTMLElement>(`[data-line="${line}"] .file-line-num`)
    ;(button ?? source)?.focus()
  }

  if (loading) return <div className="file-viewer-hint">Loading file…</div>
  if (error !== null) return <div className="file-viewer-hint file-viewer-error">{error}</div>
  if (content === null) return <div className="file-viewer-hint">Select a file to preview</div>
  const file = content

  // Клик открывает форму; повторный клик по строке с открытой формой сворачивает её (пустая исчезает).
  // Открыта только одна форма: другая сворачивается, как по крестику.
  function toggleLine(line: number): void {
    if (root === undefined) return
    const existing = entries.get(line)
    if (existing?.editorOpen === true) {
      foldLine(existing, line)
      return
    }
    foldFile(root, file.path)
    const key = existing?.key ?? newFileCommentKey()
    open(key, {
      anchor: {
        kind: 'file',
        root,
        path: file.path,
        displayPath: file.displayPath,
        reference: file.reference,
        etag: file.etag,
        line,
      },
      quote: rawLines[line - 1] ?? '',
    })
  }

  function saveLine(entry: CommentEntry, line: number): void {
    saveAndFold(entry.key)
    focusLine(line)
  }

  function foldLine(entry: CommentEntry, line: number): void {
    fold(entry.key)
    focusLine(line)
  }

  function removeLine(entry: CommentEntry, line: number): void {
    discard(entry.key)
    focusLine(line)
  }

  function renderComment(line: number, entry: CommentEntry): ReactElement | null {
    if (entry.editorOpen) {
      return (
        <CommentEditor
          context="file"
          title={`Comment on line ${line}`}
          line={line}
          quote={entry.quote}
          placeholder={`Comment on line ${line}...`}
          value={entry.text}
          onChange={(value) => edit(entry.key, value)}
          onSave={() => saveLine(entry, line)}
          onFold={() => foldLine(entry, line)}
          onDiscard={entry.saved !== '' ? () => removeLine(entry, line) : undefined}
          draft={isDraft(entry)}
          saveLabel={entry.saved !== '' ? 'Update comment' : 'Save comment'}
          discardLabel="Delete"
        />
      )
    }
    if (entry.text.trim() === '') return null
    const drift = sourceDrift(entry.quote, line, rawLines)
    return (
      <CommentCard
        title={`Comment on line ${line}`}
        line={line}
        quote={entry.quote}
        text={entry.text}
        draft={isDraft(entry)}
        status="Saved · not sent"
        caption={drift === null ? undefined : describeDrift(line, drift)}
        onEdit={() => toggleLine(line)}
        onRemove={() => removeLine(entry, line)}
        actions={cardActions(entry, line, drift)}
      />
    )
  }

  function cardActions(entry: CommentEntry, line: number, drift: SourceDrift | null): ReactElement {
    return (
      <>
        {onSend !== undefined && (
          <>
            <button
              type="button"
              className="btn btn-send"
              aria-label={`Send comment on line ${line}`}
              disabled={send.pending}
              onClick={(event) => onSend(entry.key, event.currentTarget)}
            >
              Send
            </button>
            <CommentRecipientSelect recipientId={entry.recipientId} onChange={(id) => setRecipient(entry.key, id)} />
          </>
        )}
        {drift !== null && driftActions(entry, line, drift)}
      </>
    )
  }

  function driftActions(entry: CommentEntry, line: number, drift: SourceDrift): ReactElement {
    const { moveTo, currentText } = drift
    return (
      <>
        {currentText !== null && (
          <button
            type="button"
            className="btn"
            onClick={() => reanchor(entry.key, { line, quote: currentText, etag: file.etag })}
          >
            Use current line {line}
          </button>
        )}
        {moveTo !== null && (
          <button
            type="button"
            className="btn"
            disabled={entries.has(moveTo)}
            title={entries.has(moveTo) ? `Line ${moveTo} already has a comment` : undefined}
            onClick={() => reanchor(entry.key, { line: moveTo, quote: entry.quote, etag: file.etag })}
          >
            Move to line {moveTo}
          </button>
        )}
      </>
    )
  }

  const orphans = Array.from(entries).filter(([line]) => line > lines.length)

  return (
    <div className="file-viewer-source" ref={sourceRef} tabIndex={-1}>
      {lines.map((html, index) => {
        const line = index + 1
        const entry = entries.get(line)
        const hasComment = entry !== undefined && (entry.editorOpen || entry.text.trim() !== '')
        const annotatable = root !== undefined
        return (
          <Fragment key={line}>
            <div
              className={`file-line${hasComment ? ' has-comment' : ''}${annotatable ? ' annotatable' : ''}`}
              data-line={line}
              data-testid={`file-line-${line}`}
              onClick={() => toggleLine(line)}
            >
              {annotatable ? (
                <button type="button" className="file-line-num" aria-label={`Comment on line ${line}`}>
                  {line}
                </button>
              ) : (
                <span className="file-line-num">{line}</span>
              )}
              <code className={`hljs language-${file.language}`} dangerouslySetInnerHTML={{ __html: html }} />
              {annotatable && (
                <span className="file-line-comment-marker" aria-hidden="true">
                  ●
                </span>
              )}
            </div>
            {entry !== undefined && renderComment(line, entry)}
          </Fragment>
        )
      })}
      {/* Комментарии к строкам, которых в новой версии файла уже нет, остаются видимыми под текстом. */}
      {orphans.map(([line, entry]) => (
        <Fragment key={`orphan-${line}`}>{renderComment(line, entry)}</Fragment>
      ))}
    </div>
  )
}

function describeDrift(line: number, drift: SourceDrift): string {
  if (drift.currentText === null) return `Source changed — line ${line} no longer exists`
  return `Source changed — line ${line} now reads: "${drift.currentText}"`
}
