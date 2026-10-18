import type { ReactElement } from 'react'
import type { MeshParticipant, MeshThread } from '../../api/mesh-client'
import { mentionHandle, participantLabel, participantRole, stagePresence, type Presence } from '../../lib/mesh-roster'
import { MeshAvatar } from './MeshAvatar'
import { STAGE_STATUS_LABELS, type StageStatus } from '../../types'
import { ThreadList } from './ThreadList'

export type RunTeamListStage = { id: string; title: string; status: string }

export type RunTeamListProps = {
  participants: MeshParticipant[]
  threads: MeshThread[]
  // stages — the SAME live Stage list App.tsx already polls (/api/status), in flow order. It orders the
  // "Stages" group and gives each stage row its live status: Participant.Reachable only tracks whether a
  // stage/task is TERMINAL (pkg/orchestrator/agentcomm_roster.go), not paused/pending/running.
  stages: RunTeamListStage[]
  selectedParticipantId?: string | null
  selectedThreadId?: string | null
  onSelectParticipant: (participant: MeshParticipant) => void
  onSelectThread: (thread: MeshThread) => void
  onAddRunAgent: () => void
}

function stageStatusLabel(status: string): string {
  return STAGE_STATUS_LABELS[status as StageStatus] ?? status
}

// participantStatus — the status label shown under a row's name, or null for "nothing to say". A stage
// agent shows its stage's live status. Any other participant shows "Paused" when its stage is paused
// (Reachable never distinguishes "paused" from "running"), else its unreachable reason.
function participantStatus(p: MeshParticipant, stage: RunTeamListStage | undefined): string | null {
  if (p.kind === 'stage_agent' && stage !== undefined) return stageStatusLabel(stage.status)
  if (stage?.status === 'paused') return 'Paused'
  if (!p.reachable) return p.unreachable_reason && p.unreachable_reason !== '' ? p.unreachable_reason : 'Unreachable'
  return null
}

// participantPresence — the row's presence dot: a stage agent follows its stage's status, anyone else is
// "working" while reachable and "idle" once its stage/task is terminal.
function participantPresence(p: MeshParticipant, stage: RunTeamListStage | undefined): Presence {
  if (stage !== undefined && (p.kind === 'stage_agent' || stage.status === 'paused')) return stagePresence(stage.status)
  return p.reachable ? 'working' : 'idle'
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
export function RunTeamList({
  participants, threads, stages, selectedParticipantId = null, selectedThreadId = null,
  onSelectParticipant, onSelectThread, onAddRunAgent,
}: RunTeamListProps): ReactElement {
  const runParticipants = participants.filter((p) => p.scope === 'run' && p.kind !== 'user')
  const stageParticipants = participants.filter((p) => p.scope === 'stage' && p.stage_id !== undefined && p.stage_id !== '')

  function renderRow(p: MeshParticipant, nested = false): ReactElement {
    const stage = stages.find((s) => s.id === p.stage_id)
    const name = participantLabel(p)
    const detail = participantStatus(p, stage) ?? (p.scope === 'run' ? participantRole(p.kind) : null)
    return (
      <li key={p.id}>
        <button
          type="button"
          className={`team-person-row${nested ? ' nested' : ''}${p.id === selectedParticipantId ? ' active' : ''}`}
          data-kind={p.kind}
          data-status={p.kind === 'stage_agent' ? stage?.status : undefined}
          onClick={() => onSelectParticipant(p)}
        >
          <MeshAvatar name={name} kind={p.kind} />
          <span className="team-person-text">
            <span className="team-person-name" title={name}>{name}</span>
            <span className="team-person-detail" title={detail ?? undefined}>
              <span className="team-person-handle">@{mentionHandle(p)}</span>
              {detail !== null && <span className="team-person-status">{detail}</span>}
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

  return (
    <nav className="team-roster" aria-label="Team roster">
      <div className="team-roster-top">
        <h2 className="team-roster-title">Run team</h2>
        <button type="button" className="btn btn-send team-add-run-agent" onClick={onAddRunAgent}>
          <span aria-hidden="true">+</span> Add run agent
        </button>
      </div>

      <section className="team-roster-group" aria-labelledby="team-roster-run">
        <h3 id="team-roster-run">This run</h3>
        {runParticipants.length === 0 ? (
          <p className="team-roster-empty empty-hint">No run-scoped agents yet</p>
        ) : (
          <ul className="team-roster-list">{runParticipants.map((p) => renderRow(p))}</ul>
        )}
      </section>

      {stageParticipants.length > 0 && (
        <section className="team-roster-group" aria-labelledby="team-roster-stages">
          <h3 id="team-roster-stages">Stages</h3>
          <ul className="team-roster-list">{stageOrder(stageParticipants, stages).flatMap(renderStage)}</ul>
        </section>
      )}

      <section className="team-roster-group" aria-labelledby="team-roster-conversations">
        <h3 id="team-roster-conversations">
          Conversations
          {threads.length > 0 && <span className="team-roster-count" aria-hidden="true">{threads.length}</span>}
        </h3>
        <ThreadList threads={threads} participants={participants} selectedThreadId={selectedThreadId} onSelect={onSelectThread} />
      </section>
    </nav>
  )
}
