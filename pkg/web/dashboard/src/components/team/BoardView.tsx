import { useRef, useState, type ReactElement } from 'react'
import type { MeshParticipant } from '../../api/mesh-client'
import type { MeshBoard } from '../../hooks/use-mesh'
import { boardTasksOf, type BoardTask, type BoardTaskStatus } from '../../lib/board-tasks'
import { useModalDialog } from '../../lib/focus-trap'
import { participantLabel, senderLabel } from '../../lib/mesh-roster'
import { BOARD_STATUS_LABEL, BoardTaskCard } from './BoardTaskCard'
import { BoardTaskDetails } from './BoardTaskDetails'
import { BoardTaskForm, boardTaskModeOf, type BoardTaskMode } from './BoardTaskForm'
import type { RunTeamListStage } from './RunTeamList'

const LANES: BoardTaskStatus[] = ['todo', 'in_progress', 'done']

const ALL_ASSIGNEES = 'all'
const UNASSIGNED = 'unassigned'

export type BoardViewProps = {
  // board — the shared board, polled by App; a report that moves a task between lanes arrives as new entries.
  board: MeshBoard
  // participants — the live roster: it names the agents on the cards and fills the assignee filter.
  participants: MeshParticipant[]
  // stages — live stage statuses: only agents of working stages are offered for notification.
  stages: RunTeamListStage[]
}

// searchText — what a search looks in: the key, the task's own fields and its latest note.
function searchText(task: BoardTask): string {
  return [task.key, task.title, task.description, task.note ?? ''].join('\n')
}

function matchesSearch(task: BoardTask, query: string): boolean {
  return searchText(task).toLowerCase().includes(query)
}

function matchesAssignee(task: BoardTask, assignee: string): boolean {
  if (assignee === ALL_ASSIGNEES) return true
  if (assignee === UNASSIGNED) return task.assignees.length === 0
  return task.assignees.some((a) => a.id === assignee)
}

// Lanes follow the live task projection; assignment actions stay in the shared details dialog.
export function BoardView({ board, participants, stages }: BoardViewProps): ReactElement {
  const [search, setSearch] = useState('')
  const [assignee, setAssignee] = useState(ALL_ASSIGNEES)
  const [newTaskOpen, setNewTaskOpen] = useState(false)
  // Only the key is kept: the dialog reads the task from the latest poll, and closes if the task is gone.
  const [openKey, setOpenKey] = useState<string | null>(null)
  const [announcement, setAnnouncement] = useState('')

  const tasks = boardTasksOf(board, participants)
  const taskByKey = new Map(tasks.map((t) => [t.key, t]))
  const openTask = openKey === null ? undefined : taskByKey.get(openKey)

  const query = search.trim().toLowerCase()
  const filtering = query !== '' || assignee !== ALL_ASSIGNEES
  const shown = tasks.filter((t) => matchesSearch(t, query) && matchesAssignee(t, assignee))
  const doneCount = tasks.filter((t) => t.status === 'done').length
  // An assignee that left the roster (or never was in it) can still be filtered by.
  const outsideRoster = [...new Set(tasks.flatMap((t) => t.assignees.map((a) => a.id)))].filter((id) => !participants.some((p) => p.id === id))

  function openNewTask(): void {
    setAnnouncement('')
    setNewTaskOpen(true)
  }

  // A new task is always Todo, so the filters that could hide it are cleared and the post is announced. The
  // dialog closes (focus goes back to New task) unless text typed while posting is still in the form. The new
  // card never takes focus when it arrives: by then the operator may be typing or in another dialog.
  function handlePosted(draftRemains: boolean): void {
    setSearch('')
    setAssignee(ALL_ASSIGNEES)
    setAnnouncement('Task added to Todo')
    void board.reload()
    if (!draftRemains) setNewTaskOpen(false)
  }

  return (
    <div className="board-view">
      <header className="board-view-header">
        <div className="board-view-heading">
          <h2 className="board-view-title">Board</h2>
          <p className="board-view-completion">
            {filtering && <span>{`${shown.length} shown`}</span>}
            <span>{`${doneCount} of ${tasks.length} tasks done`}</span>
            <span>· whole run</span>
          </p>
        </div>
        <button
          type="button"
          className="btn btn-send"
          onClick={openNewTask}
        >
          <span aria-hidden="true">＋</span> New task
        </button>
      </header>

      <div className="board-view-filters">
        <input
          type="search"
          className="board-view-search"
          aria-label="Search tasks"
          placeholder="Search tasks"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        <select className="board-view-assignee" aria-label="Assignee" value={assignee} onChange={(e) => setAssignee(e.target.value)}>
          <option value={ALL_ASSIGNEES}>All assignees</option>
          <option value={UNASSIGNED}>Unassigned</option>
          {participants.map((p) => (
            <option key={p.id} value={p.id}>
              {participantLabel(p)}
            </option>
          ))}
          {outsideRoster.map((id) => (
            <option key={id} value={id}>
              {senderLabel(id, participants)}
            </option>
          ))}
        </select>
      </div>

      <p className="board-view-announce" role="status" aria-label="Board updates">
        {announcement}
      </p>

      <div className="board-lanes">
        {LANES.map((status) => {
          const laneTasks = shown.filter((t) => t.status === status)
          return (
            <section key={status} className="board-lane" data-status={status} aria-label={`${BOARD_STATUS_LABEL[status]} tasks`}>
              <header className="board-lane-header">
                <h3 className="board-lane-title">{BOARD_STATUS_LABEL[status]}</h3>
                <span className="board-lane-count">{laneTasks.length}</span>
              </header>
              {laneTasks.length === 0 ? (
                <p className="board-lane-empty empty-hint">{filtering ? 'No matching tasks' : 'No tasks'}</p>
              ) : (
                <ul className="board-lane-cards">
                  {laneTasks.map((task) => (
                    <li key={task.key}>
                      <BoardTaskCard task={task} participants={participants} onOpen={(t) => setOpenKey(t.key)} />
                    </li>
                  ))}
                </ul>
              )}
            </section>
          )
        })}
      </div>

      {newTaskOpen && (
        <NewTaskDialog participants={participants} stages={stages} mode={boardTaskModeOf(board)} onPosted={handlePosted} onClose={() => setNewTaskOpen(false)} />
      )}
      {openTask !== undefined && (
        <BoardTaskDetails task={openTask} participants={participants} stages={stages} onChanged={() => void board.reload()} onClose={() => setOpenKey(null)} />
      )}
    </div>
  )
}

type NewTaskDialogProps = {
  participants: MeshParticipant[]
  stages: RunTeamListStage[]
  mode: BoardTaskMode
  onPosted: (draftRemains: boolean) => void
  onClose: () => void
}

// NewTaskDialog — the task form in a modal: focus moves into it, Tab stays inside, Escape closes it and focus
// goes back to the button that opened it (the shared useModalDialog, like BoardTaskDetails).
function NewTaskDialog({ participants, stages, mode, onPosted, onClose }: NewTaskDialogProps): ReactElement {
  const modalRef = useRef<HTMLDivElement | null>(null)
  useModalDialog(modalRef, onClose)

  return (
    <div className="modal-overlay" role="dialog" aria-modal="true" aria-label="New task">
      <div className="modal-content board-new-task" ref={modalRef}>
        <h2 className="board-new-task-title">New task</h2>
        <BoardTaskForm participants={participants} stages={stages} mode={mode} onPosted={onPosted} />
        <div className="modal-actions">
          <button type="button" className="btn btn-quiet" onClick={onClose}>
            Cancel
          </button>
        </div>
      </div>
    </div>
  )
}
