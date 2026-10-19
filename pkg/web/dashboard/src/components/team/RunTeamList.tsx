import type { ReactElement, ReactNode } from 'react'
import type { MeshParticipant, MeshThread } from '../../api/mesh-client'
import { activityLabel, activityPresence, participantLabel, participantRole, stagePresence, type Presence } from '../../lib/mesh-roster'
import { MeshAvatar } from './MeshAvatar'
import { STAGE_STATUS_LABELS, type Stage, type StageStatus } from '../../types'
import { ThreadList } from './ThreadList'

export type RunTeamListStage = { id: string; title: string; status: string }

// teamStagesOf — the live stage list (/api/status, flow order) as the roster's stage rows.
export function teamStagesOf(stages: Stage[]): RunTeamListStage[] {
  return stages.map((s) => ({ id: s.id, title: s.name !== '' ? s.name : s.id, status: s.status }))
}

export type RunTeamListProps = {
  participants: MeshParticipant[]
  // threads/onSelectThread — the "Conversations" group; only the Team tab (no focusStageId) shows it.
  threads?: MeshThread[]
  // stages — the SAME live Stage list App.tsx already polls (/api/status), in flow order. It orders the
  // "Stages" group and gives each stage row its live status: Participant.Reachable only tracks whether a
  // stage/task is TERMINAL (pkg/orchestrator/agentcomm_roster.go), not paused/pending/running.
  stages: RunTeamListStage[]
  selectedParticipantId?: string | null
  selectedThreadId?: string | null
  onSelectParticipant?: (participant: MeshParticipant) => void
  onSelectThread?: (thread: MeshThread) => void
  onAddRunAgent: () => void
  // id — the root element's id (a drawer toggle's aria-controls) and the prefix of its group headings' ids,
  // so a second embedded roster never duplicates the Team tab's ids.
  id?: string
  // drawer — false: never a drawer of its own (an embedding view owns its layout); open is then ignored.
  drawer?: boolean
  // open — on a narrow screen the roster is a drawer; open slides it in (ignored on wide screens).
  open?: boolean
  // focusStageId — the roster as one stage's "people you can talk to" (the stage Feed's side panel): groups
  // "This stage" (its agent and helpers), "Run agents", "Other stages" in flow order; each row shows its
  // command and read-only guarantee from the roster; finished participants are disabled. No conversations.
  focusStageId?: string
  // onPick — a click on a person in focus mode (the feed addresses them). Without it the rows are disabled:
  // there is nowhere to write from right now, and pickHint says why.
  onPick?: (participant: MeshParticipant) => void
  pickHint?: string
}

function stageStatusLabel(status: string): string {
  return STAGE_STATUS_LABELS[status as StageStatus] ?? status
}

// participantStatus — the status label shown under a row's name, or null for "nothing to say". A stage
// agent shows its stage's live status. Any other participant shows "Paused" when its stage is paused
// (Reachable never distinguishes "paused" from "running"), else its unreachable reason.
function participantStatus(p: MeshParticipant, stage: RunTeamListStage | undefined): string | null {
  // "Awaiting colleague" is the rail's wording; the narrow roster row says it shorter.
  if (p.kind === 'stage_agent' && stage !== undefined) return stage.status === 'awaiting_peer' ? 'Waiting' : stageStatusLabel(stage.status)
  if (stage?.status === 'paused') return 'Paused'
  const activity = activityLabel(p.activity)
  if (activity !== null) return activity
  if (!p.reachable) return p.unreachable_reason && p.unreachable_reason !== '' ? p.unreachable_reason : 'Unreachable'
  return null
}

// participantPresence — the row's presence dot: a stage agent follows its stage's status, anyone else is
// "working" while reachable and "idle" once its stage/task is terminal.
function participantPresence(p: MeshParticipant, stage: RunTeamListStage | undefined): Presence {
  if (stage !== undefined && (p.kind === 'stage_agent' || stage.status === 'paused')) return stagePresence(stage.status)
  return activityPresence(p.activity, p.reachable)
}

// isFinished — the participant's stage or task has ended. The server marks a task unreachable only once it
// is terminal; a stage can also be unreachable for another reason (a script stage has no agent).
function isFinished(p: MeshParticipant, stage: RunTeamListStage | undefined): boolean {
  if (p.kind === 'stage_agent') return stage?.status === 'done' || stage?.status === 'failed'
  return true
}

// focusStatus — a row's status in the stage people panel: whether a message can reach this person now.
// Finished participants cannot be written to; anyone else the server marks unreachable (a script stage has
// no agent) shows the server's reason; a stage not started yet reads it when it starts.
function focusStatus(p: MeshParticipant, stage: RunTeamListStage | undefined): string | null {
  if (!p.reachable) {
    if (isFinished(p, stage)) return "Finished — can't write"
    const reason = p.unreachable_reason ?? ''
    return reason === '' ? "Can't write" : reason.charAt(0).toUpperCase() + reason.slice(1)
  }
  if (p.kind === 'stage_agent' && stage?.status === 'pending') return 'Not started — reads it when it starts'
  return participantStatus(p, stage)
}

// sandboxLabel — the read-only guarantee the server froze into a run agent's or helper's task: an OS sandbox,
// or none (only the container or the prompt limits it). Stage agents write the project and carry none.
// The badge shows the server's current guarantee; when the task was admitted under a different one (a host
// run resumed in a container), the tooltip says so.
const SANDBOX_TEXT: Record<string, { text: string; title: string }> = {
  os: { text: 'read-only', title: 'Read-only, enforced by an OS sandbox' },
  none: { text: 'no sandbox', title: 'No OS read-only sandbox: only the container or the prompt limits it' },
}

function sandboxLabel(p: MeshParticipant): { text: string; title: string } | null {
  const current = SANDBOX_TEXT[p.readonly_enforcement ?? '']
  if (current === undefined) return null
  const admitted = SANDBOX_TEXT[p.admitted_readonly_enforcement ?? '']
  if (admitted === undefined || p.admitted_readonly_enforcement === p.readonly_enforcement) return current
  return { text: current.text, title: `${current.title}. When it was added: ${admitted.title.charAt(0).toLowerCase()}${admitted.title.slice(1)}` }
}

// stageOrder — stage ids in flow order; stages the roster knows but /api/status doesn't (yet) go last, in
// the order the roster lists them.
function stageOrder(stageParticipants: MeshParticipant[], stages: RunTeamListStage[]): string[] {
  const order = stages.map((s) => s.id).filter((id) => stageParticipants.some((p) => p.stage_id === id))
  for (const p of stageParticipants) {
    const id = p.stage_id as string
    if (!order.includes(id)) order.push(id)
  }
  return order
}

// RunTeamList — the Team view's roster: "This run" (run-scoped participants: run agents and the Side agent),
// "Stages" (one row per stage agent in flow order, with that stage's helpers nested right under it) and
// "Conversations" (every thread the operator belongs to). The human operator itself ("user") is never listed.
// With focusStageId it is one stage's people panel instead (see RunTeamListProps.focusStageId).
export function RunTeamList({
  participants, threads = [], stages, selectedParticipantId = null, selectedThreadId = null,
  onSelectParticipant, onSelectThread, onAddRunAgent, id = 'team-roster', drawer = true, open = false,
  focusStageId, onPick, pickHint,
}: RunTeamListProps): ReactElement {
  const focused = focusStageId !== undefined
  const runParticipants = participants.filter((p) => p.scope === 'run' && p.kind !== 'user')
  const stageParticipants = participants.filter((p) => p.scope === 'stage' && p.stage_id !== undefined && p.stage_id !== '')

  function renderRow(p: MeshParticipant, nested = false): ReactElement {
    const stage = stages.find((s) => s.id === p.stage_id)
    const name = participantLabel(p)
    const status = focused ? focusStatus(p, stage) : participantStatus(p, stage)
    const detail = status ?? (p.scope === 'run' ? participantRole(p.kind) : null)
    const sandbox = focused ? sandboxLabel(p) : null
    const disabled = focused && (onPick === undefined || !p.reachable)
    return (
      <li key={p.id}>
        <button
          type="button"
          className={`team-person-row${nested ? ' nested' : ''}${p.id === selectedParticipantId ? ' active' : ''}`}
          data-kind={p.kind}
          data-status={p.kind === 'stage_agent' ? stage?.status : undefined}
          disabled={disabled}
          onClick={() => (focused ? onPick?.(p) : onSelectParticipant?.(p))}
        >
          <MeshAvatar name={name} kind={p.kind} />
          <span className="team-person-text">
            {focused ? (
              <span className="team-person-title">
                <span className="team-person-name" title={name}>{name}</span>
                {p.command !== undefined && p.command !== '' && (
                  <span className="team-person-chip" title={`Command: ${p.command}`}>{p.command}</span>
                )}
              </span>
            ) : (
              <span className="team-person-name" title={name}>{name}</span>
            )}
            <span className="team-person-detail" title={detail ?? undefined}>
              {p.alias !== undefined && p.alias !== '' && <span className="team-person-handle">@{p.alias}</span>}
              {detail !== null && <span className="team-person-status">{detail}</span>}
              {sandbox !== null && (
                <span className="team-person-sandbox" data-enforcement={p.readonly_enforcement} title={sandbox.title}>
                  {sandbox.text}
                </span>
              )}
            </span>
          </span>
          <span className="team-presence" data-presence={participantPresence(p, stage)} aria-hidden="true" />
        </button>
      </li>
    )
  }

  // A stage agent first, then its helpers; a helper whose stage agent isn't in the roster still shows up.
  function renderStage(stageId: string): ReactElement[] {
    const members = stageParticipants.filter((p) => p.stage_id === stageId)
    const agents = members.filter((p) => p.kind === 'stage_agent')
    const helpers = members.filter((p) => p.kind !== 'stage_agent')
    return [...agents.map((p) => renderRow(p)), ...helpers.map((p) => renderRow(p, agents.length > 0))]
  }

  const rootClass = `team-roster${drawer ? '' : ' team-roster-embedded'}${drawer && open ? ' open' : ''}`

  if (focused) {
    const thisStageRows = renderStage(focusStageId)
    const otherStages = stageOrder(stageParticipants, stages).filter((s) => s !== focusStageId)
    const otherCount = stageParticipants.filter((p) => p.stage_id !== focusStageId).length
    return (
      <nav id={id} className={rootClass} aria-label="People">
        <div className="team-roster-top">
          <h2 className="team-roster-title">People</h2>
          {pickHint !== undefined && <p className="team-roster-hint">{pickHint}</p>}
        </div>
        <RosterGroup id={`${id}-stage`} label="This stage" count={thisStageRows.length}>
          <ul className="team-roster-list">{thisStageRows}</ul>
        </RosterGroup>
        <RosterGroup id={`${id}-run`} label="Run agents" count={runParticipants.length}>
          <ul className="team-roster-list">{runParticipants.map((p) => renderRow(p))}</ul>
        </RosterGroup>
        {otherStages.length > 0 && (
          <RosterGroup id={`${id}-other`} label="Other stages" count={otherCount}>
            <ul className="team-roster-list">{otherStages.flatMap(renderStage)}</ul>
          </RosterGroup>
        )}
        <button type="button" className="btn team-add-run-agent team-add-agent-bottom" onClick={onAddRunAgent}>
          <span aria-hidden="true">+</span> Add agent
        </button>
      </nav>
    )
  }

  return (
    <nav id={id} className={rootClass} aria-label="Team roster">
      <div className="team-roster-top">
        <h2 className="team-roster-title">Run team</h2>
        <button type="button" className="btn btn-send team-add-run-agent" onClick={onAddRunAgent}>
          <span aria-hidden="true">+</span> Add run agent
        </button>
      </div>

      <section className="team-roster-group" aria-labelledby={`${id}-run`}>
        <h3 id={`${id}-run`}>This run</h3>
        {runParticipants.length === 0 ? (
          <p className="team-roster-empty empty-hint">No run-scoped agents yet</p>
        ) : (
          <ul className="team-roster-list">{runParticipants.map((p) => renderRow(p))}</ul>
        )}
      </section>

      {stageParticipants.length > 0 && (
        <section className="team-roster-group" aria-labelledby={`${id}-stages`}>
          <h3 id={`${id}-stages`}>Stages</h3>
          <ul className="team-roster-list">{stageOrder(stageParticipants, stages).flatMap(renderStage)}</ul>
        </section>
      )}

      <section className="team-roster-group" aria-labelledby={`${id}-conversations`}>
        <h3 id={`${id}-conversations`}>
          Conversations
          {threads.length > 0 && <span className="team-roster-count" aria-hidden="true">{threads.length}</span>}
        </h3>
        <ThreadList threads={threads} participants={participants} selectedThreadId={selectedThreadId} onSelect={(t) => onSelectThread?.(t)} />
      </section>
    </nav>
  )
}

// RosterGroup — one titled group of the people panel, with how many people it lists.
function RosterGroup({ id, label, count, children }: { id: string; label: string; count: number; children: ReactNode }): ReactElement {
  return (
    <section className="team-roster-group" aria-labelledby={id}>
      <h3 id={id}>
        {label}
        <span className="team-roster-count" aria-hidden="true">{count}</span>
      </h3>
      {count === 0 ? <p className="team-roster-empty empty-hint">No one yet</p> : children}
    </section>
  )
}
