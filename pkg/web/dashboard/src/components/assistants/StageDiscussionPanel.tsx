import { useCallback, useEffect, useRef, useState, type ReactElement } from 'react'
import { listStageDiscussion, postStageDiscussionMessage, type DiscussionMessageWire, type DiscussionPost } from '../../api/run-client'
import { StageDiscussion, type DiscussionMessage } from './StageDiscussion'
import type { MeshParticipant } from '../../api/mesh-client'
import { ChatSendButton } from '../chat-composer/ChatSendButton'
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'
import { useCaretInsert } from '../../hooks/use-caret-insert'
import { MentionMenu } from './AssistantMentionMenu'
import { draftMentions, filterMentionProfiles } from './mention-match'
import { useMentionMenu } from './use-mention-menu'
import { mentionHandles, participantLabel, senderLabel } from '../../lib/mesh-roster'
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
    mentions: w.mentions,
    unanswered: w.unanswered,
    createdAt: w.created_at,
  }
}

export type StageDiscussionPanelProps = {
  stageId: string
  // participants — the Agent Mesh roster, so authors are shown by name and can be pulled in with `@`.
  participants?: MeshParticipant[]
  // stageStatus — the stage's live status: "Interrupt agent" is offered only while it is running.
  stageStatus?: string
}

type DiscussionMention = { id: string; handle: string; title: string }

// StageDiscussionPanel is the mounted, stateful wrapper around the presentational StageDiscussion: it polls
// the stage's discussion log (GET /api/stages/{id}/discussion, its own poll — independent of /api/status, the
// same posture as useStageAssistants) and lets the operator append a human message. Posting never touches the
// stage FSM (the message is observational), so the only feedback needed is the message appearing on the next
// poll. `@` pulls other agents into the post (shared mention primitives, as in the Team conversation), and
// "Interrupt agent" asks a running stage agent to stop its turn and answer now instead of at its next inbox read.
const MAX_INPUT_HEIGHT = 200

export function StageDiscussionPanel({ stageId, participants = [], stageStatus }: StageDiscussionPanelProps): ReactElement {
  const [messages, setMessages] = useState<DiscussionMessage[]>([])
  const [draft, setDraft] = useState('')
  const [interrupt, setInterrupt] = useState(false)
  // A post belongs to the stage it was written for: switching stage fences its late response out.
  const submitter = useRetryableSubmit<{ stageId: string } & DiscussionPost>('disc', stageId)
  // log — opens at the latest message and follows new ones, unless the reader scrolled up.
  const log = useStickToBottom<HTMLDivElement>()
  const autoGrowRef = useAutoGrowTextarea(draft, MAX_INPUT_HEIGHT)
  const { nodeRef: caretNodeRef, replaceRange } = useCaretInsert(draft, setDraft)
  const setInputRef = useCallback(
    (node: HTMLTextAreaElement | null) => {
      caretNodeRef.current = node
      autoGrowRef(node)
    },
    [autoGrowRef, caretNodeRef],
  )
  const cursorRef = useRef(0)

  // Mention candidates — everyone in the run who can still read a post, but you and this stage's own agent (it
  // gets every post anyway), each under an `@handle` unique among them, so one mention never means two
  // participants. A finished agent is not offered: the post would never reach it.
  const candidates = participants.filter((p) => p.id !== 'user' && p.id !== `stage:${stageId}` && p.reachable)
  const handles = mentionHandles(candidates)
  const handleOf = (p: MeshParticipant): string => handles.get(p.id) ?? p.id
  const mention = useMentionMenu<DiscussionMention>({
    matchesFor: (query) =>
      filterMentionProfiles(
        candidates.map((p) => ({ id: p.id, handle: handleOf(p), title: participantLabel(p) })),
        query,
      ),
    textFor: (item) => item.handle,
    replaceRange,
  })
  // mentioned — derived from the text, so deleting a mention from the draft also un-addresses that agent.
  const mentioned = candidates.filter((p) => draftMentions(draft, handleOf(p))).map((p) => p.id)
  const running = stageStatus === 'running'
  // finished — once the stage is done/failed the log is read again from the start: a post that was merely not
  // answered YET is now marked as never answered.
  const finished = stageStatus === 'done' || stageStatus === 'failed'

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
  }, [stageId, finished])

  function send(): void {
    const submitted = draft
    const body = submitted.trim()
    if (body === '') return
    const post = { stageId, body, mentions: mentioned, interrupt: running && interrupt }
    submitter.submit(post, (p, requestId) => postStageDiscussionMessage(p.stageId, requestId, { body: p.body, mentions: p.mentions, interrupt: p.interrupt }), {
      onSuccess: () => setDraft((cur) => removeSubmittedText(cur, submitted)),
    })
  }

  return (
    <div className="stage-discussion-panel">
      {/* Takes all free height (and scrolls), so the composer stays pinned to the bottom with or without messages. */}
      <div className="stage-discussion-scroll" role="log" aria-label="Discussion" ref={log.ref}>
        <StageDiscussion messages={messages} participants={participants} />
      </div>
      <div className="stage-discussion-route">
        <span>
          To the stage agent
          {mentioned.length > 0 && <strong> and {mentioned.map((id) => senderLabel(id, participants)).join(', ')}</strong>}
        </span>
        <span className="stage-discussion-route-hint">@ to bring in another agent</span>
        {running && (
          <label className="switch" title="The agent stops its current turn, answers this message, then continues its work">
            <input type="checkbox" checked={interrupt} onChange={(e) => setInterrupt(e.target.checked)} />
            <span className="track" aria-hidden="true" />
            <span className="switch-label">Interrupt agent</span>
          </label>
        )}
      </div>
      <div className="chat-composer-row">
        <textarea
          {...mention.inputAria}
          ref={setInputRef}
          className="chat-composer-input"
          placeholder="Add a message to this stage's discussion…"
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value)
            mention.onInput(e.target.value, e.target.selectionStart ?? e.target.value.length)
          }}
          onKeyDown={(e) => {
            if (mention.onKeyDown(e)) return
            if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
              e.preventDefault()
              send()
            }
          }}
          rows={1}
        />
        {mention.open && (
          <MentionMenu
            id={mention.listboxId}
            groups={[{ label: '', items: mention.matches }]}
            highlightIndex={mention.highlightIndex}
            onHover={mention.setHighlightIndex}
            onSelect={mention.pick}
            ariaLabel="Bring another agent into this discussion"
          />
        )}
        <ChatSendButton label="Send" disabled={draft.trim() === ''} sending={submitter.pending} onClick={send} />
      </div>
      {submitter.error !== null && <div className="stage-discussion-error" role="alert">{submitter.error}</div>}
    </div>
  )
}
