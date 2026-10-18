import type { ReactElement } from 'react'
import type { MeshThread } from '../../api/mesh-client'

export type ThreadListProps = {
  threads: MeshThread[]
  selectedThreadId?: string | null
  onSelect: (thread: MeshThread) => void
}

// ThreadList renders the "Conversations" group of the Team roster (RunTeamList) — every thread the human
// dashboard operator belongs to (GET /api/mesh/threads), as opposed to the participant rows above it, which
// name people/agents rather than open conversations.
export function ThreadList({ threads, selectedThreadId = null, onSelect }: ThreadListProps): ReactElement {
  if (threads.length === 0) {
    return <p className="team-thread-list-empty empty-hint">No conversations yet</p>
  }
  return (
    <ul className="team-thread-list">
      {threads.map((t) => (
        <li key={t.id}>
          <button
            type="button"
            className={`team-thread-item${t.id === selectedThreadId ? ' active' : ''}`}
            onClick={() => onSelect(t)}
          >
            <span className="team-thread-title">{t.title && t.title !== '' ? t.title : t.id}</span>
            {t.status === 'closed' && <span className="team-thread-closed">Closed</span>}
          </button>
        </li>
      ))}
    </ul>
  )
}
