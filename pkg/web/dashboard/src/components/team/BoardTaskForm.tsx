import { useRef, useState, type ReactElement } from 'react'
import { publishMeshBoard, type MeshParticipant } from '../../api/mesh-client'
import { removeSubmittedText, useRetryableSubmit } from '../../hooks/use-retryable-submit'
import { boardNotifyCandidates, participantLabel } from '../../lib/mesh-roster'
import type { RunTeamListStage } from './RunTeamList'

const TASK_PREFIX = 'task/'
const MAX_BOARD_NOTIFY = 16

export type BoardTaskFormProps = {
  // participants / stages — who can be notified: reachable run agents and agents of working stages.
  participants: MeshParticipant[]
  stages: RunTeamListStage[]
  // onPosted — the task was published; the caller refreshes its board (and may close the dialog the form sits
  // in). draftRemains — text typed while the post was in flight is still in the form.
  onPosted: (draftRemains: boolean) => void
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

// BoardTaskForm — the one form that posts a task for the agents, used by Team's Board and the Board tab. The
// operator only names the task and picks who to notify: a new task is always Todo, agents own its status.
export function BoardTaskForm({ participants, stages, onPosted }: BoardTaskFormProps): ReactElement {
  const [title, setTitle] = useState('')
  const [details, setDetails] = useState('')
  // uncheckedIds — candidates the operator opted out of; everyone else (incl. agents that appear later) is notified.
  const [uncheckedIds, setUncheckedIds] = useState<ReadonlySet<string>>(new Set())
  // submitter — a retry of the same task reuses its request id; an edited task is a new one.
  const submitter = useRetryableSubmit<{ key: string; status: string; notify: string[] }>('mesh-board')
  // draftRef — the form's text as last rendered: a post that resolves later reads what was typed meanwhile.
  const draftRef = useRef({ title, details })
  draftRef.current = { title, details }

  const candidates = boardNotifyCandidates(participants, stages)
  const notifyIds = candidates.filter((p) => !uncheckedIds.has(p.id)).map((p) => p.id)
  // A task notifies at most MAX_BOARD_NOTIFY agents (the server's agentcomm.MaxBoardNotify).
  const tooManyNotified = notifyIds.length > MAX_BOARD_NOTIFY

  function post(): void {
    const submittedTitle = title
    const submittedDetails = details
    const name = submittedTitle.trim()
    if (name === '' || tooManyNotified) return
    const text = submittedDetails.trim()
    const key = taskKey(name)
    submitter.submit(
      {
        key,
        status: text !== '' ? `${name} — ${text}` : name,
        notify: notifyIds,
      },
      (p, requestId) => publishMeshBoard({ requestId, key: p.key, status: p.status, notify: [...p.notify] }),
      {
        onSuccess: () => {
          const restTitle = removeSubmittedText(draftRef.current.title, submittedTitle)
          const restDetails = removeSubmittedText(draftRef.current.details, submittedDetails)
          setTitle(restTitle)
          setDetails(restDetails)
          onPosted(restTitle.trim() !== '' || restDetails.trim() !== '')
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
      {candidates.length > 0 && (
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
        <span className="team-board-hint">Agents check the board between steps and report under the task.</span>
        <button type="submit" className="btn btn-send" disabled={submitter.pending || title.trim() === '' || tooManyNotified}>
          {submitter.pending ? 'Posting…' : 'Post task'}
        </button>
      </div>
      {submitter.error !== null && (
        <div className="team-board-error" role="alert">
          {submitter.error}
        </div>
      )}
    </form>
  )
}
