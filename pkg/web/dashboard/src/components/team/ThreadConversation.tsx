import { useCallback, useEffect, useLayoutEffect, useRef, useState, type Dispatch, type ReactElement, type SetStateAction } from 'react'
import { genMeshRequestId, openMeshThread, sendMeshMessage, type MeshParticipant, type MeshSendReceipt, type MeshThread, type MeshThreadSummaries } from '../../api/mesh-client'
import { useMeshThread } from '../../hooks/use-mesh'
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'
import { useCaretInsert } from '../../hooks/use-caret-insert'
import { useStickToBottom } from '../../hooks/use-stick-to-bottom'
import { removeSubmittedText, useRetryableSubmit } from '../../hooks/use-retryable-submit'
import { MentionMenu } from '../assistants/AssistantMentionMenu'
import { draftMentions, filterMentionProfiles } from '../assistants/mention-match'
import { useMentionMenu } from '../assistants/use-mention-menu'
import { directPeer, findExistingDirectThread, mentionHandles, participantLabel, participantRole, senderLabel, threadLabel } from '../../lib/mesh-roster'
import { ConversationMessage } from './ConversationMessage'
import { ChatSendButton } from '../chat-composer/ChatSendButton'
import { sideAgentOutcome } from '../feed-workspace/mesh-receipt'

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
  // A direct thread can be learned from the roster or returned by the open request before its first send.
  // Let the parent remember it and discard the pending open request id in either case.
  onDirectThreadResolved?: (participantId: string, thread: MeshThread) => void
  getDirectOpenRequestId?: (participantId: string) => string
  // TeamView keeps unsettled send IDs above this keyed component, including the direct-to-thread remount.
  getSendRequestId?: (threadId: string, recipientIds: string[], body: string, proposedId: string) => string
  onSendSucceeded?: (target: ThreadConversationTarget, thread: MeshThread, submitted: string, requestId: string) => void
  // Report a committed send even if this conversation was unmounted while the request was in flight.
  onMessageSent?: (thread: MeshThread, receipt: MeshSendReceipt) => void
  // participants — the roster, to name an untitled thread and each message's sender instead of showing ids.
  participants?: MeshParticipant[]
  // threads — the latest known threads. A direct target re-checks them when SENDING, not only when the person
  // was clicked: the roster poll may learn about an existing direct thread after the click.
  threads?: MeshThread[]
  summaries?: MeshThreadSummaries
  // draft / onDraftChange — the composer's text, when the parent keeps it. TeamView does, so that text typed
  // while the first message of a direct conversation is sending survives the remount onThreadOpened causes.
  // Without them the component keeps its own draft.
  draft?: string
  onDraftChange?: Dispatch<SetStateAction<string>>
  sideFailures?: ReadonlySet<string>
  readEnabled?: boolean
  onMessagesRead?: (threadId: string, lastRenderedOtherSeq: number) => void
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
export function conversationSubtitle(target: ThreadConversationTarget, participants: MeshParticipant[]): string {
  if (target.kind === 'direct') return `Direct conversation · ${participantRole(target.participant.kind)}`
  const peer = directPeer(target.thread, participants)
  if (peer !== undefined) {
    const member = target.thread.title ? `${participantLabel(peer)} · ` : ''
    return `Direct conversation · ${member}${participantRole(peer.kind)}`
  }
  const agents = target.thread.members.filter((m) => m !== 'user').length
  const agentsText = `${agents} ${agents === 1 ? 'agent' : 'agents'}`
  // A conversation agents started between themselves does not include you.
  const who = target.thread.members.includes('user') ? `you and ${agentsText}` : agentsText
  const names = target.thread.title
    ? ` · ${target.thread.members.filter((id) => id !== 'user').map((id) => senderLabel(id, participants)).join(', ')}`
    : ''
  return `${target.thread.members.length} participants · ${who}${names}`
}

// otherMemberIds — every OTHER member of the conversation: a direct target has exactly one (the person
// clicked); an opened thread's are its full member list minus the human operator itself.
function otherMemberIds(target: ThreadConversationTarget, opened: MeshThread | null): string[] {
  if (target.kind === 'direct') return [target.participant.id]
  const thread = opened ?? target.thread
  return thread.members.filter((m) => m !== 'user')
}

// Reachability — the other members split into who still gets a message and who has finished (their stage or
// task is over). A member the roster does not know yet counts as reachable: it was only just added.
type Reachability = { reachable: string[]; finished: MeshParticipant[] }

function splitByReachability(ids: string[], participants: MeshParticipant[]): Reachability {
  const finished = ids.map((id) => participants.find((p) => p.id === id)).filter((p): p is MeshParticipant => p !== undefined && !p.reachable)
  return { reachable: ids.filter((id) => !finished.some((p) => p.id === id)), finished }
}

// unreachableNotice — when nobody else in the conversation can answer any more, say so above the composer
// (and keep Send off) instead of letting messages silently go unanswered.
function unreachableNotice({ reachable, finished }: Reachability): string | null {
  if (reachable.length > 0 || finished.length === 0) return null
  if (finished.length > 1) return 'Everyone in this conversation has finished and can\'t reply any more.'
  const only = finished[0] as MeshParticipant
  const why = only.unreachable_reason ?? ''
  return why === ''
    ? `${participantLabel(only)} has finished and can't reply any more.`
    : `${participantLabel(only)} can't reply any more: ${why}.`
}

// skippedNote — who the default route leaves out because they have finished, e.g. "QA has finished and won't
// get this message".
function skippedNote(finished: MeshParticipant[]): string {
  const names = finished.map(participantLabel).join(', ')
  return `${names} ${finished.length === 1 ? 'has' : 'have'} finished and won't get this message`
}

type MemberMention = { id: string; handle: string; title: string; unavailable: boolean }

// otherMembers — everyone in the conversation except the operator, as roster participants.
function otherMembers(target: ThreadConversationTarget, opened: MeshThread | null, participants: MeshParticipant[]): MeshParticipant[] {
  if (target.kind === 'direct') return [target.participant]
  return otherMemberIds(target, opened)
    .map((id) => participants.find((p) => p.id === id))
    .filter((p): p is MeshParticipant => p !== undefined)
}

// routeAll — the default recipient line: the person in a direct conversation, everyone still here in a thread.
function routeAll(target: ThreadConversationTarget, label: string, participants: MeshParticipant[]): string {
  if (target.kind === 'direct') return label
  const names = target.thread.members.filter((id) => id !== 'user').map((id) => senderLabel(id, participants))
  if (names.length === 1) return names[0]!
  return target.thread.title ? `Everyone in this conversation · ${names.join(', ')}` : 'Everyone in this conversation'
}

export function ThreadConversation(props: ThreadConversationProps): ReactElement {
  const { target, onThreadOpened, onDirectThreadResolved, getDirectOpenRequestId, getSendRequestId, onSendSucceeded, onMessageSent, participants = [], threads = [], summaries = {}, sideFailures = new Set<string>(), readEnabled = false, onMessagesRead } = props
  const [openedThread, setOpenedThread] = useState<MeshThread | null>(target?.kind === 'thread' ? target.thread : null)
  const [ownDraft, setOwnDraft] = useState('')
  const draft = props.draft ?? ownDraft
  const setDraft = props.onDraftChange ?? setOwnDraft
  // submitter — sends with a request id that a retry of the SAME message reuses; this component remounts per
  // conversation (see targetKey), so a late response is fenced out by unmount alone.
  const submitter = useRetryableSubmit<{ recipientIds: string[]; body: string }>('mesh-send')
  // log — opens at the latest message and follows new ones, unless the reader scrolled up.
  const log = useStickToBottom<HTMLDivElement>()
  // notifiedRef — whether the parent has already been told about openedThread. A 'thread' target starts
  // already known to the parent (it IS activeConversation), so it starts true. A 'direct' target starts
  // false: once its first message opens a real thread, the parent must hear about it exactly once — even if
  // that happens on a RETRY after the open succeeded but an earlier send attempt failed (openedThread is set
  // from the failed attempt already, so a plain "did this call just open it" check misses the retry).
  const notifiedRef = useRef<boolean>(target?.kind === 'thread')

  // Standalone callers keep an id here. TeamView owns the id by participant so it survives a keyed remount.
  const openRequestIdRef = useRef<string | null>(null)

  const autoGrowRef = useAutoGrowTextarea(draft, MAX_INPUT_HEIGHT)
  const { nodeRef: caretNodeRef, replaceRange } = useCaretInsert(draft, setDraft)
  const setInputRef = useCallback(
    (node: HTMLTextAreaElement | null) => {
      caretNodeRef.current = node
      autoGrowRef(node)
    },
    [autoGrowRef, caretNodeRef],
  )
  // Mention candidates — the other members of this conversation, addressed by a short @handle that is unique
  // among them (two run agents sharing an alias get their ids instead), so a mention means exactly one person.
  const members = target === null ? [] : otherMembers(target, openedThread, participants)
  const handles = mentionHandles(members)
  const handleOf = (p: MeshParticipant): string => handles.get(p.id) ?? p.id
  const mention = useMentionMenu<MemberMention>({
    matchesFor: (query) =>
      filterMentionProfiles(
        members.map((p) => ({ id: p.id, handle: handleOf(p), title: participantLabel(p), unavailable: !p.reachable })),
        query,
      ),
    textFor: (item) => item.handle,
    replaceRange,
  })
  // mentioned — recipients addressed with `@` in the draft right now; none means everyone still reachable.
  // Derived from the text, so deleting a mention from the draft also un-addresses that member.
  const mentioned = members.filter((p) => draftMentions(draft, handleOf(p))).map((p) => p.id)
  const threadId = openedThread?.id ?? null
  const messages = useMeshThread(threadId)
  const closed = openedThread?.status === 'closed' || (target?.kind === 'thread' && target.thread.status === 'closed')
  const closedNoticeRef = useRef<HTMLParagraphElement>(null)
  const composerWasFocusedRef = useRef(false)

  useLayoutEffect(() => {
    if (closed && composerWasFocusedRef.current) closedNoticeRef.current?.focus()
    if (!closed) composerWasFocusedRef.current = false
  }, [closed])

  useEffect(() => {
    if (!readEnabled || threadId === null || onMessagesRead === undefined || messages.length === 0) return
    // useMeshThread commits only complete pages. This effect runs after the log has rendered those messages.
    const lastOtherSeq = messages.reduce((seq, message) => message.sender_id === 'user' ? seq : Math.max(seq, message.seq), 0)
    if (lastOtherSeq === 0) return
    const markVisible = () => {
      if (!document.hidden && log.stick) onMessagesRead(threadId, lastOtherSeq)
    }
    markVisible()
    document.addEventListener('visibilitychange', markVisible)
    return () => document.removeEventListener('visibilitychange', markVisible)
  }, [readEnabled, threadId, messages, log.stick, onMessagesRead])

  if (target === null) {
    return <div className="team-conversation-empty empty-hint">Select a person or a conversation to see its messages</div>
  }

  const label = conversationTitle(target, participants)
  const reachability = splitByReachability(otherMemberIds(target, openedThread), participants)
  const notice = unreachableNotice(reachability)
  const recipientIds = mentioned.length > 0 ? mentioned : reachability.reachable
  const canSend = draft.trim() !== '' && recipientIds.length > 0 && !closed
  // readOnly — a conversation agents started between themselves: you can read along, but only members may
  // write into it (the server refuses anyone else).
  const readOnly = target.kind === 'thread' && !target.thread.members.includes('user')

  // ensureThread — the conversation's thread, opened on the first send of a direct conversation.
  async function ensureThread(current: ThreadConversationTarget): Promise<MeshThread> {
    const known = openedThread ?? (current.kind === 'direct' ? findExistingDirectThread(threads, current.participant.id, summaries) : null)
    if (known !== null) {
      if (current.kind === 'direct') onDirectThreadResolved?.(current.participant.id, known)
      return known
    }
    const thread = await openMeshThread({
      requestId: current.kind === 'direct'
        ? getDirectOpenRequestId?.(current.participant.id) ?? (openRequestIdRef.current ??= genMeshRequestId('mesh-open'))
        : (openRequestIdRef.current ??= genMeshRequestId('mesh-open')),
      title: '',
      scope: 'run',
      memberIds: current.kind === 'direct' ? [current.participant.id] : [],
    })
    if (current.kind === 'direct') onDirectThreadResolved?.(current.participant.id, thread)
    return thread
  }

  function send(): void {
    if (!canSend || target === null) return
    const submitted = draft
    const current = target
    submitter.submit(
      { recipientIds, body: submitted.trim() },
      async (p, requestId) => {
        const thread = await ensureThread(current)
        setOpenedThread(thread)
        const sendRequestId = getSendRequestId?.(thread.id, p.recipientIds, p.body, requestId) ?? requestId
        const receipt = await sendMeshMessage(thread.id, { requestId: sendRequestId, recipientIds: [...p.recipientIds], body: p.body })
        // Here rather than in onSuccess: a draft kept by the parent outlives this conversation view, and a
        // message that was sent must not wait there to be sent again after the user switched away and back.
        if (onSendSucceeded) onSendSucceeded(current, thread, submitted, sendRequestId)
        else setDraft((cur) => removeSubmittedText(cur, submitted))
        onMessageSent?.(thread, receipt)
        return thread
      },
      {
        onSuccess: (thread) => {
          // Tell the parent only AFTER a successful send: onThreadOpened changes TeamView's `activeConversation`,
          // which changes this component's `key` and unmounts it (see targetKey's doc comment above) — firing it
          // before the send could fail would wipe the in-flight draft/error on an instance that no longer exists.
          // Guarded by notifiedRef, not a per-call local, so a retry whose EARLIER attempt already opened the
          // thread (and then failed to send) still notifies the parent once this attempt's send succeeds.
          if (!notifiedRef.current) {
            notifiedRef.current = true
            onThreadOpened?.(thread)
          }
        },
      },
    )
  }

  return (
    <div className="team-conversation">
      {/* One messages area in both states: the empty hint takes the same flex slot the list does, so the
          composer stays pinned to the bottom instead of jumping down after the first message. */}
      <div className="team-conversation-messages" role="log" aria-label="Messages" ref={log.ref}>
        {messages.length === 0 ? (
          <p className="team-conversation-empty-messages empty-hint">No messages yet</p>
        ) : (
          <ul className="team-conversation-message-list">
            {messages.map((m) => (
              <ConversationMessage key={m.id} message={m} participants={participants} answered={messages.find((x) => x.id === m.in_reply_to)} sideOutcome={sideAgentOutcome(m, messages, sideFailures)} />
            ))}
          </ul>
        )}
      </div>
      {readOnly ? (
        <p className="team-conversation-notice readonly" role="status">
          The agents are talking between themselves — you can read along. To weigh in, message them directly or
          start a conversation with them.
        </p>
      ) : (
        <>
        {closed ? <p ref={closedNoticeRef} className="team-conversation-notice" role="status" tabIndex={-1}>This conversation is closed. Messages are read-only.</p>
          : notice !== null && <p className="team-conversation-notice" role="status">{notice}</p>}
        <div className="team-conversation-route">
          <span>From you →</span>
          <strong>{mentioned.length > 0 ? mentioned.map((id) => senderLabel(id, participants)).join(', ') : routeAll(target, label, participants)}</strong>
          {mentioned.length === 0 && notice === null && reachability.finished.length > 0 && (
            <span className="team-conversation-route-skipped">{skippedNote(reachability.finished)}</span>
          )}
          <span className="team-conversation-route-hint">@ to address someone</span>
        </div>
        <div className="chat-composer-row">
          <textarea
            {...mention.inputAria}
            ref={setInputRef}
            className="chat-composer-input"
            placeholder={`Message ${label}…`}
            disabled={closed}
            value={draft}
            onFocus={() => { composerWasFocusedRef.current = true }}
            onBlur={(event) => { if (!event.currentTarget.disabled) composerWasFocusedRef.current = false }}
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
              ariaLabel="Mention someone in this conversation"
            />
          )}
          <ChatSendButton label="Send" disabled={!canSend} sending={submitter.pending} onClick={send} />
        </div>
        {submitter.error !== null && <div className="team-conversation-error" role="alert">{submitter.error}</div>}
        </>
      )}
    </div>
  )
}
