import { useCallback, useRef, useState, type ReactElement } from 'react'
import { genMeshRequestId, openMeshThread, sendMeshMessage, type MeshMessage, type MeshParticipant, type MeshThread } from '../../api/mesh-client'
import { useMeshThread } from '../../hooks/use-mesh'
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'
import { useCaretInsert } from '../../hooks/use-caret-insert'
import { MentionMenu } from '../assistants/AssistantMentionMenu'
import { draftMentions, filterMentionProfiles } from '../assistants/mention-match'
import { useMentionMenu } from '../assistants/use-mention-menu'
import { findExistingDirectThread, mentionHandle, participantLabel, participantRole, senderLabel, threadLabel } from '../../lib/mesh-roster'
import { MeshAvatar } from './MeshAvatar'
import { ChatSendButton } from '../chat-composer/ChatSendButton'

// ThreadConversationTarget — what the Messages pane is currently pointed at: an ALREADY open thread (picked
// from the roster's "Conversations" group), or a person row with no thread yet ("direct" — opening one is
// deferred to the first message actually sent, so merely browsing the roster never creates empty threads).
export type ThreadConversationTarget =
  | { kind: 'thread'; thread: MeshThread }
  | { kind: 'direct'; participant: MeshParticipant }

export type ThreadConversationProps = {
  target: ThreadConversationTarget | null
  // onThreadOpened — fired once a "direct" target's first message lazily opens a real thread, so the parent
  // (TeamView) can fold this conversation into the roster's "Conversations" group instead of treating the NEXT
  // click on the same person row as yet another fresh direct target (which would open a second thread).
  onThreadOpened?: (thread: MeshThread) => void
  // onThreadCreated — fired as soon as a thread is opened, even if the message that follows fails to send,
  // so the parent knows about it right away (a later click on the same person must not open another one).
  onThreadCreated?: (thread: MeshThread) => void
  // participants — the roster, to name an untitled thread and each message's sender instead of showing ids.
  participants?: MeshParticipant[]
  // threads — the latest known threads. A direct target re-checks them when SENDING, not only when the person
  // was clicked: the roster poll may learn about an existing direct thread after the click.
  threads?: MeshThread[]
}

// targetKey — a stable identity string for a target, meant as the `key` prop on <ThreadConversation>. The
// component's open-thread/draft/error state is only ever correct for the target it was MOUNTED with (see
// the openedThread useState below), so the parent must remount on every selection change rather than
// re-rendering the same instance with a new `target` prop — otherwise a stale `openedThread` from the
// previous selection keeps receiving sends after the user switches conversations.
export function targetKey(target: ThreadConversationTarget | null): string {
  if (target === null) return 'none'
  return target.kind === 'direct' ? `direct:${target.participant.id}` : `thread:${target.thread.id}`
}

const MAX_INPUT_HEIGHT = 200

// conversationTitle — the conversation's name: the person for a direct target, else the thread's title or,
// for an untitled thread, its other members' names.
export function conversationTitle(target: ThreadConversationTarget, participants: MeshParticipant[]): string {
  return target.kind === 'direct' ? participantLabel(target.participant) : threadLabel(target.thread, participants)
}

// conversationSubtitle — the line under the title: who this is for a direct target, the head count for a thread.
export function conversationSubtitle(target: ThreadConversationTarget): string {
  if (target.kind === 'direct') return `Direct conversation · ${participantRole(target.participant.kind)}`
  const agents = target.thread.members.filter((m) => m !== 'user').length
  return `${target.thread.members.length} participants · you and ${agents} ${agents === 1 ? 'agent' : 'agents'}`
}

// unreachableNotice — when nobody else in the conversation can answer any more (their stage or task has
// finished), say so above the composer instead of letting messages silently go unanswered.
function unreachableNotice(target: ThreadConversationTarget, participants: MeshParticipant[]): string | null {
  const others = target.kind === 'direct' ? [target.participant.id] : target.thread.members.filter((m) => m !== 'user')
  const live = others.map((id) => participants.find((p) => p.id === id)).filter((p) => p !== undefined)
  if (live.length === 0 || live.some((p) => p.reachable)) return null
  return live.length === 1
    ? `${participantLabel(live[0] as MeshParticipant)} has finished and can't reply any more.`
    : 'Everyone in this conversation has finished and can\'t reply any more.'
}

// recipientsFor — every OTHER member of the conversation: a direct target has exactly one (the person
// clicked); an opened thread's recipients are its full member list minus the human operator itself.
function recipientsFor(target: ThreadConversationTarget, opened: MeshThread | null): string[] {
  if (target.kind === 'direct') return [target.participant.id]
  const thread = opened ?? target.thread
  return thread.members.filter((m) => m !== 'user')
}

type MemberMention = { id: string; handle: string; title: string; unavailable: boolean }

// otherMembers — everyone in the conversation except the operator, as roster participants.
function otherMembers(target: ThreadConversationTarget, opened: MeshThread | null, participants: MeshParticipant[]): MeshParticipant[] {
  if (target.kind === 'direct') return [target.participant]
  return recipientsFor(target, opened)
    .map((id) => participants.find((p) => p.id === id))
    .filter((p): p is MeshParticipant => p !== undefined)
}

// routeAll — the default recipient line: the person in a direct conversation, everyone in a thread.
function routeAll(target: ThreadConversationTarget, label: string): string {
  return target.kind === 'direct' ? label : 'Everyone in this conversation'
}

export function ThreadConversation({ target, onThreadOpened, onThreadCreated, participants = [], threads = [] }: ThreadConversationProps): ReactElement {
  const [openedThread, setOpenedThread] = useState<MeshThread | null>(target?.kind === 'thread' ? target.thread : null)
  const [draft, setDraft] = useState('')
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // requestIdRef — the SAME send's retry reuses this id; a NEW, successfully-sent message mints its own id
  // next time (each message is its own idempotent operation, unlike AddRunAgentDialog's single durable attach).
  const requestIdRef = useRef<string>(genMeshRequestId('mesh-send'))
  // notifiedRef — whether the parent has already been told about openedThread. A 'thread' target starts
  // already known to the parent (it IS activeConversation), so it starts true. A 'direct' target starts
  // false: once its first message opens a real thread, the parent must hear about it exactly once — even if
  // that happens on a RETRY after the open succeeded but an earlier send attempt failed (openedThread is set
  // from the failed attempt already, so a plain "did this call just open it" check misses the retry).
  const notifiedRef = useRef<boolean>(target?.kind === 'thread')

  // openRequestIdRef — one id for opening this conversation's thread, reused by every retry: if the server
  // created the thread but the response was lost, the retry gets that same thread back instead of a second one.
  const openRequestIdRef = useRef<string>(genMeshRequestId('mesh-open'))

  const autoGrowRef = useAutoGrowTextarea(draft, MAX_INPUT_HEIGHT)
  const { nodeRef: caretNodeRef, replaceRange } = useCaretInsert(draft, setDraft)
  const setInputRef = useCallback(
    (node: HTMLTextAreaElement | null) => {
      caretNodeRef.current = node
      autoGrowRef(node)
    },
    [autoGrowRef, caretNodeRef],
  )
  // Mention candidates — the other members of this conversation, addressed by their short @handle.
  const members = target === null ? [] : otherMembers(target, openedThread, participants)
  const mention = useMentionMenu<MemberMention>({
    matchesFor: (query) =>
      filterMentionProfiles(
        members.map((p) => ({ id: p.id, handle: mentionHandle(p), title: participantLabel(p), unavailable: !p.reachable })),
        query,
      ),
    textFor: (item) => item.handle,
    replaceRange,
  })
  // mentioned — recipients addressed with `@` in the draft right now; none means everyone else. Derived from
  // the text, so deleting a mention from the draft also un-addresses that member.
  const mentioned = members.filter((p) => draftMentions(draft, mentionHandle(p))).map((p) => p.id)
  const threadId = openedThread?.id ?? null
  const messages = useMeshThread(threadId)

  if (target === null) {
    return <div className="team-conversation-empty empty-hint">Select a person or a conversation to see its messages</div>
  }

  const label = conversationTitle(target, participants)
  const notice = unreachableNotice(target, participants)

  async function send(): Promise<void> {
    const text = draft.trim()
    if (text === '' || sending || target === null) return
    setSending(true)
    setError(null)
    try {
      let thread = openedThread ?? (target.kind === 'direct' ? findExistingDirectThread(threads, target.participant.id) : null)
      if (thread === null) {
        thread = await openMeshThread({
          requestId: openRequestIdRef.current,
          title: '',
          scope: 'run',
          memberIds: target.kind === 'direct' ? [target.participant.id] : [],
        })
        onThreadCreated?.(thread)
      }
      setOpenedThread(thread)
      await sendMeshMessage(thread.id, {
        requestId: requestIdRef.current,
        recipientIds: mentioned.length > 0 ? mentioned : recipientsFor(target, thread),
        body: text,
      })
      requestIdRef.current = genMeshRequestId('mesh-send')
      setDraft('')
      // Tell the parent only AFTER a successful send: onThreadOpened changes TeamView's `activeConversation`,
      // which changes this component's `key` and unmounts it (see targetKey's doc comment above) — firing it
      // before the send could fail would wipe the in-flight draft/error on an instance that no longer exists.
      // Guarded by notifiedRef, not a per-call local, so a retry whose EARLIER attempt already opened the
      // thread (and then failed to send) still notifies the parent once this attempt's send succeeds.
      if (!notifiedRef.current) {
        notifiedRef.current = true
        onThreadOpened?.(thread)
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'request failed')
    } finally {
      setSending(false)
    }
  }

  return (
    <div className="team-conversation">
      {/* One messages area in both states: the empty hint takes the same flex slot the list does, so the
          composer stays pinned to the bottom instead of jumping down after the first message. */}
      <div className="team-conversation-messages" role="log" aria-label="Messages">
        {messages.length === 0 ? (
          <p className="team-conversation-empty-messages empty-hint">No messages yet</p>
        ) : (
          <ul className="team-conversation-message-list">
            {messages.map((m) => (
              <MessageRow key={m.id} message={m} participants={participants} />
            ))}
          </ul>
        )}
      </div>
      {notice !== null && <p className="team-conversation-notice" role="status">{notice}</p>}
      <div className="team-conversation-route">
        <span>From you →</span>
        <strong>{mentioned.length > 0 ? mentioned.map((id) => senderLabel(id, participants)).join(', ') : routeAll(target, label)}</strong>
        <span className="team-conversation-route-hint">@ to address someone</span>
      </div>
      <div className="chat-composer-row">
        <textarea
          ref={setInputRef}
          className="chat-composer-input"
          placeholder={`Message ${label}…`}
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value)
            mention.onInput(e.target.value, e.target.selectionStart ?? e.target.value.length)
          }}
          onKeyDown={(e) => {
            if (mention.onKeyDown(e)) return
            if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
              e.preventDefault()
              void send()
            }
          }}
          rows={1}
        />
        {mention.open && (
          <MentionMenu
            groups={[{ label: '', items: mention.matches }]}
            highlightIndex={mention.highlightIndex}
            onHover={mention.setHighlightIndex}
            onSelect={mention.pick}
            ariaLabel="Mention someone in this conversation"
          />
        )}
        <ChatSendButton label="Send" disabled={draft.trim() === ''} sending={sending} onClick={() => void send()} />
      </div>
      {error !== null && <div className="team-conversation-error" role="alert">{error}</div>}
    </div>
  )
}

function messageTime(createdAt: string): string {
  const d = new Date(createdAt)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

// MessageRow — one message: avatar, author, "→ recipients", time, then the text bubble.
function MessageRow({ message, participants }: { message: MeshMessage; participants: MeshParticipant[] }): ReactElement {
  const author = senderLabel(message.sender_id, participants)
  const kind = participants.find((p) => p.id === message.sender_id)?.kind ?? (message.sender_id === 'user' ? 'user' : '')
  const recipients = message.recipient_ids.map((id) => senderLabel(id, participants)).join(', ')
  const fromYou = message.sender_id === 'user'
  return (
    <li className={`team-conversation-message${fromYou ? ' from-you' : ''}`} data-sender={message.sender_id}>
      <MeshAvatar name={author} kind={kind} />
      <div className="team-conversation-message-content">
        <div className="team-conversation-message-header">
          <span className="team-conversation-message-sender">{author}</span>
          {recipients !== '' && <span className="team-conversation-message-route">→ {recipients}</span>}
          <time dateTime={message.created_at}>{messageTime(message.created_at)}</time>
        </div>
        <div className="team-conversation-message-body">{message.body}</div>
      </div>
    </li>
  )
}
