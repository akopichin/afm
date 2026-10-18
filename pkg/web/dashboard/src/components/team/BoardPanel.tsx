import { useEffect, useRef, useState, type ReactElement } from 'react'
import { genMeshRequestId, getMeshBoard, publishMeshBoard, type MeshBoardEntry, type MeshParticipant } from '../../api/mesh-client'
import { senderLabel } from '../../lib/mesh-roster'

const POLL_INTERVAL_MS = 5000
const TASK_PREFIX = 'task/'

export type BoardPanelProps = {
  // participants — to show who published an entry by name.
  participants: MeshParticipant[]
}

// taskKey — "task/<slug>" for a task title ("CSV export" → "task/csv-export").
function taskKey(title: string): string {
  const slug = title
    .toLowerCase()
    .trim()
    .replace(/[^\p{L}\p{N}]+/gu, '-')
    .replace(/^-+|-+$/g, '')
  return TASK_PREFIX + (slug !== '' ? slug : 'untitled')
}

// isReport — an agent's report on a task is published under "<task key>/<alias>" (agents cannot update a key
// someone else published), and is shown under that task rather than as an entry of its own.
function reportOf(entry: MeshBoardEntry, entries: MeshBoardEntry[]): MeshBoardEntry | undefined {
  return entries.find((t) => t.key !== entry.key && entry.key.startsWith(t.key + '/'))
}

// BoardPanel — the Team view's "Board" tab: the shared board (GET /api/mesh/board, own poll). The operator
// posts tasks for the agents here; agents publish facts and report on tasks under "<task key>/<alias>".
export function BoardPanel({ participants }: BoardPanelProps): ReactElement {
  const [entries, setEntries] = useState<MeshBoardEntry[]>([])
  const [title, setTitle] = useState('')
  const [details, setDetails] = useState('')
  const [posting, setPosting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // requestIdRef — one id per task being posted, reused by retries; a new task gets a new id.
  const requestIdRef = useRef(genMeshRequestId('mesh-board'))

  const load = (): Promise<void> =>
    getMeshBoard()
      .then((res) => setEntries(res.entries))
      .catch(() => {
        /* transient — keep whatever we had, same posture as the other mesh polls */
      })

  useEffect(() => {
    let cancelled = false
    const tick = (): void => {
      getMeshBoard()
        .then((res) => {
          if (!cancelled) setEntries(res.entries)
        })
        .catch(() => {})
    }
    tick()
    const timer = setInterval(tick, POLL_INTERVAL_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [])

  function post(): void {
    const name = title.trim()
    if (name === '' || posting) return
    setPosting(true)
    setError(null)
    const text = details.trim()
    publishMeshBoard({ requestId: requestIdRef.current, key: taskKey(name), status: text !== '' ? `${name} — ${text}` : name })
      .then(() => {
        requestIdRef.current = genMeshRequestId('mesh-board')
        setTitle('')
        setDetails('')
        return load()
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'request failed'))
      .finally(() => setPosting(false))
  }

  const topLevel = entries.filter((e) => reportOf(e, entries) === undefined)

  return (
    <div className="team-board">
      <form
        className="team-board-form"
        onSubmit={(e) => {
          e.preventDefault()
          post()
        }}
      >
        <label className="team-board-field">
          <span>Task</span>
          <input type="text" value={title} onChange={(e) => setTitle(e.target.value)} placeholder="e.g. CSV export" />
        </label>
        <label className="team-board-field">
          <span>Details</span>
          <textarea value={details} onChange={(e) => setDetails(e.target.value)} placeholder="What should the agents do?" rows={2} />
        </label>
        <div className="team-board-form-actions">
          <span className="team-board-hint">Agents check the board between steps and report under the task.</span>
          <button type="submit" className="btn btn-send" disabled={posting || title.trim() === ''}>
            {posting ? 'Posting…' : 'Post task'}
          </button>
        </div>
        {error !== null && (
          <div className="team-board-error" role="alert">
            {error}
          </div>
        )}
      </form>

      {topLevel.length === 0 ? (
        <div className="team-board-empty empty-hint">Nothing on the board yet — post a task for the agents.</div>
      ) : (
        <ul className="team-board-list">
          {topLevel.map((e) => {
            const reports = entries.filter((r) => reportOf(r, entries)?.key === e.key)
            return (
              <li key={e.key}>
                <article className="team-board-entry" aria-label={e.key} data-task={e.key.startsWith(TASK_PREFIX) || undefined}>
                  <div className="team-board-entry-header">
                    <span className="team-board-entry-key">{e.key}</span>
                    <span className="team-board-entry-version">v{e.version}</span>
                  </div>
                  <p className="team-board-entry-status">{e.status}</p>
                  <div className="team-board-entry-meta">
                    <span className="team-board-entry-author">{senderLabel(e.author_id, participants)}</span>
                  </div>
                  {e.artifact_refs && e.artifact_refs.length > 0 && (
                    <ul className="team-board-entry-artifacts">
                      {e.artifact_refs.map((ref) => (
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
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}
