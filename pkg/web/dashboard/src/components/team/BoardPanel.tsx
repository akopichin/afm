import { useEffect, useState, type ReactElement } from 'react'
import { getMeshBoard, type MeshBoardEntry } from '../../api/mesh-client'

const POLL_INTERVAL_MS = 5000

// BoardPanel — the Team view's "Board" tab: every key currently published on the shared board (GET
// /api/mesh/board with no key, orchestrator.ReadBoard's "empty Key returns every entry" contract), own poll,
// independent of /api/status (same posture as every other mesh/* hook in this feature).
export function BoardPanel(): ReactElement {
  const [entries, setEntries] = useState<MeshBoardEntry[]>([])

  useEffect(() => {
    let cancelled = false
    const load = (): void => {
      getMeshBoard()
        .then((res) => {
          if (!cancelled) setEntries(res.entries)
        })
        .catch(() => {
          /* transient — keep whatever we had, same posture as the other mesh polls */
        })
    }
    load()
    const timer = setInterval(load, POLL_INTERVAL_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [])

  if (entries.length === 0) {
    return <div className="team-board-empty empty-hint">Nothing published to the board yet</div>
  }

  return (
    <ul className="team-board-list">
      {entries.map((e) => (
        <li key={e.key} className="team-board-entry">
          <div className="team-board-entry-header">
            <span className="team-board-entry-key">{e.key}</span>
            <span className="team-board-entry-status">{e.status}</span>
          </div>
          <div className="team-board-entry-meta">
            <span className="team-board-entry-author">{e.author_id}</span>
            <span className="team-board-entry-version">v{e.version}</span>
          </div>
          {e.artifact_refs && e.artifact_refs.length > 0 && (
            <ul className="team-board-entry-artifacts">
              {e.artifact_refs.map((ref) => (
                <li key={ref}>{ref}</li>
              ))}
            </ul>
          )}
        </li>
      ))}
    </ul>
  )
}
