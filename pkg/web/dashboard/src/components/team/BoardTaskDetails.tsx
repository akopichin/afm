import { useRef, type ReactElement } from 'react'
import type { MeshParticipant } from '../../api/mesh-client'
import type { BoardTask } from '../../lib/board-tasks'
import { useModalDialog } from '../../lib/focus-trap'
import { senderLabel } from '../../lib/mesh-roster'
import { BoardTaskStatusBadge, findTaskCard, splitTaskText } from './BoardTaskCard'

export type BoardTaskDetailsProps = {
  // task — read from the live projection on every render, so a report that arrives while the dialog is open shows.
  task: BoardTask
  participants: MeshParticipant[]
  onClose: () => void
  // active — the details' view is visible (Team stays mounted while hidden): see useModalDialog.
  active?: boolean
}

// BoardTaskDetails — the dialog behind a task card, the same in Team and the Board tab: status, who works on the
// task, its text, author and each agent's current report. Read-only on purpose — agents own the status.
export function BoardTaskDetails({ task, participants, onClose, active = true }: BoardTaskDetailsProps): ReactElement {
  const modalRef = useRef<HTMLDivElement | null>(null)
  const { title, description } = splitTaskText(task.text)

  // Focus moves into the dialog and, on close, goes back to the task's card — the one the board shows now.
  useModalDialog(modalRef, onClose, () => findTaskCard(task.key), active)

  return (
    <div className="modal-overlay" role="dialog" aria-modal="true" aria-label="Task details">
      <div className="modal-content board-task-details" data-status={task.status} ref={modalRef}>
        <div className="board-task-details-top">
          <BoardTaskStatusBadge status={task.status} />
          <span className="board-task-key">{task.key}</span>
          <button type="button" className="btn btn-cancel board-task-details-close" aria-label="Close task details" onClick={onClose}>
            ×
          </button>
        </div>
        <h2 className="board-task-details-title">{title}</h2>
        {description !== '' && <p className="board-task-details-description">{description}</p>}
        <p className="board-task-details-author">
          Posted by <span>{senderLabel(task.entry.author_id, participants)}</span>{' '}
          <span className="team-board-entry-version">v{task.entry.version}</span>
        </p>
        {task.entry.artifact_refs && task.entry.artifact_refs.length > 0 && (
          <ul className="team-board-entry-artifacts" aria-label="Artifacts">
            {task.entry.artifact_refs.map((ref) => (
              <li key={ref}>{ref}</li>
            ))}
          </ul>
        )}

        <h3 className="board-task-details-heading">Assignees</h3>
        {task.assignees.length === 0 ? (
          <p className="board-task-details-empty">Unassigned</p>
        ) : (
          <ul className="board-task-details-assignees" aria-label="Assignees">
            {task.assignees.map((a) => (
              <li key={a.id}>
                <span className="board-task-details-assignee-name">{senderLabel(a.id, participants)}</span>
                <span className="board-task-details-assignee-state">{a.inProgress ? 'In progress' : 'Done'}</span>
              </li>
            ))}
          </ul>
        )}

        <h3 className="board-task-details-heading">Reports</h3>
        {task.reports.length === 0 ? (
          <p className="board-task-details-empty">No reports yet.</p>
        ) : (
          <ul className="board-task-details-reports" aria-label="Reports">
            {task.reports.map((r) => (
              <li key={r.entry.key} className="team-board-report">
                <span className="team-board-report-author">{senderLabel(r.entry.author_id, participants)}</span>
                <span className="team-board-report-status">{r.entry.status}</span>
              </li>
            ))}
          </ul>
        )}

        <p className="board-task-details-note">Statuses are updated by agents.</p>
      </div>
    </div>
  )
}
