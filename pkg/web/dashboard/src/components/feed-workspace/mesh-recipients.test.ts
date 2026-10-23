import { describe, expect, it } from 'vitest'
import { buildLoopMentionGroups, buildMeshMentionGroups, isLoopTarget } from './mesh-recipients'
import { findExistingDirectThread, participantLabel } from '../../lib/mesh-roster'
import type { MeshParticipant, MeshThread } from '../../api/mesh-client'
import type { AssistantProfile } from '../../types/assistant-profile'

const stageAgent: MeshParticipant = { id: 'stage:s1', kind: 'stage_agent', scope: 'stage', stage_id: 's1', alias: 's1', display_name: 'Backend', reachable: true }
const otherStageAgent: MeshParticipant = { id: 'stage:s2', kind: 'stage_agent', scope: 'stage', stage_id: 's2', alias: 's2', display_name: 'Frontend', reachable: true }
const helperOnS1: MeshParticipant = { id: 'helper:t1', kind: 'helper', scope: 'stage', stage_id: 's1', alias: 't1', display_name: 'QA helper', reachable: true }
const runAgent: MeshParticipant = { id: 'run:qa', kind: 'run_agent', scope: 'run', display_name: 'QA reviewer', reachable: true }
const user: MeshParticipant = { id: 'user', kind: 'user', scope: 'run', display_name: 'You', reachable: true }

const profiles: AssistantProfile[] = [
  { id: 'frontendDeveloper', title: 'Frontend reviewer', command: 'claude', available: true, unavailableReason: '' },
  { id: 'ghost', title: 'Ghost', command: 'claude', available: false, unavailableReason: 'no verifiable read-only policy' },
]

describe('participantLabel', () => {
  it('prefers display_name', () => {
    expect(participantLabel(stageAgent)).toBe('Backend')
  })
  it('falls back to alias when display_name is empty', () => {
    expect(participantLabel({ ...stageAgent, display_name: '' })).toBe('s1')
  })
  it('falls back to id when alias and display_name are both empty', () => {
    expect(participantLabel({ ...stageAgent, display_name: '', alias: '' })).toBe('stage:s1')
  })
})

describe('buildMeshMentionGroups', () => {
  it('keeps the reachable Assistant selectable in the run mention group', () => {
    const side: MeshParticipant = { id: 'side:r1', kind: 'side_agent', scope: 'run', alias: 'assistant', display_name: 'claude', reachable: true }
    const item = buildMeshMentionGroups([side], [], 's1')[0]?.items[0]
    expect(item).toMatchObject({ id: 'side:r1', handle: 'assistant', unavailable: false, target: { kind: 'participant', id: 'side:r1' } })
  })
  it('keeps the bound @assistant and a stage on @assistant-2 as two distinct mention targets', () => {
    const side: MeshParticipant = { id: 'side:r1', kind: 'side_agent', scope: 'run', alias: 'assistant', display_name: 'claude', reachable: true }
    const stage: MeshParticipant = { id: 'stage:assistant', kind: 'stage_agent', scope: 'stage', stage_id: 'assistant', alias: 'assistant-2', display_name: 'Smoke', reachable: true }
    const groups = buildMeshMentionGroups([side, stage], [], 'assistant')
    expect(groups.flatMap((g) => g.items.map((i) => [i.id, i.handle]))).toEqual([['side:r1', 'assistant'], ['stage:assistant', 'assistant-2']])
  })
  it('groups run-scoped participants under "This run", excluding the human', () => {
    const groups = buildMeshMentionGroups([user, runAgent, stageAgent], [], 's1')
    const thisRun = groups.find((g) => g.label === 'This run')
    expect(thisRun?.items.map((i) => i.id)).toEqual(['run:qa'])
  })

  it('groups only the CURRENT stage\'s participants under "This stage"', () => {
    const groups = buildMeshMentionGroups([stageAgent, otherStageAgent, helperOnS1], [], 's1')
    const thisStage = groups.find((g) => g.label === 'This stage')
    expect(thisStage?.items.map((i) => i.id).sort()).toEqual(['helper:t1', 'stage:s1'])
  })

  it('lists only available profiles under "Attach to stage", as attach targets', () => {
    const groups = buildMeshMentionGroups([], profiles, 's1')
    const attach = groups.find((g) => g.label === 'Attach to stage')
    expect(attach?.items).toEqual([
      { id: 'frontendDeveloper', title: 'Frontend reviewer', target: { kind: 'profile', profileId: 'frontendDeveloper' } },
    ])
  })

  it('gives two run agents that share an alias distinct handles (their ids), so a mention names one of them', () => {
    const qa1: MeshParticipant = { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', reachable: true }
    const qa2: MeshParticipant = { id: 'run:t-2', kind: 'run_agent', scope: 'run', alias: 'qa', reachable: true }
    const thisRun = buildMeshMentionGroups([qa1, qa2], [], 's1')[0]?.items ?? []
    expect(thisRun.map((i) => i.handle)).toEqual(['run:t-1', 'run:t-2'])
  })

  it('marks an unreachable participant unavailable with its reason', () => {
    const terminal: MeshParticipant = { ...stageAgent, reachable: false, unreachable_reason: 'stage s1 has reached a terminal status' }
    const groups = buildMeshMentionGroups([terminal], [], 's1')
    const item = groups.find((g) => g.label === 'This stage')?.items[0]
    expect(item?.unavailable).toBe(true)
    expect(item?.unavailableReason).toBe('stage s1 has reached a terminal status')
  })

  it('a script stage the server marks unreachable is offered unavailable', () => {
    const script: MeshParticipant = { ...stageAgent, reachable: false, unreachable_reason: 'script stage: no agent reads messages' }
    const item = buildMeshMentionGroups([script], [], 's1').find((g) => g.label === 'This stage')?.items[0]
    expect(item?.unavailable).toBe(true)
    expect(item?.unavailableReason).toBe('script stage: no agent reads messages')
  })

  it('resolves a participant mention target to its own scope, not the composer\'s stage', () => {
    const groups = buildMeshMentionGroups([runAgent], [], 's1')
    const item = groups.find((g) => g.label === 'This run')?.items[0]
    expect(item?.target).toEqual({ kind: 'participant', id: 'run:qa', scope: 'run' })
  })
})

describe('loop targets', () => {
  const helperOnS2: MeshParticipant = { id: 'helper:t2', kind: 'helper', scope: 'stage', stage_id: 's2', alias: 't2', display_name: 'Other helper', reachable: true }
  const side: MeshParticipant = { id: 'side:r1', kind: 'side_agent', scope: 'run', alias: 'assistant', display_name: 'claude', reachable: true }
  const gone: MeshParticipant = { ...runAgent, id: 'run:old', reachable: false, unreachable_reason: 'task has reached a terminal status' }
  const roster = [user, runAgent, stageAgent, otherStageAgent, helperOnS1, helperOnS2, side, gone]

  it('isLoopTarget accepts run agents and the stage\'s own helpers only', () => {
    expect(roster.filter((p) => isLoopTarget(p, 's1')).map((p) => p.id)).toEqual(['run:qa', 'helper:t1', 'run:old'])
  })

  it('offers only reachable run agents and this stage\'s helpers — no stage agent, Side agent, human or attach profile', () => {
    const groups = buildLoopMentionGroups(roster, 's1')
    expect(groups.map((g) => [g.label, g.items.map((i) => i.id)])).toEqual([
      ['This run', ['run:qa']],
      ['This stage', ['helper:t1']],
    ])
    expect(groups.flatMap((g) => g.items).every((i) => i.target.kind === 'participant')).toBe(true)
  })

  it('offers nothing when the run has no eligible agent', () => {
    expect(buildLoopMentionGroups([user, stageAgent, side], 's1').flatMap((g) => g.items)).toEqual([])
  })
})

describe('findExistingDirectThread', () => {
  const open2: MeshThread = { id: 'mt-1', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
  const closed2: MeshThread = { id: 'mt-2', scope: 'run', members: ['user', 'run:qa'], status: 'closed' }
  const group3: MeshThread = { id: 'mt-3', scope: 'run', members: ['user', 'run:qa', 'run:dev'], status: 'open' }

  it('finds an open, exactly-two-member thread naming the participant', () => {
    expect(findExistingDirectThread([open2], 'run:qa')).toEqual(open2)
  })

  it('ignores a closed thread', () => {
    expect(findExistingDirectThread([closed2], 'run:qa')).toBeNull()
  })

  it('does not match a larger group thread that happens to include the participant', () => {
    expect(findExistingDirectThread([group3], 'run:qa')).toBeNull()
  })

  it('returns null when no thread exists at all', () => {
    expect(findExistingDirectThread([], 'run:qa')).toBeNull()
  })
})
