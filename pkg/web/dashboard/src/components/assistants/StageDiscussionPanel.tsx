import { useEffect, useRef, useState, type ReactElement } from 'react'
import { listStageDiscussion, postStageDiscussionMessage, type DiscussionMessageWire } from '../../api/run-client'
import { StageDiscussion, type DiscussionMessage } from './StageDiscussion'
import type { MeshParticipant } from '../../api/mesh-client'
import { ChatSendButton } from '../chat-composer/ChatSendButton'
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'
import { useStickToBottom } from '../../hooks/use-stick-to-bottom'
import { removeSubmittedText, useRetryableSubmit } from '../../hooks/use-retryable-submit'
import { startSerialPoll } from '../../lib/serial-poll'

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
  // participants — the Agent Mesh roster, so authors are shown by name.
  participants?: MeshParticipant[]
}

// StageDiscussionPanel is the mounted, stateful wrapper around the presentational StageDiscussion: it polls
// the stage's discussion log (GET /api/stages/{id}/discussion, its own poll — independent of /api/status, the
// same posture as useStageAssistants) and lets the operator append a human message. Posting never touches the
// stage FSM (the message is observational), so the only feedback needed is the message appearing on the next
// poll.
const MAX_INPUT_HEIGHT = 200

export function StageDiscussionPanel({ stageId, participants }: StageDiscussionPanelProps): ReactElement {
  const [messages, setMessages] = useState<DiscussionMessage[]>([])
  const [draft, setDraft] = useState('')
  // A post belongs to the stage it was written for: switching stage fences its late response out.
  const submitter = useRetryableSubmit<{ stageId: string; body: string }>('disc', stageId)
  // log — opens at the latest message and follows new ones, unless the reader scrolled up.
  const log = useStickToBottom<HTMLDivElement>()
  const autoGrowRef = useAutoGrowTextarea(draft, MAX_INPUT_HEIGHT)
  const cursorRef = useRef(0)

  useEffect(() => {
    setMessages([])
    cursorRef.current = 0
    // Serial: two slow polls never read the same cursor, so a page is never appended twice.
    const poll = startSerialPoll(async (isStopped) => {
      try {
        for (;;) {
          const page = await listStageDiscussion(stageId, cursorRef.current)
          if (isStopped()) return
          if (page.messages.length > 0) {
            setMessages((prev) => prev.concat(page.messages.map(toMessage)))
          }
          cursorRef.current = page.nextCursor
          if (!page.hasMore) return
        }
      } catch {
        /* transient — keep what we have, retry next tick from the same cursor */
      }
    }, POLL_INTERVAL_MS)
    return poll.stop
  }, [stageId])

  function send(): void {
    const submitted = draft
    const body = submitted.trim()
    if (body === '') return
    submitter.submit({ stageId, body }, (p, requestId) => postStageDiscussionMessage(p.stageId, requestId, p.body), {
      onSuccess: () => setDraft((cur) => removeSubmittedText(cur, submitted)),
    })
  }

  return (
    <div className="stage-discussion-panel">
      {/* Takes all free height (and scrolls), so the composer stays pinned to the bottom with or without messages. */}
      <div className="stage-discussion-scroll" role="log" aria-label="Discussion" ref={log.ref}>
        <StageDiscussion messages={messages} participants={participants} />
      </div>
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
        <ChatSendButton label="Send" disabled={draft.trim() === ''} sending={submitter.pending} onClick={send} />
      </div>
      {submitter.error !== null && <div className="stage-discussion-error" role="alert">{submitter.error}</div>}
    </div>
  )
}
