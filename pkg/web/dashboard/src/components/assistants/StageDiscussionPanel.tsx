import { useEffect, useRef, useState, type ReactElement } from 'react'
import { listStageDiscussion, postStageDiscussionMessage, type DiscussionMessageWire } from '../../api/run-client'
import { StageDiscussion, type DiscussionMessage } from './StageDiscussion'
import { ChatSendButton } from '../chat-composer/ChatSendButton'
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'

const POLL_INTERVAL_MS = 3000

function toMessage(w: DiscussionMessageWire): DiscussionMessage {
  return {
    id: w.id,
    authorKind: w.author_kind,
    authorId: w.author_id,
    role: w.role,
    executor: w.executor,
    round: w.round,
    parentMessageId: w.parent_message_id,
    contextVersion: w.context_version,
    evidence: w.evidence,
    body: w.body ?? '',
    createdAt: w.created_at,
  }
}

export type StageDiscussionPanelProps = {
  stageId: string
}

// StageDiscussionPanel is the mounted, stateful wrapper around the presentational StageDiscussion: it polls
// the stage's discussion log (GET /api/stages/{id}/discussion, its own poll — independent of /api/status, the
// same posture as useStageAssistants) and lets the operator append a human message. Posting never touches the
// stage FSM (the message is observational), so the only feedback needed is the message appearing on the next
// poll.
const MAX_INPUT_HEIGHT = 200

export function StageDiscussionPanel({ stageId }: StageDiscussionPanelProps): ReactElement {
  const [messages, setMessages] = useState<DiscussionMessage[]>([])
  const [draft, setDraft] = useState('')
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const autoGrowRef = useAutoGrowTextarea(draft, MAX_INPUT_HEIGHT)
  const cursorRef = useRef(0)

  useEffect(() => {
    setMessages([])
    cursorRef.current = 0
    let cancelled = false
    const load = async (): Promise<void> => {
      try {
        for (;;) {
          const page = await listStageDiscussion(stageId, cursorRef.current)
          if (cancelled) return
          if (page.messages.length > 0) {
            setMessages((prev) => prev.concat(page.messages.map(toMessage)))
          }
          cursorRef.current = page.nextCursor
          if (!page.hasMore) return
        }
      } catch {
        /* transient — keep what we have, retry next tick from the same cursor */
      }
    }
    void load()
    const timer = setInterval(() => void load(), POLL_INTERVAL_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [stageId])

  function send(): void {
    const text = draft.trim()
    if (text === '' || sending) return
    setSending(true)
    setError(null)
    postStageDiscussionMessage(stageId, `disc-${Date.now()}-${Math.random().toString(16).slice(2)}`, text)
      .then(() => setDraft(''))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : 'request failed'))
      .finally(() => setSending(false))
  }

  return (
    <div className="stage-discussion-panel">
      <StageDiscussion messages={messages} />
      <div className="chat-composer-row">
        <textarea
          ref={autoGrowRef}
          className="chat-composer-input"
          placeholder="Add a message to this stage's discussion…"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
              e.preventDefault()
              send()
            }
          }}
          rows={1}
        />
        <ChatSendButton label="Send" disabled={sending || draft.trim() === ''} onClick={send} />
      </div>
      {error !== null && <div className="stage-discussion-error" role="alert">{error}</div>}
    </div>
  )
}
