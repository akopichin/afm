import { handleTextareaSubmit } from '../../lib/textarea-submit'
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type Dispatch, type ReactElement, type SetStateAction } from 'react'
import { genMeshRequestId, openMeshThread, sendMeshMessage, type MeshParticipant, type MeshSendReceipt, type MeshThread, type MeshThreadSummaries } from '../../api/mesh-client'
import { useMeshThread } from '../../hooks/use-mesh'
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'
import { useCaretInsert } from '../../hooks/use-caret-insert'
import { useStickToBottom } from '../../hooks/use-stick-to-bottom'
import { removeSubmittedText, useRetryableSubmit } from '../../hooks/use-retryable-submit'
import { MentionMenu } from '../assistants/AssistantMentionMenu'
import { draftMentions, filterMentionProfiles } from '../assistants/mention-match'
import { useMentionMenu } from '../assistants/use-mention-menu'
import { canReplyInConversation, isAgentMessageToUser, latestUnansweredRequest } from '../../lib/mesh-reply'
import { messageText } from '../../lib/agent-result'
import { directPeer, findExistingDirectThread, mentionHandles, participantHandle, participantLabel, participantMentionNote, participantRole, senderLabel, threadHandlesLine, threadLabel } from '../../lib/mesh-roster'
import { ConversationMessage } from './ConversationMessage'
import { ChatSendButton } from '../chat-composer/ChatSendButton'
import { ComposerCard, ComposerHint, ComposerRecipients } from '../chat-composer/ComposerCard'
import { sideAgentOutcome } from '../feed-workspace/mesh-receipt'

// ThreadConversationTarget — what the Messages pane is currently pointed at: an ALREADY open thread (picked
// from the roster's "Conversations" group), or a person row with no thread yet ("direct" — opening one is
// deferred to the first message actually sent, so merely browsing the roster never creates empty threads).
export type ThreadConversationTarget =
  | { kind: 'thread'; thread: MeshThread }
  | { kind: 'direct'; participant: MeshParticipant }

// RunConversationMode — what the Run conversations panel adds to a conversation: seen tracking, and ownership of
// the reply target (as it owns the draft), so both outlive a trip Back to the list. Without it (Team) the
// conversation keeps its own reply target: it is remounted per conversation anyway.
export type RunConversationMode = {
  // seenSeq — the stored cursor: agent messages to the human up to it have been seen.
  seenSeq: number
  onSeen: (threadId: string, seq: number) => void
  // tracking — the panel is open and this history is on screen; only then do messages count as seen or animate.
  tracking: boolean
  // replyTargetId — a message id (answers it), null (the human cancelled it) or undefined (not chosen yet: the
  // newest unanswered request is preselected while the draft is empty).
  replyTargetId: string | null | undefined
  onReplyTargetChange: (id: string | null | undefined) => void
}

export type ConversationSession = { replyTargetId?: string | null; scrollTop?: number; stick?: boolean }

export type ThreadConversationProps = {
  session?: ConversationSession
  onSessionChange?: (patch: Partial<ConversationSession>) => void
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
  run?: RunConversationMode
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
// ACCENT_MS — how long a just-arrived agent message stays bright. A fixed timer: under reduced motion no
// animation runs, so `animationend` would never clear it.
const ACCENT_MS = 2400
// Many thresholds, so the observer reports every step of a bubble scrolling in and the half-visible rule is
// checked often enough.
const VISIBILITY_THRESHOLDS = Array.from({ length: 21 }, (_, i) => i / 20)
const NO_IDS: ReadonlySet<string> = new Set()

// conversationTitle — the conversation's name: the person for a direct target, else the thread's title or,
// for an untitled thread, its other members' names.
export function conversationTitle(target: ThreadConversationTarget, participants: MeshParticipant[]): string {
  return target.kind === 'direct' ? participantLabel(target.participant) : threadLabel(target.thread, participants)
}

// conversationSubtitle — the line under the title: the people's handles and their roles ("@a11y, @impl · Helper / Stage executor").
export function conversationSubtitle(target: ThreadConversationTarget, participants: MeshParticipant[]): string {
  if (target.kind === 'direct') return `${participantHandle(target.participant)} · ${participantRole(target.participant.kind)}`
  const peer = directPeer(target.thread, participants)
  if (peer !== undefined) return `${target.thread.title ? participantLabel(peer) : participantHandle(peer)} · ${participantRole(peer.kind)}`
  const handles = threadHandlesLine(target.thread, participants)
  const known = target.thread.members.map((id) => participants.find((p) => p.id === id)).filter((p): p is MeshParticipant => p !== undefined && p.kind !== 'user')
  const roles = [...new Set(known.map((p) => participantRole(p.kind)))].join(' / ')
  return roles === '' ? handles : `${handles} · ${roles}`
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

type MemberMention = { id: string; handle: string; title: string; kind: string; description: string; unavailable: boolean }

// otherMembers — everyone in the conversation except the operator, as roster participants.
function otherMembers(target: ThreadConversationTarget, opened: MeshThread | null, participants: MeshParticipant[]): MeshParticipant[] {
  if (target.kind === 'direct') return [target.participant]
  return otherMemberIds(target, opened)
    .map((id) => participants.find((p) => p.id === id))
    .filter((p): p is MeshParticipant => p !== undefined)
}

export function ThreadConversation(props: ThreadConversationProps): ReactElement {
  const { target, onThreadOpened, onDirectThreadResolved, getDirectOpenRequestId, getSendRequestId, onSendSucceeded, onMessageSent, participants = [], threads = [], summaries = {}, sideFailures = new Set<string>(), readEnabled = false, onMessagesRead, run } = props
  const [openedThread, setOpenedThread] = useState<MeshThread | null>(target?.kind === 'thread' ? target.thread : null)
  const [ownDraft, setOwnDraft] = useState('')
  const draft = props.draft ?? ownDraft
  const setDraft = props.onDraftChange ?? setOwnDraft
  // submitter — sends with a request id that a retry of the SAME message reuses; this component remounts per
  // conversation (see targetKey), so a late response is fenced out by unmount alone.
  const submitter = useRetryableSubmit<{ recipientIds: string[]; body: string; inReplyTo?: string }>('mesh-send')
  // log — opens at the latest message and follows new ones, unless the reader scrolled up.
  const log = useStickToBottom<HTMLDivElement>()
  // logNodeRef — the log element itself: the root the seen-tracking observer watches bubbles in.
  const logNodeRef = useRef<HTMLDivElement | null>(null)
  const setLogNode = useCallback((node: HTMLDivElement | null) => {
    logNodeRef.current = node
    log.ref(node)
  }, [log.ref])
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
        members.map((p) => ({ id: p.id, handle: handleOf(p), title: participantLabel(p), kind: p.kind, description: participantMentionNote(p), unavailable: !p.reachable })),
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
  // The roster's current record of the opened thread wins over the snapshot taken when it was opened, so a thread
  // closed meanwhile (e.g. while Team was hidden) stops accepting messages.
  const liveThread = openedThread === null ? null : threads.find((t) => t.id === openedThread.id) ?? openedThread
  const closed = liveThread?.status === 'closed' || (target?.kind === 'thread' && target.thread.status === 'closed')
  const closedNoticeRef = useRef<HTMLParagraphElement>(null)
  const composerWasFocusedRef = useRef(false)

  useLayoutEffect(() => {
    if (closed && composerWasFocusedRef.current) closedNoticeRef.current?.focus()
    if (!closed) composerWasFocusedRef.current = false
  }, [closed])

  // ---- Linked replies; seen tracking only in Run conversations mode ----
  const incoming = useMemo(() => messages.filter(isAgentMessageToUser), [messages])
  const unseen = useMemo(() => (run === undefined ? [] : incoming.filter((m) => m.seq > run.seenSeq)), [incoming, run?.seenSeq])
  const unseenKey = unseen.map((m) => m.id).join(',')
  const tracking = run?.tracking === true
  const onSeenRef = useRef(run?.onSeen)
  onSeenRef.current = run?.onSeen

  // justAnswered — requests answered a moment ago that the polled list does not show yet: without it the answered
  // question would be preselected again right after the send.
  const [justAnswered, setJustAnswered] = useState<ReadonlySet<string>>(NO_IDS)
  const savedScroll = useRef(props.session)
  const restoredScroll = useRef(false)
  useLayoutEffect(() => {
    if (restoredScroll.current || savedScroll.current?.stick !== false) return
    log.release()
    if (messages.length === 0 || logNodeRef.current === null) return
    logNodeRef.current.scrollTop = savedScroll.current.scrollTop ?? 0
    restoredScroll.current = true
  }, [messages, log.release])
  const replyThread = liveThread ?? undefined
  const preselected = latestUnansweredRequest(messages, replyThread, participants, justAnswered)
  const draftEmpty = draft.trim() === ''
  const [ownReplyTargetId, setOwnReplyTargetId] = useState<string | null | undefined>(undefined)
  const replyTargetId = run !== undefined ? run.replyTargetId : props.session !== undefined ? props.session.replyTargetId : ownReplyTargetId
  const onReplyTargetChange = run !== undefined ? run.onReplyTargetChange : props.onSessionChange !== undefined ? (replyTargetId: string | null | undefined) => props.onSessionChange?.({ replyTargetId }) : setOwnReplyTargetId
  // The live choice, for a send that resolves later: by then the human may have pointed at another question.
  const replyTargetRef = useRef(replyTargetId)
  replyTargetRef.current = replyTargetId
  // The newest unanswered request is the default target while nothing is typed; from then on it is held, so a
  // newer request never silently takes over an answer in progress.
  useEffect(() => {
    if (preselected !== undefined && replyTargetId === undefined && draftEmpty) onReplyTargetChange(preselected.id)
  }, [preselected?.id, replyTargetId, draftEmpty, onReplyTargetChange])
  useEffect(() => {
    if (closed && replyTargetId !== undefined) onReplyTargetChange(undefined)
  }, [closed, replyTargetId, onReplyTargetChange])
  const replyTo = typeof replyTargetId === 'string' ? messages.find((m) => m.id === replyTargetId && canReplyInConversation(m, replyThread, participants)) ?? null : null

  // Seen: a message counts once at least half of the smaller of its own and the log's height is on screen, while
  // the tab is visible. The cursor moves only through a run of seen messages, so a visible later one never marks
  // an unseen earlier one. The first time a bubble counts it also turns bright for a moment (`accented`, state of
  // its own, so the cursor moving on in that same render cannot take the accent away; one timer ends it).
  const seenIdsRef = useRef(new Set<string>())
  const [accented, setAccented] = useState<ReadonlySet<string>>(NO_IDS)
  useEffect(() => {
    const root = logNodeRef.current
    if (!tracking || root === null || threadId === null || unseen.length === 0 || typeof IntersectionObserver === 'undefined') return
    const ids = new Set(unseen.map((m) => m.id))
    const report = () => {
      let upTo = 0
      for (const m of unseen) {
        if (!seenIdsRef.current.has(m.id)) break
        upTo = m.seq
      }
      if (upTo > 0) onSeenRef.current?.(threadId, upTo)
    }
    const observer = new IntersectionObserver((entries) => {
      // A layout change in a hidden tab (a new message scrolling the log) is not the human looking.
      if (document.hidden) return
      const newlySeen: string[] = []
      for (const entry of entries) {
        const id = (entry.target as HTMLElement).dataset.messageId
        if (id === undefined || seenIdsRef.current.has(id)) continue
        const needed = Math.min(entry.boundingClientRect.height, entry.rootBounds?.height ?? Infinity) / 2
        if (entry.isIntersecting && entry.intersectionRect.height >= needed) {
          seenIdsRef.current.add(id)
          newlySeen.push(id)
        }
      }
      if (newlySeen.length > 0) setAccented((prev) => new Set([...prev, ...newlySeen]))
      report()
    }, { root, threshold: VISIBILITY_THRESHOLDS })
    const bubbles = [...root.querySelectorAll<HTMLElement>('[data-message-id]')].filter((el) => ids.has(el.dataset.messageId ?? ''))
    bubbles.forEach((el) => observer.observe(el))
    // Whatever the tab showed while hidden was ignored: on return look at every bubble again (observing anew
    // makes the observer report each one's current state).
    const lookAgain = () => {
      if (document.hidden) return
      bubbles.forEach((el) => { observer.unobserve(el); observer.observe(el) })
    }
    document.addEventListener('visibilitychange', lookAgain)
    return () => {
      observer.disconnect()
      document.removeEventListener('visibilitychange', lookAgain)
    }
  }, [tracking, threadId, unseenKey])
  useEffect(() => {
    if (accented.size === 0) return
    const timer = window.setTimeout(() => setAccented(NO_IDS), ACCENT_MS)
    return () => window.clearTimeout(timer)
  }, [accented])

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
  const addressed = mentioned.length > 0 ? mentioned : reachability.reachable
  // A reply always reaches the author of the question, whoever else is @mentioned.
  const recipientIds = replyTo !== null && !addressed.includes(replyTo.sender_id) ? [replyTo.sender_id, ...addressed] : addressed
  const canSend = draft.trim() !== '' && recipientIds.length > 0 && !closed
  // The chips name the recipients the way they are addressed: by their unique @handle, else by name.
  const recipientChips = recipientIds.map((id) => {
    const person = participants.find((p) => p.id === id)
    if (person === undefined) return { id, text: senderLabel(id, participants) }
    return { id, text: person.alias !== undefined && person.alias !== '' ? `@${handleOf(person)}` : participantLabel(person) }
  })
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
    const targetAtSend = replyTargetId
    submitter.submit(
      { recipientIds, body: submitted.trim(), ...(replyTo === null ? {} : { inReplyTo: replyTo.id }) },
      async (p, requestId) => {
        const thread = await ensureThread(current)
        setOpenedThread(thread)
        const sendRequestId = getSendRequestId?.(thread.id, p.recipientIds, p.body, requestId) ?? requestId
        const receipt = await sendMeshMessage(thread.id, {
          requestId: sendRequestId,
          recipientIds: [...p.recipientIds],
          body: p.body,
          ...(p.inReplyTo === undefined ? {} : { kind: 'reply', inReplyTo: p.inReplyTo }),
        })
        const answeredId = p.inReplyTo
        if (answeredId !== undefined) setJustAnswered((prev) => new Set(prev).add(answeredId))
        // Any sent message ends the choice it was sent with (also a cancelled one): the next question is
        // preselected afresh. A choice made while this was in flight is the human's next one: keep it.
        if (replyTargetRef.current === targetAtSend) onReplyTargetChange(undefined)
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

  // openPicker — "+ Add recipient" and "@" open the same member list at the caret, as typing "@" does.
  function openPicker(): void {
    const el = caretNodeRef.current
    const caret = el?.selectionStart ?? draft.length
    const before = draft.slice(0, caret)
    const insert = before !== '' && !/\s$/.test(before) ? ' @' : '@'
    replaceRange(caret, caret, insert)
    mention.onInput(before + insert + draft.slice(caret), caret + insert.length)
    el?.focus()
  }

  function toggleReplyTarget(id: string): void {
    onReplyTargetChange(replyTo?.id === id ? null : id)
    if (replyTo?.id !== id) caretNodeRef.current?.focus()
  }

  return (
    <div className="team-conversation">
      {/* One messages area in both states: the empty hint takes the same flex slot the list does, so the
          composer stays pinned to the bottom instead of jumping down after the first message. */}
      <div className="team-conversation-messages" role="log" aria-label="Messages" ref={setLogNode} onScroll={(event) => {
        const node = event.currentTarget
        if (node.clientHeight === 0) return
        props.onSessionChange?.({ scrollTop: node.scrollTop, stick: node.scrollHeight - node.scrollTop - node.clientHeight < 40 })
      }}>
        {messages.length === 0 ? (
          <p className="team-conversation-empty-messages empty-hint">No messages yet</p>
        ) : (
          <ul className="team-conversation-message-list">
            {messages.map((m, index) => (
              <ConversationMessage
                key={m.id}
                message={m}
                previousCreatedAt={messages[index - 1]?.created_at}
                participants={participants}
                answered={messages.find((x) => x.id === m.in_reply_to)}
                sideOutcome={sideAgentOutcome(m, messages, sideFailures)}
                isNew={accented.has(m.id)}
                replyActive={replyTo?.id === m.id}
                onReply={canReplyInConversation(m, replyThread, participants) ? () => toggleReplyTarget(m.id) : undefined}
              />
            ))}
          </ul>
        )}
      </div>
      {tracking && unseen.length > 0 && !log.stick && (
        <button type="button" className="team-conversation-new-below" onClick={log.jumpToBottom}>New message below</button>
      )}
      {readOnly ? (
        <p className="team-conversation-notice readonly" role="status">
          The agents are talking between themselves — you can read along. To weigh in, message them directly or
          start a conversation with them.
        </p>
      ) : (
        <>
        {closed ? <p ref={closedNoticeRef} className="team-conversation-notice" role="status" tabIndex={-1}>This conversation is closed. Messages are read-only.</p>
          : notice !== null && <p className="team-conversation-notice" role="status">{notice}</p>}
        {replyTo !== null && (
          <div className="feed-reply-chip feed-reply-chip--conversation">
            <span className="feed-reply-chip-label">↩ Reply in conversation</span>
            <span className="feed-reply-chip-src">{messageText(replyTo.body, true).slice(0, 120)}</span>
            <button type="button" className="feed-reply-chip-cancel" aria-label="Cancel reply in conversation" onClick={() => onReplyTargetChange(null)}>
              ✕
            </button>
          </div>
        )}
        <div className="team-composer-head">
          <strong>Message {recipientChips.map((chip) => chip.text).join(', ')}</strong>
          {mentioned.length === 0 && notice === null && reachability.finished.length > 0 && (
            <span className="team-conversation-route-skipped">{skippedNote(reachability.finished)}</span>
          )}
        </div>
        <ComposerCard>
          <ComposerRecipients>
            {recipientChips.map((chip) => (
              <span key={chip.id} className="mesh-recipient-chip chat-composer-recipient">{chip.text}</span>
            ))}
            <button type="button" className="mesh-recipients-add chat-composer-add" aria-label="Add recipient" title="Add recipient (@)" disabled={closed} onClick={openPicker}>
              <span aria-hidden="true">+</span> Add recipient
            </button>
          </ComposerRecipients>
          <div className="pasteable-textarea-wrap">
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
                handleTextareaSubmit(e, send, canSend && !submitter.pending)
              }}
              rows={1}
            />
            <div className="pasteable-toolbar">
              <button type="button" className="chat-composer-mention" aria-label="Mention someone" title="Mention someone (@)" disabled={closed} onClick={openPicker}>@</button>
              <ChatSendButton label="Send" visibleLabel="Send" disabled={!canSend} sending={submitter.pending} onClick={send} />
            </div>
          </div>
          {mention.open && (
            <MentionMenu
              id={mention.listboxId}
              groups={[{ label: '', items: mention.matches }]}
              highlightIndex={mention.highlightIndex}
              onHover={mention.setHighlightIndex}
              onSelect={mention.pick}
              ariaLabel="Mention someone in this conversation"
              heading="Mention a participant"
            />
          )}
        </ComposerCard>
        <ComposerHint note="Only the recipients above receive this message." />
        {submitter.error !== null && <div className="team-conversation-error" role="alert">{submitter.error}</div>}
        </>
      )}
    </div>
  )
}
