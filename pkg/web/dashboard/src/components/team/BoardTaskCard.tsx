import type { ReactElement } from 'react'
import type { MeshParticipant } from '../../api/mesh-client'
import type { BoardTask, BoardTaskStatus } from '../../lib/board-tasks'
import { senderLabel } from '../../lib/mesh-roster'
import { MeshAvatar } from './MeshAvatar'

export const BOARD_STATUS_LABEL: Record<BoardTaskStatus, string> = {
  todo: 'Todo',
  in_progress: 'In progress',
  done: 'Done',
}

// The task form publishes "<title> — <details>"; a task an agent published may have no details.
const TITLE_SEPARATOR = ' — '

// splitTaskText — the task's title and its description (empty when the text has no details).
export function splitTaskText(text: string): { title: string; description: string } {
  const at = text.indexOf(TITLE_SEPARATOR)
  if (at < 0) return { title: text, description: '' }
  return { title: text.slice(0, at), description: text.slice(at + TITLE_SEPARATOR.length) }
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

export type BoardTaskCardProps = {
  task: BoardTask
  // participants — to name the assignees and the author of the latest report.
  participants: MeshParticipant[]
  // onOpen — the card is a button: click, Enter and Space all open the task's details.
  onOpen: (task: BoardTask) => void
}

// BoardTaskCard — one task of the shared board in Team's mixed grid and the Board tab's columns. Read-only: the
// status is whatever the agents' reports say (see lib/board-tasks), the card never changes it.
export function BoardTaskCard({ task, participants, onOpen }: BoardTaskCardProps): ReactElement {
  const { title, description } = splitTaskText(task.text)
  const latest = task.reports.at(-1)
  const reportCount = task.reports.length
  return (
    <button
      type="button"
      className="board-task-card"
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
                <span>{name}</span>
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
