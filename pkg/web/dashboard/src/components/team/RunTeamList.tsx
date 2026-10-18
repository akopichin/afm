import type { ReactElement } from 'react'
import type { MeshParticipant, MeshThread } from '../../api/mesh-client'
import { participantLabel, participantRole } from '../../lib/mesh-roster'
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

// participantStatus — the status label shown next to a row, or null for "nothing to say". A stage agent
// shows its stage's live status. Any other participant shows "Paused" when its stage is paused (Reachable
// never distinguishes "paused" from "running"), else its unreachable reason.
function participantStatus(p: MeshParticipant, stage: RunTeamListStage | undefined): string | null {
  if (p.kind === 'stage_agent' && stage !== undefined) return stageStatusLabel(stage.status)
  if (stage?.status === 'paused') return 'Paused'
  if (!p.reachable) return p.unreachable_reason && p.unreachable_reason !== '' ? p.unreachable_reason : 'Unreachable'
  return null
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
    const status = participantStatus(p, stage)
    const name = participantLabel(p)
    const role = p.scope === 'run' ? participantRole(p.kind) : null
    return (
      <li key={p.id}>
        <button
          type="button"
          className={`team-person-row${nested ? ' nested' : ''}${p.id === selectedParticipantId ? ' active' : ''}`}
          data-kind={p.kind}
          data-status={p.kind === 'stage_agent' ? stage?.status : undefined}
          onClick={() => onSelectParticipant(p)}
        >
          <span className="team-person-text">
            <span className="team-person-name" title={name}>{name}</span>
            {role !== null && <span className="team-person-role">{role}</span>}
          </span>
          {status !== null && <span className="team-person-status" title={status}>{status}</span>}
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
      <section className="team-roster-group" aria-labelledby="team-roster-run">
        <div className="team-roster-group-header">
          <h3 id="team-roster-run">This run</h3>
          <button type="button" className="btn team-add-run-agent" onClick={onAddRunAgent}>
            Add run agent
          </button>
        </div>
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
        <h3 id="team-roster-conversations">Conversations</h3>
        <ThreadList threads={threads} participants={participants} selectedThreadId={selectedThreadId} onSelect={onSelectThread} />
      </section>
    </nav>
  )
}
