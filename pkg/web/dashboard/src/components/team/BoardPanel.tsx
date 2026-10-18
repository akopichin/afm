import { useState, type ReactElement } from 'react'
import { publishMeshBoard, type MeshBoardEntry, type MeshParticipant } from '../../api/mesh-client'
import type { MeshBoard } from '../../hooks/use-mesh'
import { removeSubmittedText, useRetryableSubmit } from '../../hooks/use-retryable-submit'
import { boardNotifyCandidates, participantLabel, senderLabel } from '../../lib/mesh-roster'
import type { RunTeamListStage } from './RunTeamList'

const TASK_PREFIX = 'task/'

export type BoardPanelProps = {
  // board — the shared board, polled by the Team view (which also counts its tasks on the tab).
  board: MeshBoard
  // participants — to show who published an entry by name.
  participants: MeshParticipant[]
  // stages — live stage statuses: only agents of working stages are offered for notification.
  stages: RunTeamListStage[]
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

// boardTopLevel — the board's own entries (tasks and facts), without the reports filed under them.
export function boardTopLevel(entries: MeshBoardEntry[]): MeshBoardEntry[] {
  return entries.filter((e) => reportOf(e, entries) === undefined)
}

// boardTaskCount — the tasks on the board (the Board tab's badge): top-level "task/…" keys, not facts or reports.
export function boardTaskCount(entries: MeshBoardEntry[]): number {
  return boardTopLevel(entries).filter((e) => e.key.startsWith(TASK_PREFIX)).length
}

// BoardPanel — the Team view's "Board" tab: the shared board. The operator
// posts tasks for the agents here; agents publish facts and report on tasks under "<task key>/<alias>".
export function BoardPanel({ board, participants, stages }: BoardPanelProps): ReactElement {
  const { entries } = board
  const [title, setTitle] = useState('')
  const [details, setDetails] = useState('')
  // uncheckedIds — candidates the operator opted out of; everyone else (incl. agents that appear later) is notified.
  const [uncheckedIds, setUncheckedIds] = useState<ReadonlySet<string>>(new Set())
  // submitter — a retry of the same task reuses its request id; an edited task is a new one.
  const submitter = useRetryableSubmit<{ key: string; status: string; notify: string[] }>('mesh-board')

  const candidates = boardNotifyCandidates(participants, stages)

  function post(): void {
    const submittedTitle = title
    const submittedDetails = details
    const name = submittedTitle.trim()
    if (name === '') return
    const text = submittedDetails.trim()
    submitter.submit(
      {
        key: taskKey(name),
        status: text !== '' ? `${name} — ${text}` : name,
        notify: candidates.filter((p) => !uncheckedIds.has(p.id)).map((p) => p.id),
      },
      (p, requestId) => publishMeshBoard({ requestId, key: p.key, status: p.status, notify: [...p.notify] }),
      {
        onSuccess: () => {
          setTitle((cur) => removeSubmittedText(cur, submittedTitle))
          setDetails((cur) => removeSubmittedText(cur, submittedDetails))
          void board.reload()
        },
      },
    )
  }

  function toggleNotify(id: string): void {
    setUncheckedIds((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const topLevel = boardTopLevel(entries)

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
        {candidates.length > 0 && (
          <fieldset className="team-board-notify">
            <legend>Notify</legend>
            {candidates.map((p) => (
              <label key={p.id} className="team-board-notify-option">
                <input type="checkbox" checked={!uncheckedIds.has(p.id)} onChange={() => toggleNotify(p.id)} />
                {participantLabel(p)}
              </label>
            ))}
          </fieldset>
        )}
        <div className="team-board-form-actions">
          <span className="team-board-hint">Agents check the board between steps and report under the task.</span>
          <button type="submit" className="btn btn-send" disabled={submitter.pending || title.trim() === ''}>
            {submitter.pending ? 'Posting…' : 'Post task'}
          </button>
        </div>
        {submitter.error !== null && (
          <div className="team-board-error" role="alert">
            {submitter.error}
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
