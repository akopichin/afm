import { useEffect, useState, type ReactElement } from 'react'
import type { MeshParticipant, MeshThread, MeshThreadSummaries } from '../../api/mesh-client'
import { directPeer, participantLabel, participantRole, senderLabel, threadLabel, threadMembersLine } from '../../lib/mesh-roster'
import { MeshAvatar, ThreadSymbol } from './MeshAvatar'

export type ThreadListProps = {
  threads: MeshThread[]
  // participants — чтобы назвать личный тред без названия по имени собеседника, а не по `mt-…` id.
  participants: MeshParticipant[]
  summaries?: MeshThreadSummaries
  unreadThreadIds?: ReadonlySet<string>
  selectedThreadId?: string | null
  onSelect: (thread: MeshThread) => void
}

// ThreadList renders the "Conversations" group of the Team roster (RunTeamList) — every thread the human
// dashboard operator belongs to (GET /api/mesh/threads), as opposed to the participant rows above it, which
// name people/agents rather than open conversations.
function relativeTime(createdAt: string, now: number): string {
  const elapsed = Math.max(0, Math.floor((now - new Date(createdAt).getTime()) / 1000))
  if (elapsed < 60) return 'now'
  if (elapsed < 3600) return `${Math.floor(elapsed / 60)}m`
  if (elapsed < 86400) return `${Math.floor(elapsed / 3600)}h`
  return `${Math.floor(elapsed / 86400)}d`
}

export function ThreadList({ threads, participants, summaries = {}, unreadThreadIds, selectedThreadId = null, onSelect }: ThreadListProps): ReactElement {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 60_000)
    return () => window.clearInterval(timer)
  }, [])
  if (threads.length === 0) {
    return <p className="team-thread-list-empty empty-hint">No conversations yet</p>
  }
  return (
    <ul className="team-thread-list">
      {threads.map((t) => {
        const label = threadLabel(t, participants)
        const peer = directPeer(t, participants)
        const last = summaries[t.id]?.last_message
        const date = last === undefined ? null : new Date(last.created_at)
        const validDate = date !== null && !Number.isNaN(date.getTime())
        const preview = last === undefined ? 'No messages yet' : `${senderLabel(last.sender_id, participants)}: ${last.preview}`
        const detail = peer === undefined ? threadMembersLine(t, participants) :
          t.title ? `${participantLabel(peer)} · ${participantRole(peer.kind)}` : participantRole(peer.kind)
        const unread = unreadThreadIds?.has(t.id) ?? false
        return (
          <li key={t.id}>
            <button
              type="button"
              className={`team-thread-item${t.id === selectedThreadId ? ' active' : ''}${unread ? ' unread' : ''}`}
              aria-pressed={t.id === selectedThreadId}
              onClick={() => onSelect(t)}
            >
              {peer !== undefined ? <MeshAvatar name={participantLabel(peer)} kind={peer.kind} size="small" /> : <ThreadSymbol size="small" />}
              <span className="team-person-text">
                <span className="team-thread-topline">
                  <span className="team-thread-title" title={label}>{label}</span>
                  {validDate && <time className="team-thread-time" dateTime={last!.created_at} title={date.toLocaleString()} aria-label={date.toISOString()}>{relativeTime(last!.created_at, now)}</time>}
                </span>
                <span className="team-person-detail" title={detail}>{detail}</span>
                <span className="team-thread-preview" title={preview}>{preview}</span>
              </span>
              {t.status === 'closed' && <span className="team-thread-closed">Closed</span>}
              {unread && <span className="team-thread-unread"><span className="team-thread-unread-label">New messages</span></span>}
            </button>
          </li>
        )
      })}
    </ul>
  )
}
