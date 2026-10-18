import type { ReactElement } from 'react'
import type { MeshParticipant, MeshThread } from '../../api/mesh-client'
import { ThreadList } from './ThreadList'

export type RunTeamListStage = { id: string; title: string; status: string }

export type RunTeamListProps = {
  participants: MeshParticipant[]
  threads: MeshThread[]
  // stages — the SAME live Stage list App.tsx already polls (/api/status). Participant.Reachable only tracks
  // whether a stage/task is TERMINAL, not whether it is currently paused (pkg/orchestrator/agentcomm_roster.go)
  // — "a paused participant shows Paused" needs the live stage status this roster doesn't carry on its own.
  stages: RunTeamListStage[]
  selectedParticipantId?: string | null
  selectedThreadId?: string | null
  onSelectParticipant: (participant: MeshParticipant) => void
  onSelectThread: (thread: MeshThread) => void
  onAddRunAgent: () => void
}

function participantLabel(p: MeshParticipant): string {
  if (p.display_name && p.display_name !== '') return p.display_name
  if (p.alias && p.alias !== '') return p.alias
  return p.id
}

function stageTitle(stages: RunTeamListStage[], stageId: string): string {
  return stages.find((s) => s.id === stageId)?.title || stageId
}

// participantStatus — the one status label shown next to a roster row, or null for "nothing to say". A
// paused STAGE (looked up by the participant's own stage_id) takes priority over Reachable, which never
// distinguishes "paused" from "running" (both are non-terminal).
function participantStatus(p: MeshParticipant, stages: RunTeamListStage[]): string | null {
  const stage = p.stage_id !== undefined && p.stage_id !== '' ? stages.find((s) => s.id === p.stage_id) : undefined
  if (stage?.status === 'paused') return 'Paused'
  if (!p.reachable) return p.unreachable_reason && p.unreachable_reason !== '' ? p.unreachable_reason : 'Unreachable'
  return null
}

// RunTeamList — the Team view's roster: participants grouped under "This run" (run-scoped, e.g. a standing
// run agent or the Side agent) and one "Stage · <name>" group per stage with a bound agent/helper, followed
// by "Conversations" (every open/closed thread the operator belongs to). The human operator itself ("user")
// is never listed as a row — a dashboard contact list does not include yourself.
export function RunTeamList({
  participants, threads, stages, selectedParticipantId = null, selectedThreadId = null,
  onSelectParticipant, onSelectThread, onAddRunAgent,
}: RunTeamListProps): ReactElement {
  const runParticipants = participants.filter((p) => p.scope === 'run' && p.kind !== 'user')
  const stageParticipants = participants.filter((p) => p.scope === 'stage' && p.stage_id !== undefined && p.stage_id !== '')

  // stageOrder — distinct stage ids among stage-scoped participants, in first-seen order (stable across polls
  // that didn't change membership — no incidental re-sort).
  const stageOrder: string[] = []
  for (const p of stageParticipants) {
    const id = p.stage_id as string
    if (!stageOrder.includes(id)) stageOrder.push(id)
  }

  function renderRow(p: MeshParticipant): ReactElement {
    const status = participantStatus(p, stages)
    return (
      <li key={p.id}>
        <button
          type="button"
          className={`team-person-row${p.id === selectedParticipantId ? ' active' : ''}`}
          onClick={() => onSelectParticipant(p)}
        >
          <span className="team-person-name">{participantLabel(p)}</span>
          {status !== null && <span className="team-person-status">{status}</span>}
        </button>
      </li>
    )
  }

  return (
    <nav className="team-roster" aria-label="Team roster">
      <section className="team-roster-group">
        <div className="team-roster-group-header">
          <h3>This run</h3>
          <button type="button" className="btn team-add-run-agent" onClick={onAddRunAgent}>
            Add run agent
          </button>
        </div>
        {runParticipants.length === 0 ? (
          <p className="team-roster-empty empty-hint">No run-scoped agents yet</p>
        ) : (
          <ul className="team-roster-list">{runParticipants.map(renderRow)}</ul>
        )}
      </section>

      {stageOrder.map((stageId) => (
        <section key={stageId} className="team-roster-group">
          <h3>{`Stage · ${stageTitle(stages, stageId)}`}</h3>
          <ul className="team-roster-list">
            {stageParticipants.filter((p) => p.stage_id === stageId).map(renderRow)}
          </ul>
        </section>
      ))}

      <section className="team-roster-group">
        <h3>Conversations</h3>
        <ThreadList threads={threads} selectedThreadId={selectedThreadId} onSelect={onSelectThread} />
      </section>
    </nav>
  )
}
