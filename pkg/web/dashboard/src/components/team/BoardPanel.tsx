import { useState, type ReactElement } from 'react'
import type { MeshParticipant } from '../../api/mesh-client'
import type { MeshBoard } from '../../hooks/use-mesh'
import { boardReportsOf, boardTasks, boardTopLevel, type BoardTaskStatus } from '../../lib/board-tasks'
import { senderLabel } from '../../lib/mesh-roster'
import { BOARD_STATUS_LABEL, BoardTaskCard, BoardTaskStatusBadge } from './BoardTaskCard'
import { BoardTaskDetails } from './BoardTaskDetails'
import { BoardTaskForm } from './BoardTaskForm'
import type { RunTeamListStage } from './RunTeamList'

const STATUSES: BoardTaskStatus[] = ['todo', 'in_progress', 'done']

export type BoardPanelProps = {
  // board — the shared board, polled by the Team view (which also counts its tasks on the tab).
  board: MeshBoard
  // participants — to show who published an entry by name.
  participants: MeshParticipant[]
  // stages — live stage statuses: only agents of working stages are offered for notification.
  stages: RunTeamListStage[]
}

// BoardPanel — the Team view's "Board" tab: the shared board. The operator posts tasks for the agents here;
// tasks are cards coloured by status (Todo, In progress, Done) in ONE mixed grid with the agents' facts — the
// grouped columns are the Board tab's. Agents report on a task under "<task key>/<alias>"; reports stay inside the
// task's card and details and never become cards of their own.
export function BoardPanel({ board, participants, stages }: BoardPanelProps): ReactElement {
  const { entries } = board
  const tasks = boardTasks(entries, participants)
  const taskByKey = new Map(tasks.map((t) => [t.key, t]))
  // Only the key is kept: the dialog reads the task from the latest poll, and closes if the task is gone.
  const [openKey, setOpenKey] = useState<string | null>(null)
  const openTask = openKey === null ? undefined : taskByKey.get(openKey)
  const topLevel = boardTopLevel(entries)

  return (
    <div className="team-board">
      <BoardTaskForm participants={participants} stages={stages} onPosted={() => void board.reload()} />

      {topLevel.length === 0 ? (
        <div className="team-board-empty empty-hint">Nothing on the board yet — post a task for the agents.</div>
      ) : (
        <>
          {tasks.length > 0 && (
            <ul className="team-board-legend" aria-label="Statuses">
              {STATUSES.map((status) => {
                const count = tasks.filter((t) => t.status === status).length
                return (
                  <li key={status} aria-label={`${BOARD_STATUS_LABEL[status]}: ${count}`}>
                    <BoardTaskStatusBadge status={status} />
                    <span className="team-board-legend-count">{count}</span>
                  </li>
                )
              })}
            </ul>
          )}
          <ul className="team-board-list">
            {topLevel.map((e) => {
              const task = taskByKey.get(e.key)
              if (task !== undefined) {
                return (
                  <li key={e.key}>
                    <BoardTaskCard task={task} participants={participants} onOpen={(t) => setOpenKey(t.key)} />
                  </li>
                )
              }
              const reports = boardReportsOf(e, entries)
              return (
                <li key={e.key}>
                  <article className="team-board-entry" aria-label={e.key}>
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
        </>
      )}

      {openTask !== undefined && <BoardTaskDetails task={openTask} participants={participants} onClose={() => setOpenKey(null)} />}
    </div>
  )
}
