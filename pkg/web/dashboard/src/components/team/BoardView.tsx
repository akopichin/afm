import { useEffect, useRef, useState, type ReactElement } from 'react'
import type { MeshParticipant } from '../../api/mesh-client'
import type { MeshBoard } from '../../hooks/use-mesh'
import { boardTasks, type BoardTask, type BoardTaskStatus } from '../../lib/board-tasks'
import { visibleFocusables } from '../../lib/focus-trap'
import { participantLabel } from '../../lib/mesh-roster'
import { BOARD_STATUS_LABEL, BoardTaskCard } from './BoardTaskCard'
import { BoardTaskDetails } from './BoardTaskDetails'
import { BoardTaskForm } from './BoardTaskForm'
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

function matchesSearch(task: BoardTask, query: string): boolean {
  return `${task.key}\n${task.text}`.toLowerCase().includes(query)
}

function matchesAssignee(task: BoardTask, assignee: string): boolean {
  if (assignee === ALL_ASSIGNEES) return true
  if (assignee === UNASSIGNED) return task.assignees.length === 0
  return task.assignees.some((a) => a.id === assignee)
}

// BoardView — the run-wide Board tab: the same tasks as Team's mixed board, grouped into Todo / In progress / Done
// by what the agents reported. Read-only on purpose: there is no way to move a card, agents own the status.
export function BoardView({ board, participants, stages }: BoardViewProps): ReactElement {
  const [search, setSearch] = useState('')
  const [assignee, setAssignee] = useState(ALL_ASSIGNEES)
  const [newTaskOpen, setNewTaskOpen] = useState(false)
  // Only the key is kept: the dialog reads the task from the latest poll, and closes if the task is gone.
  const [openKey, setOpenKey] = useState<string | null>(null)
  // focusKey — a task just posted: it takes focus as soon as its card is on the board.
  const [focusKey, setFocusKey] = useState<string | null>(null)
  const [announcement, setAnnouncement] = useState('')
  const lanesRef = useRef<HTMLDivElement | null>(null)

  const tasks = boardTasks(board.entries, participants)
  const taskByKey = new Map(tasks.map((t) => [t.key, t]))
  const openTask = openKey === null ? undefined : taskByKey.get(openKey)

  const query = search.trim().toLowerCase()
  const filtering = query !== '' || assignee !== ALL_ASSIGNEES
  const shown = tasks.filter((t) => matchesSearch(t, query) && matchesAssignee(t, assignee))
  const doneCount = tasks.filter((t) => t.status === 'done').length

  useEffect(() => {
    if (focusKey === null) return
    const item = Array.from(lanesRef.current?.querySelectorAll<HTMLElement>('[data-task-key]') ?? []).find(
      (el) => el.dataset.taskKey === focusKey,
    )
    if (item === undefined) return
    item.querySelector('button')?.focus()
    setFocusKey(null)
  }, [focusKey, board.entries])

  // A new task is always Todo, so the filters that could hide it are cleared and its card is focused once it arrives.
  function handlePosted(key: string): void {
    setSearch('')
    setAssignee(ALL_ASSIGNEES)
    setNewTaskOpen(false)
    setFocusKey(key)
    setAnnouncement('Task added to Todo')
    void board.reload()
  }

  return (
    <div className="board-view">
      <header className="board-view-header">
        <div className="board-view-heading">
          <h2 className="board-view-title">Board</h2>
          <p className="board-view-completion">
            {filtering && <span>{`${shown.length} shown`}</span>}
            <span>{`${doneCount} of ${tasks.length} completed`}</span>
            <span className="board-view-progress" aria-hidden="true">
              <span style={{ width: tasks.length === 0 ? '0%' : `${(doneCount / tasks.length) * 100}%` }} />
            </span>
          </p>
        </div>
        <button
          type="button"
          className="btn btn-send"
          onClick={() => {
            setAnnouncement('')
            setNewTaskOpen(true)
          }}
        >
          New task
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
        </select>
      </div>

      <p className="board-view-announce" role="status" aria-label="Board updates">
        {announcement}
      </p>

      <div className="board-lanes" ref={lanesRef}>
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
                    <li key={task.key} data-task-key={task.key}>
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
        <NewTaskDialog participants={participants} stages={stages} onPosted={handlePosted} onClose={() => setNewTaskOpen(false)} />
      )}
      {openTask !== undefined && <BoardTaskDetails task={openTask} participants={participants} onClose={() => setOpenKey(null)} />}
    </div>
  )
}

type NewTaskDialogProps = {
  participants: MeshParticipant[]
  stages: RunTeamListStage[]
  onPosted: (key: string) => void
  onClose: () => void
}

// NewTaskDialog — the task form in a modal: focus moves into it, Tab stays inside, Escape closes it and focus
// goes back to the button that opened it. Same keyboard contract as BoardTaskDetails.
function NewTaskDialog({ participants, stages, onPosted, onClose }: NewTaskDialogProps): ReactElement {
  const modalRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    const opener = document.activeElement
    const modal = modalRef.current
    if (modal !== null) visibleFocusables(modal)[0]?.focus()
    return () => {
      if (opener instanceof HTMLElement) opener.focus()
    }
  }, [])

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent): void {
      if (e.key === 'Escape') {
        e.preventDefault()
        onClose()
        return
      }
      const modal = modalRef.current
      if (e.key !== 'Tab' || modal === null) return
      const items = visibleFocusables(modal)
      const first = items[0]
      const last = items[items.length - 1]
      if (first === undefined || last === undefined) return
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault()
        last.focus()
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault()
        first.focus()
      }
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [onClose])

  return (
    <div className="modal-overlay" role="dialog" aria-modal="true" aria-label="New task">
      <div className="modal-content board-new-task" ref={modalRef}>
        <h2 className="board-new-task-title">New task</h2>
        <BoardTaskForm participants={participants} stages={stages} onPosted={onPosted} />
        <div className="modal-actions">
          <button type="button" className="btn btn-cancel" onClick={onClose}>
            Cancel
          </button>
        </div>
      </div>
    </div>
  )
}
