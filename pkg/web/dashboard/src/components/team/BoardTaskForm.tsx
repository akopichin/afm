import { useRef, useState, type ReactElement } from 'react'
import { postMeshTask, publishMeshBoard, type MeshParticipant } from '../../api/mesh-client'
import { removeSubmittedText, useRetryableSubmit, useUnsettledChange } from '../../hooks/use-retryable-submit'
import type { MeshBoard } from '../../hooks/use-mesh'
import { boardNotifyCandidates, participantLabel } from '../../lib/mesh-roster'
import type { RunTeamListStage } from './RunTeamList'

const TASK_PREFIX = 'task/'
const MAX_BOARD_NOTIFY = 16

// BoardTaskMode — how the run keeps its tasks: 'unknown' until the board's first load says it.
export type BoardTaskMode = 'unknown' | 'legacy' | 'typed'

// boardTaskModeOf — the task mode the board's last successful load reported.
export function boardTaskModeOf(board: MeshBoard): BoardTaskMode {
  if (board.status === 'loading' || board.status === 'error') return 'unknown'
  return board.typedTasks ? 'typed' : 'legacy'
}

// TypedCreate — a typed task's create request as plain data (useRetryableSubmit compares intents by JSON).
type TypedCreate = { key: string; title: string; description?: string; to?: string }

export type BoardTaskFormProps = {
  // participants / stages — who can be notified: reachable run agents and agents of working stages.
  participants: MeshParticipant[]
  stages: RunTeamListStage[]
  // mode — 'typed': the task is created through /api/mesh/tasks and may be assigned to one agent at once;
  // 'legacy': it is published with a list of agents to notify; 'unknown': the form posts nothing, so a typed
  // run never gets a legacy publish before its board has loaded.
  mode: BoardTaskMode
  // onPosted — the task was published; the caller refreshes its board (and may close the dialog the form sits
  // in). draftRemains — text typed while the post was in flight is still in the form.
  onPosted: (draftRemains: boolean) => void
}

// The server's whole key is at most 200 bytes (agentcomm.ValidBoardTaskKey).
const MAX_KEY_BYTES = 200
const utf8 = new TextEncoder()

// truncateToBytes — the longest prefix of text that fits maxBytes of UTF-8, cut between code points.
function truncateToBytes(text: string, maxBytes: number): string {
  let bytes = 0
  let out = ''
  for (const char of text) {
    bytes += utf8.encode(char).length
    if (bytes > maxBytes) break
    out += char
  }
  return out
}

// taskKey — "task/<slug>" for a task title ("CSV export" → "task/csv-export"). The slug has only the characters the
// server accepts in a task key (letters, digits, "." "_" "-") and keeps the whole key within MAX_KEY_BYTES.
function taskKey(title: string): string {
  const slug = truncateToBytes(
    title
      .toLowerCase()
      .trim()
      .replace(/[^\p{L}\p{N}._-]+/gu, '-')
      .replace(/^-+/, ''),
    MAX_KEY_BYTES - TASK_PREFIX.length,
  ).replace(/-+$/, '')
  return TASK_PREFIX + (slug !== '' ? slug : 'untitled')
}

// BoardTaskForm — the one form that posts a task for the agents, used by Team's Board and the Board tab. The
// operator only names the task and picks who to notify (legacy) or, optionally, who to assign it to (typed): a
// new task is always Todo, agents own its status.
export function BoardTaskForm({ participants, stages, mode, onPosted }: BoardTaskFormProps): ReactElement {
  const typed = mode === 'typed'
  const [title, setTitle] = useState('')
  const [details, setDetails] = useState('')
  // assignTo — the typed task's assignee picked in the form; '' is nobody yet.
  const [assignTo, setAssignTo] = useState('')
  // uncheckedIds — candidates the operator opted out of; everyone else (incl. agents that appear later) is notified.
  const [uncheckedIds, setUncheckedIds] = useState<ReadonlySet<string>>(new Set())
  // submitter — a retry of the same task reuses its request id; an edited task is a new one.
  const submitter = useRetryableSubmit<{ key: string; status: string; notify: string[] }>('mesh-board')
  const typedSubmitter = useRetryableSubmit<TypedCreate>('mesh-task')
  // unsettledCreate — a create whose response was lost is resent as it was (assignee included) while the same task
  // is posted again, even if the assignee left the candidates meanwhile; a success or a refusal settles it.
  const unsettledCreate = useUnsettledChange<TypedCreate>((a, b) => a.key === b.key)
  // draftRef — the form's text as last rendered: a post that resolves later reads what was typed meanwhile.
  const draftRef = useRef({ title, details })
  draftRef.current = { title, details }

  const candidates = boardNotifyCandidates(participants, stages)
  // assignee — the picked agent while it is still a candidate; the select shows and the create sends only it.
  const assignee = candidates.some((p) => p.id === assignTo) ? assignTo : ''
  const notifyIds = candidates.filter((p) => !uncheckedIds.has(p.id)).map((p) => p.id)
  // A task notifies at most MAX_BOARD_NOTIFY agents (the server's agentcomm.MaxBoardNotify); a typed task notifies
  // only its assignee.
  const tooManyNotified = !typed && notifyIds.length > MAX_BOARD_NOTIFY
  const pending = typed ? typedSubmitter.pending : submitter.pending
  const error = typed ? typedSubmitter.error : submitter.error

  function post(): void {
    const submittedTitle = title
    const submittedDetails = details
    const name = submittedTitle.trim()
    if (name === '' || tooManyNotified || mode === 'unknown') return
    const text = submittedDetails.trim()
    const key = taskKey(name)
    const onSuccess = (): void => {
      const restTitle = removeSubmittedText(draftRef.current.title, submittedTitle)
      const restDetails = removeSubmittedText(draftRef.current.details, submittedDetails)
      setTitle(restTitle)
      setDetails(restDetails)
      setAssignTo('')
      onPosted(restTitle.trim() !== '' || restDetails.trim() !== '')
    }
    if (typed) {
      const create = unsettledCreate.freeze({ key, title: name, description: text !== '' ? text : undefined, to: assignee !== '' ? assignee : undefined })
      typedSubmitter.submit(
        create,
        (p, requestId) => unsettledCreate.track(() => postMeshTask({ requestId, action: 'create', ...p })),
        { onSuccess },
      )
      return
    }
    submitter.submit(
      {
        key,
        status: text !== '' ? `${name} — ${text}` : name,
        notify: notifyIds,
      },
      (p, requestId) => publishMeshBoard({ requestId, key: p.key, status: p.status, notify: [...p.notify] }),
      { onSuccess },
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

  return (
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
      {typed && (
        <label className="team-board-field">
          <span>Assignee</span>
          <select aria-label="Assignee" value={assignee} onChange={(e) => setAssignTo(e.target.value)}>
            <option value="">Nobody yet</option>
            {candidates.map((p) => (
              <option key={p.id} value={p.id}>
                {participantLabel(p)}
              </option>
            ))}
          </select>
        </label>
      )}
      {!typed && candidates.length > 0 && (
        <fieldset className="team-board-notify">
          <legend>Notify</legend>
          {candidates.map((p) => (
            <label key={p.id} className="team-board-notify-option">
              <input type="checkbox" checked={!uncheckedIds.has(p.id)} onChange={() => toggleNotify(p.id)} />
              {participantLabel(p)}
            </label>
          ))}
          {tooManyNotified && (
            <p className="team-board-notify-limit" role="status">
              A task can notify at most {MAX_BOARD_NOTIFY} agents — untick {notifyIds.length - MAX_BOARD_NOTIFY}.
            </p>
          )}
        </fieldset>
      )}
      <div className="team-board-form-actions">
        <span className="team-board-hint">
          {typed ? 'An assigned agent is told about the task at once.' : 'Agents check the board between steps and report under the task.'}
        </span>
        <button type="submit" className="btn btn-send" disabled={pending || title.trim() === '' || tooManyNotified || mode === 'unknown'}>
          {pending ? 'Posting…' : 'Post task'}
        </button>
      </div>
      {error !== null && (
        <div className="team-board-error" role="alert">
          {error}
        </div>
      )}
    </form>
  )
}
