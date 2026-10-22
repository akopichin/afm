import type { ReactElement } from 'react'
import type { MeshParticipant } from '../../api/mesh-client'
import { splitTaskText, type BoardAssignee, type BoardTask, type BoardTaskStatus } from '../../lib/board-tasks'
import { senderLabel } from '../../lib/mesh-roster'
import { MeshAvatar } from './MeshAvatar'

export const BOARD_STATUS_LABEL: Record<BoardTaskStatus, string> = {
  todo: 'Todo',
  in_progress: 'In progress',
  done: 'Done',
}

// ASSIGNEE_STATE_LABEL — what one assignee is doing: a typed assignment can be merely assigned.
export const ASSIGNEE_STATE_LABEL: Record<BoardAssignee['state'], string> = {
  assigned: 'Assigned',
  in_progress: 'In progress',
  done: 'Done',
}

// isTypedTask — the task comes from the typed task protocol (it has assignments of its own, not reports).
export function isTypedTask(task: BoardTask): boolean {
  return task.assignments !== undefined
}

// StatusIcon — a different shape per status, so the status reads without relying on color.
function StatusIcon({ status }: { status: BoardTaskStatus }): ReactElement {
  return (
    <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <circle cx="8" cy="8" r="6" strokeDasharray={status === 'todo' ? '2.4 2.4' : undefined} />
      {status === 'in_progress' && <path d="M8 2a6 6 0 0 1 0 12z" fill="currentColor" />}
      {status === 'done' && <path d="m5.2 8.2 1.9 1.9 3.7-3.9" />}
    </svg>
  )
}

// BoardTaskStatusBadge — the status as icon + text; its color comes from the data-status hook in mesh.css.
export function BoardTaskStatusBadge({ status }: { status: BoardTaskStatus }): ReactElement {
  return (
    <span className="board-task-status" data-status={status}>
      <StatusIcon status={status} />
      <span>{BOARD_STATUS_LABEL[status]}</span>
    </span>
  )
}

// findTaskCard — the card that shows the task now, wherever the latest poll put it (a card that moved to another
// lane is a new element).
export function findTaskCard(key: string, root: ParentNode = document): HTMLElement | undefined {
  return Array.from(root.querySelectorAll<HTMLElement>('.board-task-card[data-task-key]')).find((el) => el.dataset.taskKey === key)
}

export type BoardTaskCardProps = {
  task: BoardTask
  // participants — to name the assignees and the author of the latest report.
  participants: MeshParticipant[]
  // onOpen — the card is a button: click, Enter and Space all open the task's details.
  onOpen: (task: BoardTask) => void
}

// BoardTaskCard — one task of the shared board in Team's mixed grid and the Board tab's columns. Read-only: the
// status is whatever the agents' reports (legacy) or assignments (typed) say, the card never changes it.
export function BoardTaskCard({ task, participants, onOpen }: BoardTaskCardProps): ReactElement {
  const typed = isTypedTask(task)
  // A typed task carries its own fields; a legacy one keeps them in its text.
  const { title, description } = typed ? task : splitTaskText(task.text)
  const latest = task.reports.at(-1)
  const reportCount = task.reports.length
  return (
    <button
      type="button"
      className="board-task-card"
      data-task-key={task.key}
      data-status={task.status}
      aria-label={`${title}, ${BOARD_STATUS_LABEL[task.status]}`}
      onClick={() => onOpen(task)}
    >
      <span className="board-task-key-row">
        <span className="board-task-key">{task.key}</span>
        <BoardTaskStatusBadge status={task.status} />
      </span>
      <span className="board-task-title">{title}</span>
      {description !== '' && <span className="board-task-description">{description}</span>}
      {typed && task.note !== undefined && <span className="board-task-latest-report">{task.note}</span>}
      {latest !== undefined && (
        <span className="board-task-latest-report">
          <strong>{senderLabel(latest.entry.author_id, participants)}</strong> · {latest.entry.status}
        </span>
      )}
      <span className="board-task-bottom">
        {task.assignees.length === 0 ? (
          <span className="board-task-unassigned">Unassigned</span>
        ) : (
          task.assignees.map((a) => {
            const name = senderLabel(a.id, participants)
            return (
              <span key={a.id} className="board-task-assignee">
                <MeshAvatar name={name} kind={participants.find((p) => p.id === a.id)?.kind ?? 'run_agent'} size="small" />
                <span className="board-task-assignee-name">{name}</span>
                {typed && <span className="board-task-assignee-state">{ASSIGNEE_STATE_LABEL[a.state]}</span>}
              </span>
            )
          })
        )}
        {reportCount > 0 && (
          <span className="board-task-report-count">
            {reportCount} {reportCount === 1 ? 'report' : 'reports'}
          </span>
        )}
      </span>
    </button>
  )
}
