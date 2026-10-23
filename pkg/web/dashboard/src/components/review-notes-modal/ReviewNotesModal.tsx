import { useEffect, useRef, useState, type ReactElement } from 'react'
import { cancelNotes, deleteNote, injectNotes, listNotes, updateNote, FlowApiError } from '../../api/run-client'
import type { ReviewNote } from '../../types'
import { CommentCard } from '../comments/CommentCard'
import { CommentEditor } from '../comments/CommentEditor'

type ReviewNotesModalProps = {
  // Стадии, реально приостановленные прямо сейчас — только они могут быть целью инжекта заметок: агенту
  // некуда доставить контекст, пока сам он не стоит на паузе.
  pausedStages: string[]
  onClose: () => void
  // Кнопка ожидания закрывает модалку и открывает attention, которое иначе осталось бы за ней.
  attentionShortcut?: { label: string; onActivate: () => void } | null
}

// Тексты машиночитаемых кодов ошибок бэкенда — stale_content и rev_conflict — оба требуют перечитать
// список заново, а не тихо повторить тот же запрос с той же устаревшей ревизией.
function describeApiError(err: unknown): string {
  if (err instanceof FlowApiError) {
    if (err.code === 'stale_content') return 'The file changed since this note was created (stale content) — reload and retry.'
    if (err.code === 'rev_conflict') return 'Notes changed elsewhere (rev conflict) — reload and retry.'
    if (err.code !== undefined) return `Request failed: ${err.code}`
  }
  return err instanceof Error ? err.message : 'Request failed'
}

const noteTitle = 'Review note · sent with Continue'

function noteLabel(note: ReviewNote): string {
  return note.line !== null ? `review note on line ${note.line}` : 'file-level review note'
}

// Заметки одного файла идут одной группой, в порядке первого появления файла в списке (а не
// пересортировкой по алфавиту) — тот же порядок, в котором их отдал listNotes().
function groupByFile(notes: ReviewNote[]): Array<[string, ReviewNote[]]> {
  const order: string[] = []
  const groups = new Map<string, ReviewNote[]>()
  for (const note of notes) {
    const list = groups.get(note.display_path)
    if (list === undefined) {
      groups.set(note.display_path, [note])
      order.push(note.display_path)
    } else {
      list.push(note)
    }
  }
  return order.map((path) => [path, groups.get(path) ?? []])
}

// Модалка ревью-раунда: открывается по клику «Send notes» в ReviewBanner.
export function ReviewNotesModal({ pausedStages, onClose, attentionShortcut = null }: ReviewNotesModalProps): ReactElement {
  const [notes, setNotes] = useState<ReviewNote[]>([])
  const [rev, setRev] = useState(0)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)

  const [editingId, setEditingId] = useState<string | null>(null)
  // Правки по ID заметки: крестик/Escape сворачивают редактор, но набранное остаётся до Save или Delete.
  const [drafts, setDrafts] = useState<Readonly<Record<string, string>>>({})

  const [selectedStage, setSelectedStage] = useState(pausedStages[0] ?? '')
  const listRef = useRef<HTMLDivElement | null>(null)
  const restoreFocusId = useRef<string | null>(null)

  // После выхода из редактора фокус возвращается на Edit той же заметки.
  useEffect(() => {
    if (editingId !== null || restoreFocusId.current === null) return
    const id = restoreFocusId.current
    restoreFocusId.current = null
    const item = Array.from(listRef.current?.querySelectorAll<HTMLElement>('[data-note-id]') ?? []).find((el) => el.dataset.noteId === id)
    item?.querySelector<HTMLElement>('[data-comment-edit]')?.focus()
  }, [editingId])

  useEffect(() => {
    let cancelled = false
    listNotes()
      .then((res) => {
        if (cancelled) return
        setNotes(res.notes)
        setRev(res.rev)
        setLoading(false)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setLoadError(describeApiError(err))
        setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [])

  function startEdit(note: ReviewNote): void {
    setActionError(null)
    setEditingId(note.id)
  }

  function foldEdit(id: string): void {
    restoreFocusId.current = id
    setEditingId(null)
  }

  function setDraft(id: string, text: string): void {
    setDrafts((prev) => ({ ...prev, [id]: text }))
  }

  function clearDraft(id: string): void {
    setDrafts(({ [id]: _removed, ...rest }) => rest)
  }

  async function saveEdit(id: string, draft: string): Promise<void> {
    const text = draft.trim()
    if (text === '') return
    try {
      const res = await updateNote(id, text, rev)
      setRev(res.rev)
      setNotes((prev) => prev.map((n) => (n.id === id ? { ...n, text } : n)))
      clearDraft(id)
      foldEdit(id)
      setActionError(null)
    } catch (err) {
      setActionError(describeApiError(err))
    }
  }

  async function handleDelete(id: string): Promise<void> {
    try {
      await deleteNote(id, rev)
      // После удаления перечитываем notes: следующий update/delete требует свежей rev.
      const res = await listNotes()
      setNotes(res.notes)
      setRev(res.rev)
      clearDraft(id)
      setEditingId(null)
      setActionError(null)
    } catch (err) {
      setActionError(describeApiError(err))
    }
  }

  async function handleInject(): Promise<void> {
    try {
      await injectNotes(selectedStage)
      onClose()
    } catch (err) {
      setActionError(describeApiError(err))
    }
  }

  async function handleCancel(): Promise<void> {
    try {
      await cancelNotes()
      onClose()
    } catch (err) {
      setActionError(describeApiError(err))
    }
  }

  const groups = groupByFile(notes)
  const noStages = pausedStages.length === 0

  return (
    <div className="modal-overlay review-notes-overlay" role="dialog" aria-modal="true" aria-label="Review notes">
      <div className="modal-content review-notes-modal">
        {attentionShortcut !== null && (
          <div className="modal-attention-row">
            <button
              type="button"
              className="modal-attention-shortcut"
              onClick={() => { onClose(); attentionShortcut.onActivate() }}
            >
              <span className="attn-dot" aria-hidden="true" />
              {attentionShortcut.label}
            </button>
          </div>
        )}
        <h2 className="review-notes-title">Review notes</h2>

        {loadError !== null && <p className="review-notes-error" role="alert">{loadError}</p>}
        {actionError !== null && <p className="review-notes-error" role="alert">{actionError}</p>}

        <div className="review-notes-list" ref={listRef}>
          {loading ? (
            <p className="review-notes-hint">Loading notes…</p>
          ) : groups.length === 0 ? (
            <p className="review-notes-hint">No notes collected yet.</p>
          ) : (
            groups.map(([displayPath, fileNotes]) => (
              <div className="review-notes-group" key={displayPath}>
                <h3 className="review-notes-file">{displayPath}</h3>
                <ul className="review-notes-items">
                  {fileNotes.map((note) => {
                    const draft = drafts[note.id]
                    return (
                      <li key={note.id} data-note-id={note.id}>
                        {editingId === note.id ? (
                          <CommentEditor
                            context="review"
                            title={noteTitle}
                            label={noteLabel(note)}
                            quote={note.orig_line_text ?? ''}
                            value={draft ?? note.text}
                            onChange={(text) => setDraft(note.id, text)}
                            onSave={() => void saveEdit(note.id, draft ?? note.text)}
                            saveDisabled={(draft ?? note.text).trim() === ''}
                            onFold={() => foldEdit(note.id)}
                            onDiscard={() => void handleDelete(note.id)}
                            discardLabel="Delete"
                            saveLabel="Save"
                          />
                        ) : (
                          <CommentCard
                            title={noteTitle}
                            label={noteLabel(note)}
                            caption={note.line !== null ? `Line ${note.line}` : 'File-level'}
                            quote={note.orig_line_text ?? ''}
                            text={note.text}
                            draft={draft !== undefined && draft !== note.text}
                            onEdit={() => startEdit(note)}
                            actions={
                              <button
                                type="button"
                                className="btn btn-cancel"
                                aria-label={`Delete ${noteLabel(note)}`}
                                onClick={() => void handleDelete(note.id)}
                              >
                                Delete
                              </button>
                            }
                          />
                        )}
                      </li>
                    )
                  })}
                </ul>
              </div>
            ))
          )}
        </div>

        <div className="review-notes-target">
          <label className="review-notes-target-label" htmlFor="review-notes-stage-select">
            Target stage
          </label>
          <select
            id="review-notes-stage-select"
            value={selectedStage}
            onChange={(e) => setSelectedStage(e.target.value)}
            disabled={noStages}
          >
            {noStages ? (
              <option value="">No active stage</option>
            ) : (
              pausedStages.map((stageId) => (
                <option key={stageId} value={stageId}>
                  {stageId}
                </option>
              ))
            )}
          </select>
          {noStages && <p className="review-notes-hint">There is no active stage to receive notes.</p>}
        </div>

        <div className="modal-actions review-notes-modal-actions">
          <button type="button" className="btn btn-cancel" onClick={() => void handleCancel()}>
            Cancel
          </button>
          <button type="button" className="btn btn-send" disabled={noStages} onClick={() => void handleInject()}>
            Inject
          </button>
        </div>
      </div>
    </div>
  )
}
