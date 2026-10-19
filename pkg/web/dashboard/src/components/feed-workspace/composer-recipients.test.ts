import { describe, expect, it } from 'vitest'
import { composerRecipients, recipientFence, typedMentionHandles } from './composer-recipients'
import type { MeshParticipant } from '../../api/mesh-client'

const impl: MeshParticipant = { id: 'stage:impl', kind: 'stage_agent', scope: 'stage', stage_id: 'impl', alias: 'impl', display_name: 'Backend', reachable: true }
const docs: MeshParticipant = { id: 'stage:docs', kind: 'stage_agent', scope: 'stage', stage_id: 'docs', alias: 'docs', reachable: true }
const qa: MeshParticipant = { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA', reachable: true }
const done: MeshParticipant = {
  id: 'run:t-2', kind: 'run_agent', scope: 'run', alias: 'old', reachable: false, unreachable_reason: 'task t-2 has reached a terminal status',
}
const user: MeshParticipant = { id: 'user', kind: 'user', scope: 'run', alias: 'user', reachable: true }
const participants = [user, impl, docs, qa, done]
const running = [{ id: 'impl', status: 'running' }, { id: 'docs', status: 'running' }]

describe('typedMentionHandles', () => {
  it('finds whole @handle tokens, without trailing punctuation, and ignores e-mail addresses', () => {
    expect(typedMentionHandles('@qa, and @run:t-9. mail me at a@b.c (@docs)')).toEqual(['qa', 'run:t-9'])
    expect(typedMentionHandles('@qa\n@docs!')).toEqual(['qa', 'docs'])
  })
})

describe('composerRecipients', () => {
  it('without anyone named, the message goes to the stage agent by default', () => {
    const r = composerRecipients({ participants, stages: running, stageId: 'impl', picked: [], draft: 'hello' })
    expect(r.recipients).toEqual([{ key: 'stage:impl', handle: 'impl', participantId: 'stage:impl', source: 'default', problem: null }])
    expect(r.defaultProblem).toBeNull()
  })

  it('picked and typed recipients together, each person once, picked first', () => {
    const r = composerRecipients({ participants, stages: running, stageId: 'impl', picked: ['run:t-1'], draft: '@docs and @QA please' })
    expect(r.recipients.map((x) => [x.participantId, x.source])).toEqual([
      ['run:t-1', 'picked'],
      ['stage:docs', 'typed'],
    ])
  })

  it('an unknown or unreachable name is an error with its reason — never dropped or redirected', () => {
    const r = composerRecipients({ participants, stages: running, stageId: 'impl', picked: [], draft: '@nobody @old hi' })
    expect(r.recipients.map((x) => x.problem)).toEqual([
      'No one is called @nobody in this run',
      "@old can't receive messages: task t-2 has reached a terminal status",
    ])
  })

  it('a stage agent that waits for its helpers reads only a Revise; it is not the default then', () => {
    const waiting = [{ id: 'impl', status: 'awaiting_assistants' }]
    const typed = composerRecipients({ participants, stages: waiting, stageId: 'impl', picked: [], draft: '@impl hi' })
    expect(typed.recipients[0]?.problem).toMatch(/reads a note only as a Revise/)
    const implicit = composerRecipients({ participants, stages: waiting, stageId: 'impl', picked: [], draft: 'hi' })
    expect(implicit.recipients).toEqual([])
    expect(implicit.defaultProblem).toMatch(/reads a note only as a Revise/)
  })

  it('a finished stage agent is no default: a recipient is required', () => {
    const finished = { ...impl, reachable: false, unreachable_reason: 'stage impl has reached a terminal status' }
    const r = composerRecipients({ participants: [user, finished, qa], stages: [{ id: 'impl', status: 'done' }], stageId: 'impl', picked: [], draft: 'hi' })
    expect(r.recipients).toEqual([])
    expect(r.defaultProblem).toMatch(/terminal status/)
  })

  it('an alias two people share is ambiguous; @user is you', () => {
    const twin: MeshParticipant = { ...qa, id: 'run:t-3' }
    const r = composerRecipients({ participants: [...participants, twin], stages: running, stageId: 'impl', picked: [], draft: '@qa @user' })
    expect(r.recipients.map((x) => x.problem)).toEqual(['@qa matches several people — pick one from the list', "@user is you — you can't write to yourself"])
  })
})

describe('recipientFence', () => {
  it('is the recipient set (in any order) plus the thread and the reply target', () => {
    expect(recipientFence(['b', 'a'], 'mt-1', 'mm-2')).toBe(recipientFence(['a', 'b'], 'mt-1', 'mm-2'))
    expect(recipientFence(['a'], '', '')).not.toBe(recipientFence(['a', 'b'], '', ''))
    expect(recipientFence(['a'], 'mt-1', '')).not.toBe(recipientFence(['a'], 'mt-1', 'mm-2'))
  })
})
