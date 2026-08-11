import { useState, type ReactElement } from 'react'
import { PasteableTextarea } from '../pasteable-textarea'

type AgentNoteModalProps = {
  stageId: string
  onSubmit: (note: string) => void
  onCancel: () => void
}

// Модалка «Добавить поправку агенту» (agent_suggest): открывается из кебаб-
// меню StagesList. Предупреждает, что агент доведёт текущее действие до
// конца перед перезапуском с этой фразой в контексте.
export function AgentNoteModal({ stageId, onSubmit, onCancel }: AgentNoteModalProps): ReactElement {
  const [note, setNote] = useState('')

  return (
    <div className="modal-overlay" role="dialog" aria-modal="true" aria-label={`Add a note for stage ${stageId}`}>
      <div className="modal-content agent-note-modal">
        <p className="agent-note-warning">
          The agent will finish its current action, then restart with this note in context.
        </p>
        <PasteableTextarea
          stageId={stageId}
          className="agent-note-textarea"
          value={note}
          onChange={setNote}
          placeholder="What should the agent take into account?"
          autoFocus
        />
        <div className="modal-actions">
          <button type="button" className="btn btn-cancel" onClick={onCancel}>
            Cancel
          </button>
          <button
            type="button"
            className="btn btn-send"
            disabled={note.trim() === ''}
            onClick={() => onSubmit(note.trim())}
          >
            Send
          </button>
        </div>
      </div>
    </div>
  )
}
