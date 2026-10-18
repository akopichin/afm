import type { ReactElement } from 'react'
import type { MeshParticipant, MeshThread } from '../../api/mesh-client'
import { threadLabel } from '../../lib/mesh-roster'

export type ThreadListProps = {
  threads: MeshThread[]
  // participants — чтобы назвать личный тред без названия по имени собеседника, а не по `mt-…` id.
  participants: MeshParticipant[]
  selectedThreadId?: string | null
  onSelect: (thread: MeshThread) => void
}

// ThreadList renders the "Conversations" group of the Team roster (RunTeamList) — every thread the human
// dashboard operator belongs to (GET /api/mesh/threads), as opposed to the participant rows above it, which
// name people/agents rather than open conversations.
export function ThreadList({ threads, participants, selectedThreadId = null, onSelect }: ThreadListProps): ReactElement {
  if (threads.length === 0) {
    return <p className="team-thread-list-empty empty-hint">No conversations yet</p>
  }
  return (
    <ul className="team-thread-list">
      {threads.map((t) => {
        const label = threadLabel(t, participants)
        return (
          <li key={t.id}>
            <button
              type="button"
              className={`team-thread-item${t.id === selectedThreadId ? ' active' : ''}`}
              onClick={() => onSelect(t)}
            >
              <span className="team-thread-title" title={label}>{label}</span>
              {t.status === 'closed' && <span className="team-thread-closed">Closed</span>}
            </button>
          </li>
        )
      })}
    </ul>
  )
}
