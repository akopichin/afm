import { type KeyboardEvent, type ReactElement } from 'react'
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'

const MAX_TEXTAREA_HEIGHT = 200

export type SideAgentComposerProps = {
  draft: string
  onDraftChange: (value: string) => void
  // Отправить текущий черновик. Вызывается только при непустом (trim) тексте и
  // когда агент не занят (running → поле редактируемо, но Send недоступен).
  onSend: () => void
  // Прервать активный turn (кнопка Stop показывается только во время running).
  onStop: () => void
  running: boolean
}

// SideAgentComposer — свой композер беседы (НЕ FeedComposer: у того Revise +
// Pause-on-focus, не нужные здесь). Растущая textarea на общих хуках, без
// stageId/вложений. Cmd/Ctrl+Enter — отправка, обычный Enter — новая строка.
// Поле редактируемо и во время работы агента; отправка следующего сообщения
// возможна только когда текущий turn завершён (Send disabled при running).
export function SideAgentComposer({ draft, onDraftChange, onSend, onStop, running }: SideAgentComposerProps): ReactElement {
  const textareaRef = useAutoGrowTextarea(draft, MAX_TEXTAREA_HEIGHT)
  const canSend = !running && draft.trim() !== ''

  function submit(): void {
    if (canSend) onSend()
  }

  function onKeyDown(e: KeyboardEvent<HTMLTextAreaElement>): void {
    // Cmd/Ctrl+Enter — отправка; обычный Enter оставляем textarea (новая строка).
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault()
      submit()
    }
  }

  return (
    <div className="side-agent-composer">
      <textarea
        ref={textareaRef}
        className="side-agent-textarea"
        value={draft}
        onChange={(e) => onDraftChange(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder="Message the agent…  (Cmd/Ctrl+Enter to send)"
        aria-label="Message the agent"
        rows={1}
      />
      <div className="side-agent-composer-actions">
        {running && (
          <button type="button" className="btn btn-cancel side-agent-stop" onClick={onStop}>
            Stop
          </button>
        )}
        <button type="button" className="btn btn-send side-agent-send" disabled={!canSend} onClick={submit}>
          Send
        </button>
      </div>
    </div>
  )
}
