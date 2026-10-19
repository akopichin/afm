import { describe, expect, it } from 'vitest'
import { buildMeshMentionGroups } from './mesh-recipients'
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

  it('resolves a participant mention target to its own scope, not the composer\'s stage', () => {
    const groups = buildMeshMentionGroups([runAgent], [], 's1')
    const item = groups.find((g) => g.label === 'This run')?.items[0]
    expect(item?.target).toEqual({ kind: 'participant', id: 'run:qa', scope: 'run' })
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
