import { useEffect, useMemo, useState, type ReactElement, type ReactNode } from 'react'
import { FlowApiError, type AddNoteRequest } from '../../api/run-client'
import type { FileContent } from '../../api/files-client'
import type { ReviewNote } from '../../types'
import { highlight, splitHighlightedLines } from './highlight'

// Преобразуем коды API в текст; stale_content требует перечитать файл, rev_conflict — обновить заметки.
function describeAddNoteError(err: unknown): string {
  if (err instanceof FlowApiError) {
    if (err.code === 'stale_content') return 'File changed since it was opened — reload and re-anchor the note.'
    if (err.code === 'rev_conflict') return 'Notes changed elsewhere — reload and retry.'
    if (err.code !== undefined) return `Request failed: ${err.code}`
  }
  return err instanceof Error ? err.message : 'Request failed'
}

// Совпадает с use-status.ts's FlowStatus['flowPauseState'] — 'none', 'paused', 'resuming'.
type FlowPauseState = 'none' | 'paused' | 'resuming'

type FileViewerProps = {
  content: FileContent | null
  loading: boolean
  error: string | null
  // Annotation props are optional; without them FileViewer renders a read-only preview.
  root?: string
  // Master gate for the annotate affordance — lines open the comment editor directly only when the flow is
  // actually paused.
  flowPauseState?: FlowPauseState
  // pauseFlow runs after the user confirms the pause dialog.
  pauseFlow?: () => Promise<{ paused_stages: string[] }>
  addNote?: (body: AddNoteRequest) => Promise<{ note: ReviewNote; rev: number }>
  // Optimistic-concurrency token the backend checks addNote against. Defaults to 0 (an empty/never-fetched
  // store), which is also what a fresh review round starts at server-side.
  expectedRev?: number
  // Notes already persisted for the current review round. Local optimistic edits still take precedence
  // until the modal's list catches up.
  savedNotes?: ReviewNote[]
  // Test-only override: lets FileViewer.test.tsx supply a fixed hash without depending on jsdom's
  // crypto.subtle.
  contentSha?: string
}

// Показывает содержимое выбранного файла, построчно.
export function FileViewer({
  content,
  loading,
  error,
  root,
  flowPauseState = 'none',
  pauseFlow,
  addNote,
  expectedRev = 0,
  savedNotes,
  contentSha,
}: FileViewerProps): ReactElement {
  const [comments, setComments] = useState<Record<number, string>>({})
  const [activeCommentLine, setActiveCommentLine] = useState<number | null>(null)
  const [draft, setDraft] = useState('')
  const [saving, setSaving] = useState(false)
  // Инлайновая ошибка последнего addNote: форма остаётся открытой, пользователь видит причину рядом с
  // редактором и может перечитать/повторить — а не молча закрывшуюся форму без сигнала.
  const [saveError, setSaveError] = useState<string | null>(null)
  const [computedSha, setComputedSha] = useState<string | null>(null)
  // Line clicked while flowPauseState === 'none', awaiting the user's yes/no on the pause-gate confirm.
  const [pendingPauseLine, setPendingPauseLine] = useState<number | null>(null)

  const canUseNotes = addNote !== undefined && root !== undefined && content !== null
  const canAnnotate = flowPauseState === 'paused' && canUseNotes
  const canRequestPause = flowPauseState === 'none' && canUseNotes && pauseFlow !== undefined
  const sha = contentSha ?? computedSha

  // Persisted line notes for THIS file, keyed by line number, so a reopened file shows its saved
  // markers/prefill again.
  const savedByLine = useMemo(() => {
    const m: Record<number, string> = {}
    if (root === undefined || content === null || savedNotes === undefined) return m
    for (const n of savedNotes) {
      if (n.root === root && n.path === content.path && n.line != null) m[n.line] = n.text
    }
    return m
  }, [savedNotes, root, content])
  const displayComments = useMemo(() => ({ ...savedByLine, ...comments }), [savedByLine, comments])

  // Открыть отложенную строку, как только флоу реально встал на паузу — тот самый переход, ради которого
  // пользователь нажал "Да" в конфирме.
  useEffect(() => {
    if (flowPauseState !== 'paused' || pendingPauseLine === null) return
    setActiveCommentLine(pendingPauseLine)
    setDraft(displayComments[pendingPauseLine] ?? '')
    setPendingPauseLine(null)
    // `comments` deliberately excluded: it only changes after a save, and by
    // then pendingPauseLine has already been cleared above, so re-running
    // this effect on a comments change is a no-op guarded by that check.
  }, [flowPauseState, pendingPauseLine])

  // Свежий файл — свежее состояние комментариев/формы.
  useEffect(() => {
    setComments({})
    setActiveCommentLine(null)
    setDraft('')
    setSaveError(null)
  }, [root, content?.path])

  // content_sha считается один раз на загруженный файл — не на каждый рендер и не на каждое нажатие Save.
  // contentSha-проп (тестовый) пропускает вычисление целиком.
  useEffect(() => {
    if (contentSha !== undefined || content === null) return

    let cancelled = false
    computeContentSha(content.content)
      .then((value) => {
        if (!cancelled) setComputedSha(value)
      })
      .catch(() => {
        if (!cancelled) setComputedSha(null)
      })

    return () => {
      cancelled = true
    }
  }, [content, contentSha])

  const lines = useMemo(() => {
    if (content === null) return []
    return splitHighlightedLines(highlight(content.language, content.content), content.content)
  }, [content])

  if (loading) return <div className="file-viewer-hint">Loading file…</div>
  if (error !== null) return <div className="file-viewer-hint file-viewer-error">{error}</div>
  if (content === null) return <div className="file-viewer-hint">Select a file to preview</div>

  return <div className="file-viewer-source">{lines.map((html, index) => renderLine(index + 1, html))}</div>

  function handleLineClick(line: number) {
    if (canAnnotate) {
      if (activeCommentLine === line) {
        setActiveCommentLine(null)
        return
      }

      setActiveCommentLine(line)
      setDraft(displayComments[line] ?? '')
      setSaveError(null)
      return
    }

    if (!canRequestPause) return

    // Toggle, same as the direct-editor path above: clicking the same
    // pending line again closes the confirm instead of re-asking.
    setPendingPauseLine((prev) => (prev === line ? null : line))
  }

  function closeCommentForm() {
    setActiveCommentLine(null)
    setDraft('')
    setSaveError(null)
  }

  function confirmPause() {
    if (pauseFlow === undefined) return
    // pendingPauseLine stays set — the useEffect above opens the editor for it once flowPauseState
    // actually reports 'paused'.
    void pauseFlow().catch(() => setPendingPauseLine(null))
  }

  function declinePause() {
    setPendingPauseLine(null)
  }

  async function saveComment(line: number) {
    if (addNote === undefined || root === undefined || content === null || sha === null) return

    const text = draft.trim()
    if (text === '') return

    setSaving(true)
    setSaveError(null)
    try {
      await addNote({
        root,
        path: content.path,
        line,
        text,
        content_sha: sha,
        expected_rev: expectedRev,
      })
      setComments((prev) => ({ ...prev, [line]: text }))
      setActiveCommentLine(null)
      setDraft('')
    } catch (err) {
      // Keep the form open with the draft intact so the user can reload/retry
      // rather than losing the note to a silently-closed editor.
      setSaveError(describeAddNoteError(err))
    } finally {
      setSaving(false)
    }
  }

  function renderLine(lineNumber: number, html: string): ReactNode {
    const hasComment = displayComments[lineNumber] !== undefined
    const editing = activeCommentLine === lineNumber
    const confirmingPause = pendingPauseLine === lineNumber
    // Lines stay clickable for both direct annotation and the pause confirmation gate.
    const clickable = canAnnotate || canRequestPause

    return (
      <div
        key={lineNumber}
        className={`file-line${hasComment ? ' has-comment' : ''}${clickable ? ' annotatable' : ''}`}
        data-line={lineNumber}
        data-testid={`file-line-${lineNumber}`}
        onClick={() => handleLineClick(lineNumber)}
      >
        <span className="file-line-num">{lineNumber}</span>
        <code className={`hljs language-${content?.language ?? 'plain'}`} dangerouslySetInnerHTML={{ __html: html }} />
        {clickable && <span className="file-line-comment-marker">●</span>}

        {confirmingPause && (
          <div className="review-pause-confirm" onClick={(event) => event.stopPropagation()}>
            <p>Writing notes requires pausing the flow. Pause it?</p>
            <div className="comment-actions">
              <button className="btn btn-send" type="button" onClick={confirmPause}>
                Pause
              </button>
              <button className="btn btn-cancel" type="button" onClick={declinePause}>
                Cancel
              </button>
            </div>
          </div>
        )}

        {editing && (
          <div className="line-comment-form" onClick={(event) => event.stopPropagation()}>
            <div className="comment-display-header">
              <span style={{ color: 'var(--c-awaiting)', fontSize: '12px' }}>Comment on line {lineNumber}</span>
              <button
                type="button"
                className="comment-remove"
                aria-label={`Close comment on line ${lineNumber}`}
                title="Close"
                onClick={closeCommentForm}
              >
                ✕
              </button>
            </div>
            <textarea
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              placeholder={`Comment on line ${lineNumber}...`}
              autoFocus
              onKeyDown={(event) => {
                // Cmd/Ctrl+Enter сохраняет комментарий (как кнопка Save/Update).
                if ((event.metaKey || event.ctrlKey) && event.key === 'Enter' && !saving && draft.trim() !== '' && sha !== null) {
                  event.preventDefault()
                  void saveComment(lineNumber)
                }
              }}
            />
            {saveError !== null && (
              <p className="line-comment-error" role="alert">
                {saveError}
              </p>
            )}
            <div className="comment-actions">
              <button
                className="btn btn-send"
                type="button"
                disabled={saving || draft.trim() === '' || sha === null}
                onClick={() => void saveComment(lineNumber)}
              >
                {hasComment ? 'Update' : 'Save'}
              </button>
              <button className="btn btn-cancel" type="button" onClick={closeCommentForm}>
                Cancel
              </button>
            </div>
          </div>
        )}
      </div>
    )
  }
}

// computeContentSha возвращает sha256:<hex> через WebCrypto; формула должна совпадать с серверной
// проверкой content_sha.
async function computeContentSha(text: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text))
  return `sha256:${toHex(new Uint8Array(digest))}`
}

function toHex(bytes: Uint8Array): string {
  let hex = ''
  for (const byte of bytes) hex += byte.toString(16).padStart(2, '0')
  return hex
}
