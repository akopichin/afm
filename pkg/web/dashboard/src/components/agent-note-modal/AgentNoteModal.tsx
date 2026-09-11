import { useState, type ReactElement } from 'react'
import { PasteableTextarea } from '../pasteable-textarea'

type AgentNoteModalProps = {
  stageId: string
  onSubmit: (note: string) => void
  onCancel: () => void
  // variant='revise' (по умолчанию) — поправка живому агенту (agent_suggest):
  // текст обязателен, кнопка «Send», агент перезапустится с ним в контексте.
  // variant='prenote' — заметка ещё не стартовавшей стадии: префилл текущим
  // текстом, пустой текст разрешён (= удалить заметку), кнопка «Save».
  variant?: 'revise' | 'prenote'
  initialNote?: string
  // Шорткат к ждущему действию (Finding #5, раунд 3): если во время открытой
  // заметки появляется/висит ожидание, в шапке показывается кнопка, которая
  // закрывает модалку и открывает нужный attention-воркспейс. null — нет ожидания.
  attentionShortcut?: { label: string; onActivate: () => void } | null
}

// Модалка заметки агенту: открывается из кебаб-меню StagesList. Для 'revise'
// предупреждает, что агент доведёт текущее действие до конца перед
// перезапуском; для 'prenote' — что заметка попадёт в контекст при старте.
export function AgentNoteModal({
  stageId,
  onSubmit,
  onCancel,
  variant = 'revise',
  initialNote = '',
  attentionShortcut = null,
}: AgentNoteModalProps): ReactElement {
  const [note, setNote] = useState(initialNote)
  const isPreNote = variant === 'prenote'

  return (
    <div className="modal-overlay" role="dialog" aria-modal="true" aria-label={`Add a note for stage ${stageId}`}>
      <div className="modal-content agent-note-modal">
        {attentionShortcut !== null && (
          <div className="modal-attention-row">
            <button
              type="button"
              className="modal-attention-shortcut"
              onClick={() => { onCancel(); attentionShortcut.onActivate() }}
            >
              <span className="attn-dot" aria-hidden="true" />
              {attentionShortcut.label}
            </button>
          </div>
        )}
        <p className="agent-note-warning">
          {isPreNote
            ? 'This note will be added to the agent’s context when the stage starts. Clear it and save to remove.'
            : 'The agent will finish its current action, then restart with this note in context.'}
        </p>
        <PasteableTextarea
          stageId={stageId}
          className="agent-note-textarea"
          value={note}
          onChange={setNote}
          placeholder="What should the agent take into account?"
          autoFocus
          // Заметка агенту тоже может ссылаться на файлы проекта и прикладывать
          // изображение — скрепка появляется только при включённом файловом
          // браузере (Docker), на хосте её нет (как и у остальных полей).
          allowFileReferences
          // Cmd/Ctrl+Enter отправляет заметку — тот же гейт, что и у кнопки Send/Save.
          onSubmit={() => { if (isPreNote || note.trim() !== '') onSubmit(note.trim()) }}
        />
        <div className="modal-actions">
          <button type="button" className="btn btn-cancel" onClick={onCancel}>
            Cancel
          </button>
          <button
            type="button"
            className="btn btn-send"
            disabled={!isPreNote && note.trim() === ''}
            onClick={() => onSubmit(note.trim())}
          >
            {isPreNote ? 'Save' : 'Send'}
          </button>
        </div>
      </div>
    </div>
  )
}
