import { useEffect, useState, type ReactElement } from 'react'
import { cancelNotes, deleteNote, injectNotes, listNotes, updateNote, FlowApiError } from '../../api/run-client'
import type { ReviewNote } from '../../types'

type ReviewNotesModalProps = {
  // Стадии, реально приостановленные прямо сейчас (см. use-status.ts's
  // flowPausedStages) — только они могут быть целью инжекта заметок: агенту
  // некуда доставить контекст, пока сам он не стоит на паузе. Пустой массив —
  // Inject недоступен, доставлять некуда (см. дизейбл ниже).
  pausedStages: string[]
  onClose: () => void
  // Шорткат к ждущему действию (Finding #5, раунд 3) — начальный экран модалки
  // ревью может не иметь input'а, поэтому новое ожидание могло бы авто-открыться
  // за непрозрачной модалкой; кнопка в шапке закрывает её и открывает attention.
  attentionShortcut?: { label: string; onActivate: () => void } | null
}

// Тексты машиночитаемых кодов ошибок бэкенда (см. FlowApiError.code в
// run-client.ts) — stale_content (addNote'а содержимое файла изменилось с
// момента постановки заметки) и rev_conflict (кто-то ещё поменял стор заметок
// между чтением rev и этим update/delete) — оба требуют перечитать список
// заново, а не тихо повторить тот же запрос с той же устаревшей ревизией.
function describeApiError(err: unknown): string {
  if (err instanceof FlowApiError) {
    if (err.code === 'stale_content') return 'The file changed since this note was created (stale content) — reload and retry.'
    if (err.code === 'rev_conflict') return 'Notes changed elsewhere (rev conflict) — reload and retry.'
    if (err.code !== undefined) return `Request failed: ${err.code}`
  }
  return err instanceof Error ? err.message : 'Request failed'
}

// Заметки одного файла идут одной группой, в порядке первого появления файла
// в списке (а не пересортировкой по алфавиту) — тот же порядок, в котором их
// отдал listNotes().
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

// Модалка ревью-раунда (Task 23): открывается по клику «Send notes» в
// ReviewBanner (Task 22). Показывает собранные заметки, сгруппированные по
// файлу, с редактированием/удалением на месте, плюс выбор целевой стадии и
// финальное решение — Inject (доставить в стадию и возобновить флоу) или
// Cancel (отменить раунд, заметки остаются в сторе на следующий раз).
export function ReviewNotesModal({ pausedStages, onClose, attentionShortcut = null }: ReviewNotesModalProps): ReactElement {
  const [notes, setNotes] = useState<ReviewNote[]>([])
  const [rev, setRev] = useState(0)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)

  const [editingId, setEditingId] = useState<string | null>(null)
  const [editDraft, setEditDraft] = useState('')

  const [selectedStage, setSelectedStage] = useState(pausedStages[0] ?? '')

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
    setEditDraft(note.text)
  }

  function cancelEdit(): void {
    setEditingId(null)
    setEditDraft('')
  }

  async function saveEdit(id: string): Promise<void> {
    const text = editDraft.trim()
    if (text === '') return
    try {
      const res = await updateNote(id, text, rev)
      setRev(res.rev)
      setNotes((prev) => prev.map((n) => (n.id === id ? { ...n, text } : n)))
      setEditingId(null)
      setEditDraft('')
      setActionError(null)
    } catch (err) {
      setActionError(describeApiError(err))
    }
  }

  async function handleDelete(id: string): Promise<void> {
    try {
      await deleteNote(id, rev)
      // Ре-читаем целиком, а не просто выкидываем note из локального массива:
      // deleteNote возвращает только void, а нам нужна свежая rev для
      // последующих update/delete — как и написано в брифе Task 23.
      const res = await listNotes()
      setNotes(res.notes)
      setRev(res.rev)
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

        <div className="review-notes-list">
          {loading ? (
            <p className="review-notes-hint">Loading notes…</p>
          ) : groups.length === 0 ? (
            <p className="review-notes-hint">No notes collected yet.</p>
          ) : (
            groups.map(([displayPath, fileNotes]) => (
              <div className="review-notes-group" key={displayPath}>
                <h3 className="review-notes-file">{displayPath}</h3>
                <ul className="review-notes-items">
                  {fileNotes.map((note) => (
                    <li key={note.id} className="review-note-item">
                      {editingId === note.id ? (
                        <div className="review-note-edit">
                          <textarea
                            className="review-note-textarea"
                            value={editDraft}
                            onChange={(e) => setEditDraft(e.target.value)}
                            autoFocus
                          />
                          <div className="review-note-actions">
                            <button type="button" className="btn btn-send" disabled={editDraft.trim() === ''} onClick={() => void saveEdit(note.id)}>
                              Save
                            </button>
                            <button type="button" className="btn btn-cancel" onClick={cancelEdit}>
                              Cancel
                            </button>
                          </div>
                        </div>
                      ) : (
                        <>
                          <span className="review-note-line">{note.line !== null ? `Line ${note.line}` : 'File-level'}</span>
                          <p className="review-note-text">{note.text}</p>
                          <div className="review-note-actions">
                            <button type="button" className="btn btn-edit" onClick={() => startEdit(note)}>
                              Edit
                            </button>
                            <button type="button" className="btn btn-delete" onClick={() => void handleDelete(note.id)}>
                              Delete
                            </button>
                          </div>
                        </>
                      )}
                    </li>
                  ))}
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
