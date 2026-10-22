import { useRef, useState, type ReactElement } from 'react'
import { MeshApiError, postMeshTask, type MeshBoardTaskAssignment, type MeshParticipant } from '../../api/mesh-client'
import { removeSubmittedText, useRetryableSubmit } from '../../hooks/use-retryable-submit'
import { assignmentText, type BoardTask } from '../../lib/board-tasks'
import { useModalDialog } from '../../lib/focus-trap'
import { boardNotifyCandidates, participantLabel, senderLabel } from '../../lib/mesh-roster'
import { ASSIGNEE_STATE_LABEL, BoardTaskStatusBadge, findTaskCard, isTypedTask } from './BoardTaskCard'
import type { RunTeamListStage } from './RunTeamList'

// TASK_CHANGED — what the operator sees when someone changed the task after the dialog last showed it.
const TASK_CHANGED = 'Task changed, reloaded'

export type BoardTaskDetailsProps = {
  // task — read from the live projection on every render, so a report that arrives while the dialog is open shows.
  task: BoardTask
  participants: MeshParticipant[]
  // stages — live stage statuses: only agents of working stages are offered to assign a typed task to.
  stages?: RunTeamListStage[]
  // onChanged — a typed task was changed here (or turned out stale): the caller reloads its board.
  onChanged?: () => void
  onClose: () => void
  // active — the details' view is visible (Team stays mounted while hidden): see useModalDialog.
  active?: boolean
}

// BoardTaskDetails — the dialog behind a task card, the same in Team and the Board tab: status, who works on the
// task, its text and author. A legacy task is read-only (agents own its status through their reports); a typed
// task lets the operator assign, release and reopen it.
export function BoardTaskDetails({ task, participants, stages = [], onChanged = () => {}, onClose, active = true }: BoardTaskDetailsProps): ReactElement {
  const modalRef = useRef<HTMLDivElement | null>(null)
  const typed = isTypedTask(task)
  const { title, description } = task

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
          Posted by <span>{senderLabel(task.author, participants)}</span>{' '}
          <span className="team-board-entry-version">v{task.version}</span>
        </p>
        {typed ? (
          <TypedTaskBody task={task} participants={participants} stages={stages} onChanged={onChanged} />
        ) : (
          <LegacyTaskBody task={task} participants={participants} />
        )}
      </div>
    </div>
  )
}

type BodyProps = { task: BoardTask; participants: MeshParticipant[] }

// LegacyTaskBody — the task entry's artifacts, who claimed it and each agent's current report.
function LegacyTaskBody({ task, participants }: BodyProps): ReactElement {
  return (
    <>
      {task.entry?.artifact_refs && task.entry.artifact_refs.length > 0 && (
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
              <span className="board-task-details-assignee-state">{ASSIGNEE_STATE_LABEL[a.state]}</span>
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
    </>
  )
}

// TaskChange — one operator change of a typed task, as plain data (useRetryableSubmit compares intents by JSON).
type TaskChange = {
  action: 'assign' | 'release' | 'reopen'
  key: string
  expectedVersion: number
  assignmentId?: string
  to?: string
  text?: string
}

function isStaleRevision(err: unknown): boolean {
  return err instanceof MeshApiError && err.code === 'stale_revision'
}

function endedLabel(a: MeshBoardTaskAssignment, participants: MeshParticipant[]): string {
  const verb = a.ended === 'reopened' ? 'Reopened' : 'Released'
  const by = a.ended_by !== undefined && a.ended_by !== '' ? ` by ${senderLabel(a.ended_by, participants)}` : ''
  const reason = a.end_reason !== undefined && a.end_reason !== '' ? `: ${a.end_reason}` : ''
  return `${verb}${by}${reason}`
}

// TypedTaskBody — the typed task's latest note, its assignments with their own state, the ended ones, and the
// operator's actions: Assign, Release (an active assignment not done yet) and Reopen.
function TypedTaskBody({ task, participants, stages, onChanged }: BodyProps & { stages: RunTeamListStage[]; onChanged: () => void }): ReactElement {
  const assignments = task.assignments ?? []
  const history = task.history ?? []
  const [assignTo, setAssignTo] = useState('')
  const [reason, setReason] = useState('')
  // reasonRef — the reason as last rendered: a change that resolves later keeps what was typed meanwhile.
  const reasonRef = useRef(reason)
  reasonRef.current = reason
  const submitter = useRetryableSubmit<TaskChange>('mesh-task', task.key)

  const assignedIds = new Set(assignments.map((a) => a.participant_id))
  const candidates = boardNotifyCandidates(participants, stages).filter((p) => !assignedIds.has(p.id))
  const canReopen = assignments.length > 0 || history.length > 0
  // A done task is never assigned again (the server refuses it): it is reopened first.
  const canAssign = task.status !== 'done'

  function change(intent: Omit<TaskChange, 'key' | 'expectedVersion'>): void {
    const submittedReason = intent.text ?? ''
    // stale — this attempt hit a newer version of the task: the board is reloaded to show it.
    let stale = false
    const send = async (payload: Readonly<TaskChange>, requestId: string) => {
      try {
        return await postMeshTask({ requestId, ...payload })
      } catch (err) {
        if (!isStaleRevision(err)) throw err
        stale = true
        throw new Error(TASK_CHANGED)
      }
    }
    submitter.submit({ ...intent, key: task.key, expectedVersion: task.version }, send, {
      onSuccess: () => {
        if (intent.action === 'assign') setAssignTo('')
        setReason(removeSubmittedText(reasonRef.current, submittedReason))
        onChanged()
      },
      onFailure: () => {
        if (stale) onChanged()
      },
    })
  }

  // reasonText — the typed reason, or nothing when it is blank (the server then records no reason).
  const reasonText = reason.trim() !== '' ? reason : undefined

  return (
    <>
      {task.note !== undefined && <p className="board-task-details-description board-task-details-progress">{task.note}</p>}

      <h3 className="board-task-details-heading">Assignees</h3>
      {assignments.length === 0 ? (
        <p className="board-task-details-empty">Unassigned</p>
      ) : (
        <ul className="board-task-details-assignees" aria-label="Assignees">
          {assignments.map((a) => {
            const name = senderLabel(a.participant_id, participants)
            const text = assignmentText(a)
            return (
              <li key={a.id}>
                <span className="board-task-details-assignee-name">
                  {name}
                  {text !== undefined && <span className="board-task-details-assignee-text">{text}</span>}
                </span>
                <span className="board-task-details-assignee-actions">
                  <span className="board-task-details-assignee-state">{ASSIGNEE_STATE_LABEL[a.state]}</span>
                  {a.state !== 'done' && (
                    <button
                      type="button"
                      className="btn btn-cancel board-task-details-action"
                      aria-label={`Release ${name}`}
                      disabled={submitter.pending}
                      onClick={() => change({ action: 'release', assignmentId: a.id, text: reasonText })}
                    >
                      Release
                    </button>
                  )}
                </span>
              </li>
            )
          })}
        </ul>
      )}

      {history.length > 0 && (
        <>
          <h3 className="board-task-details-heading">History</h3>
          <ul className="board-task-details-assignees" aria-label="History">
            {history.map((a) => (
              <li key={a.id}>
                <span className="board-task-details-assignee-name">
                  {senderLabel(a.participant_id, participants)}
                  {a.result && <span className="board-task-details-assignee-text">{a.result}</span>}
                </span>
                <span className="board-task-details-assignee-state">{endedLabel(a, participants)}</span>
              </li>
            ))}
          </ul>
        </>
      )}

      {canAssign && (
        <form
          className="board-task-details-assign"
          onSubmit={(e) => {
            e.preventDefault()
            if (assignTo !== '') change({ action: 'assign', to: assignTo })
          }}
        >
          <select aria-label="Assign to" value={assignTo} onChange={(e) => setAssignTo(e.target.value)}>
            <option value="">Choose an agent</option>
            {candidates.map((p) => (
              <option key={p.id} value={p.id}>
                {participantLabel(p)}
              </option>
            ))}
          </select>
          <button type="submit" className="btn btn-send" disabled={submitter.pending || assignTo === ''}>
            Assign
          </button>
        </form>
      )}

      <label className="team-board-field">
        <span>Reason</span>
        <textarea value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Why release or reopen (optional)" rows={2} />
      </label>
      {canReopen && (
        <div className="board-task-details-reopen">
          <button type="button" className="btn btn-cancel" disabled={submitter.pending} onClick={() => change({ action: 'reopen', text: reasonText })}>
            Reopen
          </button>
        </div>
      )}

      {submitter.error !== null && (
        <div className="team-board-error" role="alert">
          {submitter.error}
        </div>
      )}
    </>
  )
}
