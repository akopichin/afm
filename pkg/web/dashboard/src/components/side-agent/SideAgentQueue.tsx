import { useEffect, useRef, useState, type ReactElement } from 'react'
import type { QueueRemoval, QueuedMessage } from '../../hooks/use-side-agent'

const LONG_TEXT_CHARS = 200
const LONG_TEXT_LINES = 3

export type SideAgentQueueProps = {
  queue: QueuedMessage[]
  removal: Readonly<Record<string, QueueRemoval>>
  stages: { id: string; title: string }[]
  onRemove: (messageId: string) => void
  // Куда уйдёт фокус, когда после удаления в очереди не осталось действий.
  onFocusComposer: () => void
}

function isLong(text: string): boolean {
  return text.length > LONG_TEXT_CHARS || text.split('\n').length > LONG_TEXT_LINES
}

function QueuedText({ text }: { text: string }): ReactElement {
  const [expanded, setExpanded] = useState(false)
  const long = isLong(text)
  return (
    <>
      <div className={`side-agent-queue-text${long && !expanded ? ' collapsed' : ''}`}>{text}</div>
      {long && (
        <button type="button" className="side-agent-queue-toggle" aria-expanded={expanded} onClick={() => setExpanded(!expanded)}>
          {expanded ? 'Show less' : 'Show more'}
        </button>
      )}
    </>
  )
}

// SideAgentQueue — сообщения, принятые сервером, но ещё не взятые агентом в работу. Принято не значит
// прочитано; запись можно убрать без подтверждения, пока её не забрал ход.
export function SideAgentQueue({ queue, removal, stages, onRemove, onFocusComposer }: SideAgentQueueProps): ReactElement | null {
  const buttons = useRef(new Map<string, HTMLButtonElement>())
  // Удалённая запись и её соседа по очереди: фокус переезжает, когда запись реально ушла из очереди.
  const pendingFocus = useRef<{ id: string; fallbackId: string | null } | null>(null)

  useEffect(() => {
    const pending = pendingFocus.current
    if (pending === null || queue.some((m) => m.messageId === pending.id)) return
    pendingFocus.current = null
    // Пользователь уже мог перейти в другое место — его фокус не трогаем.
    if (document.activeElement !== document.body) return
    const next = pending.fallbackId === null ? undefined : buttons.current.get(pending.fallbackId)
    if (next !== undefined) next.focus()
    else onFocusComposer()
  }, [queue, onFocusComposer])

  if (queue.length === 0) return null

  function remove(index: number): void {
    const target = queue[index]!
    if (removal[target.messageId]?.status === 'removing') return
    pendingFocus.current = { id: target.messageId, fallbackId: (queue[index + 1] ?? queue[index - 1])?.messageId ?? null }
    onRemove(target.messageId)
  }

  const stageTitle = (id: string): string => (id === '' ? 'No stage' : stages.find((s) => s.id === id)?.title || id)

  return (
    <section className="side-agent-queue" aria-label="Queued messages">
      <h3 className="side-agent-queue-title">Queued ({queue.length})</h3>
      <p className="side-agent-queue-note">Sent to the server — the Assistant has not read these yet.</p>
      <ul className="side-agent-queue-list">
        {queue.map((m, index) => {
          const state = removal[m.messageId]
          const removing = state?.status === 'removing'
          return (
            <li key={m.messageId} className="side-agent-queue-item">
              <QueuedText text={m.text} />
              <div className="side-agent-queue-meta">
                <span className="side-agent-queue-context">Context: {stageTitle(m.stageId)}</span>
                {state?.status === 'error' && <span className="side-agent-queue-error" role="alert">{state.message}</span>}
                <button
                  type="button"
                  className="side-agent-queue-remove"
                  aria-disabled={removing ? true : undefined}
                  ref={(node) => {
                    if (node === null) buttons.current.delete(m.messageId)
                    else buttons.current.set(m.messageId, node)
                  }}
                  onClick={() => remove(index)}
                >
                  {removing ? 'Removing…' : state?.status === 'error' ? 'Retry' : 'Remove from queue'}
                </button>
              </div>
            </li>
          )
        })}
      </ul>
    </section>
  )
}
