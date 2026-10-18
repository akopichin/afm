import { useRef, useState, type ReactElement } from 'react'
import { genMeshRequestId, openMeshThread, sendMeshMessage, type MeshParticipant, type MeshThread } from '../../api/mesh-client'
import { useMeshThread } from '../../hooks/use-mesh'

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

function targetLabel(target: ThreadConversationTarget): string {
  if (target.kind === 'direct') {
    const p = target.participant
    if (p.display_name && p.display_name !== '') return p.display_name
    if (p.alias && p.alias !== '') return p.alias
    return p.id
  }
  const title = target.thread.title
  return title && title !== '' ? title : target.thread.id
}

// recipientsFor — every OTHER member of the conversation: a direct target has exactly one (the person
// clicked); an opened thread's recipients are its full member list minus the human operator itself.
function recipientsFor(target: ThreadConversationTarget, opened: MeshThread | null): string[] {
  if (target.kind === 'direct') return [target.participant.id]
  const thread = opened ?? target.thread
  return thread.members.filter((m) => m !== 'user')
}

export function ThreadConversation({ target, onThreadOpened }: ThreadConversationProps): ReactElement {
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

  const threadId = openedThread?.id ?? null
  const messages = useMeshThread(threadId)

  if (target === null) {
    return <div className="team-conversation-empty empty-hint">Select a person or a conversation to see its messages</div>
  }

  const label = targetLabel(target)

  async function send(): Promise<void> {
    const text = draft.trim()
    if (text === '' || sending || target === null) return
    setSending(true)
    setError(null)
    try {
      let thread = openedThread
      if (thread === null) {
        thread = await openMeshThread({
          requestId: genMeshRequestId('mesh-open'),
          title: '',
          scope: 'run',
          memberIds: target.kind === 'direct' ? [target.participant.id] : [],
        })
        setOpenedThread(thread)
      }
      await sendMeshMessage(thread.id, {
        requestId: requestIdRef.current,
        recipientIds: recipientsFor(target, thread),
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
      <h3 className="team-conversation-title">{label}</h3>
      {messages.length === 0 ? (
        <p className="team-conversation-empty-messages empty-hint">No messages yet</p>
      ) : (
        <ul className="team-conversation-messages">
          {messages.map((m) => (
            <li key={m.id} className="team-conversation-message" data-sender={m.sender_id}>
              <span className="team-conversation-message-sender">{m.sender_id === 'user' ? 'You' : m.sender_id}</span>
              <span className="team-conversation-message-body">{m.body}</span>
            </li>
          ))}
        </ul>
      )}
      <div className="team-conversation-composer">
        <textarea
          className="team-conversation-input"
          placeholder={`Message ${label}…`}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
              e.preventDefault()
              void send()
            }
          }}
          rows={2}
        />
        <button type="button" className="btn btn-send" disabled={sending || draft.trim() === ''} onClick={() => void send()}>
          {sending ? 'Sending…' : 'Send'}
        </button>
      </div>
      {error !== null && <div className="team-conversation-error" role="alert">{error}</div>}
    </div>
  )
}
