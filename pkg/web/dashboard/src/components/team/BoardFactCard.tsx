import type { ReactElement } from 'react'
import type { MeshBoardEntry, MeshParticipant } from '../../api/mesh-client'
import { boardReportsOf } from '../../lib/board-tasks'
import { senderLabel } from '../../lib/mesh-roster'

export type BoardFactCardProps = {
  fact: MeshBoardEntry
  // entries — the whole board: the reports under "<fact key>/<alias>" are found here and stay inside the card.
  entries: MeshBoardEntry[]
  participants: MeshParticipant[]
}

// BoardFactCard — an agent's fact on the board: key, version, status, author, artifacts and the reports on it.
// Shared by the Team Board panel and the run-wide Board tab.
export function BoardFactCard({ fact, entries, participants }: BoardFactCardProps): ReactElement {
  const reports = boardReportsOf(fact, entries)
  return (
    <article className="team-board-entry" aria-label={fact.key}>
      <div className="team-board-entry-header">
        <span className="team-board-entry-key">{fact.key}</span>
        <span className="team-board-entry-version">v{fact.version}</span>
      </div>
      <p className="team-board-entry-status">{fact.status}</p>
      <div className="team-board-entry-meta">
        <span className="team-board-entry-author">{senderLabel(fact.author_id, participants)}</span>
      </div>
      {fact.artifact_refs && fact.artifact_refs.length > 0 && (
        <ul className="team-board-entry-artifacts">
          {fact.artifact_refs.map((ref) => (
            <li key={ref}>{ref}</li>
          ))}
        </ul>
      )}
      {reports.length > 0 && (
        <ul className="team-board-reports" aria-label="Reports">
          {reports.map((r) => (
            <li key={r.key} className="team-board-report">
              <span className="team-board-report-author">{senderLabel(r.author_id, participants)}</span>
              <span className="team-board-report-status">{r.status}</span>
            </li>
          ))}
        </ul>
      )}
    </article>
  )
}
