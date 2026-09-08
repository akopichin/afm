import { useEffect, useMemo, useState, type ReactElement, type ReactNode } from 'react'
import type { AddNoteRequest } from '../../api/run-client'
import type { FileContent } from '../../api/files-client'
import type { ReviewNote } from '../../types'
import { highlight, splitHighlightedLines } from './highlight'

// Совпадает с use-status.ts's FlowStatus['flowPauseState'] — 'none' (флоу
// активен), 'paused' (заметки можно писать прямо сейчас), 'resuming'
// (флоу возобновляется, заметки только что были доставлены/отменены). Тип
// продублирован здесь намеренно: FileViewer не должен тянуть весь модуль
// use-status ради одного литерала.
type FlowPauseState = 'none' | 'paused' | 'resuming'

type FileViewerProps = {
  content: FileContent | null
  loading: boolean
  error: string | null
  // root/addNote/flowPauseState/pauseFlow/expectedRev are all optional: a
  // plain content preview (the only thing FileBrowserModal wires up today)
  // doesn't pass them, and FileViewer degrades to the old read-only render —
  // no line is clickable, no comment marker shows. The modal's authoritative
  // notes list (Task 23) threads real values in on top of this.
  root?: string
  // Master gate for the annotate affordance — lines open the comment editor
  // directly only when the flow is actually paused (writing a note against
  // content that might move under an agent's feet is unsafe otherwise).
  // 'none' → the first line click shows a confirm ("pause the flow to write
  // notes?") instead of the editor; 'resuming' behaves like 'none' (nothing
  // is clickable, no confirm — the pause/resume transition is already
  // in-flight from elsewhere).
  flowPauseState?: FlowPauseState
  // Task 19's pauseFlow() — invoked when the user confirms the pause-gate
  // dialog. Without it (or without root/addNote/content), a line click is a
  // no-op even when flowPauseState is 'none' — there is nothing to gate.
  pauseFlow?: () => Promise<{ paused_stages: string[] }>
  addNote?: (body: AddNoteRequest) => Promise<{ note: ReviewNote; rev: number }>
  // Optimistic-concurrency token the backend checks addNote against. Task 23
  // owns the authoritative notes list (and therefore the current `rev`) via
  // listNotes() — threading it in as a prop here is the simplest option that
  // doesn't duplicate that fetch inside a leaf view component. Defaults to 0
  // (an empty/never-fetched store), which is also what a fresh review round
  // starts at server-side.
  expectedRev?: number
  // Test-only override: lets FileViewer.test.tsx supply a fixed hash without
  // depending on jsdom's crypto.subtle. Production callers never pass this —
  // FileViewer computes it itself (see computeContentSha below) once per
  // loaded file.
  contentSha?: string
}

// Показывает содержимое выбранного файла, построчно (Task 20 — по образцу
// PlanPanel: каждая строка — свой div, кликабельный, с ●-маркером и инлайновой
// формой комментария). ЕДИНСТВЕННОЕ место во всём дереве file-browser, где
// используется dangerouslySetInnerHTML — и оно заведомо безопасно: HTML каждой
// строки приходит только из highlight()+splitHighlightedLines() (highlight.ts),
// которые либо экранируют исходник целиком (язык 'plain'/незарегистрированный),
// либо прогоняют его через hljs.highlight (сам hljs экранирует текст внутри
// найденных токенов) с последующей пере-балансировкой тегов по границам строк.
// Контент НИКОГДА не идёт через markdown-it — в отличие от плана/диалога
// (PlanPanel/DialogChannel), это чужой исходный код проекта, а не markdown,
// написанный агентом.
export function FileViewer({
  content,
  loading,
  error,
  root,
  flowPauseState = 'none',
  pauseFlow,
  addNote,
  expectedRev = 0,
  contentSha,
}: FileViewerProps): ReactElement {
  const [comments, setComments] = useState<Record<number, string>>({})
  const [activeCommentLine, setActiveCommentLine] = useState<number | null>(null)
  const [draft, setDraft] = useState('')
  const [saving, setSaving] = useState(false)
  const [computedSha, setComputedSha] = useState<string | null>(null)
  // Line clicked while flowPauseState === 'none', awaiting the user's
  // yes/no on the pause-gate confirm. Cleared on "Нет", on reopening the
  // same pending line, and once the effect below opens the editor for it
  // after the flow actually reports 'paused'.
  const [pendingPauseLine, setPendingPauseLine] = useState<number | null>(null)

  const canUseNotes = addNote !== undefined && root !== undefined && content !== null
  const canAnnotate = flowPauseState === 'paused' && canUseNotes
  const canRequestPause = flowPauseState === 'none' && canUseNotes && pauseFlow !== undefined
  const sha = contentSha ?? computedSha

  // Открыть отложенную строку, как только флоу реально встал на паузу — тот
  // самый переход, ради которого пользователь нажал "Да" в конфирме. Статус
  // приходит через опрос /api/status снаружи (см. проп flowPauseState), так
  // что этот эффект — единственное место, которое реагирует на изменение.
  useEffect(() => {
    if (flowPauseState !== 'paused' || pendingPauseLine === null) return
    setActiveCommentLine(pendingPauseLine)
    setDraft(comments[pendingPauseLine] ?? '')
    setPendingPauseLine(null)
    // `comments` deliberately excluded: it only changes after a save, and by
    // then pendingPauseLine has already been cleared above, so re-running
    // this effect on a comments change is a no-op guarded by that check.
  }, [flowPauseState, pendingPauseLine])

  // Свежий файл — свежее состояние комментариев/формы. Ключ по path, а не по
  // самому content: тот же файл, перезагруженный после Reload (см.
  // FileBrowserModal), не должен сбрасывать уже сохранённые в этой сессии
  // маркеры комментариев.
  useEffect(() => {
    setComments({})
    setActiveCommentLine(null)
    setDraft('')
  }, [content?.path])

  // content_sha считается один раз на загруженный файл — не на каждый рендер
  // и не на каждое нажатие Save (см. бриф: "Compute content_sha once per
  // loaded file"). contentSha-проп (тестовый) пропускает вычисление целиком.
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
    return splitHighlightedLines(highlight(content.language, content.content))
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
      setDraft(comments[line] ?? '')
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
  }

  function confirmPause() {
    if (pauseFlow === undefined) return
    // pendingPauseLine stays set — the useEffect above opens the editor for
    // it once flowPauseState actually reports 'paused'. On failure, clear it
    // so the user can retry the click instead of being stuck silently.
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
    } finally {
      setSaving(false)
    }
  }

  function renderLine(lineNumber: number, html: string): ReactNode {
    const hasComment = comments[lineNumber] !== undefined
    const editing = activeCommentLine === lineNumber
    const confirmingPause = pendingPauseLine === lineNumber
    // A line looks/behaves clickable both when it opens the editor directly
    // (canAnnotate) and when it opens the pause-gate confirm instead
    // (canRequestPause) — the affordance is "you can leave a note here" in
    // both cases, only what happens on click differs.
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
            <p>Чтобы писать заметки, нужно поставить флоу на паузу. Поставить?</p>
            <div className="comment-actions">
              <button className="btn btn-send" type="button" onClick={confirmPause}>
                Да
              </button>
              <button className="btn btn-cancel" type="button" onClick={declinePause}>
                Нет
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
            />
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

// computeContentSha реализует ровно ту формулу, что задана брифом:
// 'sha256:' + hex(SHA-256(content)) через WebCrypto — тот же алгоритм сервер
// использует для проверки content_sha при POST /api/flow/notes (см.
// addNote в run-client.ts), поэтому клиентский хэш обязан совпасть байт-в-байт.
async function computeContentSha(text: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text))
  return `sha256:${toHex(new Uint8Array(digest))}`
}

function toHex(bytes: Uint8Array): string {
  let hex = ''
  for (const byte of bytes) hex += byte.toString(16).padStart(2, '0')
  return hex
}
