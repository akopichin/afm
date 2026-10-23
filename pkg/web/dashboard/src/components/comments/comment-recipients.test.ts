import { describe, expect, it } from 'vitest'
import type { MeshParticipant } from '../../api/mesh-client'
import { ASSISTANT_BUSY, CHOOSE_AGENT, commentRecipients, resolveCommentRecipient, type CommentRecipientsInput } from './comment-recipients'

const impl: MeshParticipant = { id: 'stage:impl', kind: 'stage_agent', scope: 'stage', stage_id: 'impl', alias: 'impl', display_name: 'Backend', reachable: true }
const docs: MeshParticipant = { id: 'stage:docs', kind: 'stage_agent', scope: 'stage', stage_id: 'docs', alias: 'docs', reachable: true }
const script: MeshParticipant = { id: 'stage:hold', kind: 'stage_agent', scope: 'stage', stage_id: 'hold', reachable: false, unreachable_reason: 'script stage' }
const helper: MeshParticipant = { id: 'task:h1', kind: 'helper', scope: 'stage', stage_id: 'impl', alias: 'reviewer', reachable: true }
const qa: MeshParticipant = { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA', reachable: true }
const gone: MeshParticipant = { id: 'run:t-2', kind: 'run_agent', scope: 'run', alias: 'old', reachable: false, unreachable_reason: 'finished' }
const user: MeshParticipant = { id: 'user', kind: 'user', scope: 'run', reachable: true }
const rosterSide: MeshParticipant = { id: 'side:r1', kind: 'side_agent', scope: 'run', alias: 'side', reachable: true }

const running = [
  { id: 'impl', status: 'running' },
  { id: 'docs', status: 'running' },
  { id: 'hold', status: 'running' },
]

function input(over: Partial<CommentRecipientsInput> = {}): CommentRecipientsInput {
  return {
    runId: 'r1',
    participants: [user, rosterSide, impl, docs, script, helper, qa, gone],
    stages: running,
    assistant: { available: true, busy: false },
    ...over,
  }
}

describe('commentRecipients', () => {
  it('lists the Assistant, stage agents, helpers and run agents with their roles', () => {
    expect(commentRecipients(input()).map((r) => [r.id, r.label, r.role, r.route])).toEqual([
      ['side:r1', 'Assistant', 'Assistant', 'assistant'],
      ['stage:impl', 'Backend', 'Stage executor', 'mesh'],
      ['stage:docs', 'docs', 'Stage executor', 'mesh'],
      ['task:h1', 'reviewer', 'Helper', 'mesh'],
      ['run:t-1', 'QA', 'Run agent', 'mesh'],
    ])
  })

  it('freezes the origin stage only for a stage agent', () => {
    const byId = new Map(commentRecipients(input()).map((r) => [r.id, r.originStage]))
    expect(byId.get('stage:impl')).toBe('impl')
    expect(byId.get('task:h1')).toBe('')
    expect(byId.get('run:t-1')).toBe('')
    expect(byId.get('side:r1')).toBe('')
  })

  it('leaves out the user, script stages and unreachable agents', () => {
    const ids = commentRecipients(input()).map((r) => r.id)
    expect(ids).not.toContain('user')
    expect(ids).not.toContain('stage:hold')
    expect(ids).not.toContain('run:t-2')
  })

  it('leaves out a finished stage agent and one waiting for its helpers', () => {
    const stages = [
      { id: 'impl', status: 'awaiting_assistants' },
      { id: 'docs', status: 'done' },
    ]
    const ids = commentRecipients(input({ stages })).map((r) => r.id)
    expect(ids).not.toContain('stage:impl')
    expect(ids).not.toContain('stage:docs')
    expect(commentRecipients(input({ stages: [{ id: 'impl', status: 'failed' }, { id: 'docs', status: 'running' }] })).map((r) => r.id)).not.toContain('stage:impl')
  })

  it('works without the roster: only the Assistant, with the run-scoped id', () => {
    expect(commentRecipients(input({ participants: [] })).map((r) => r.id)).toEqual(['side:r1'])
  })

  it('drops the Assistant when the side-agent capability is off or the run is unknown, keeping the roster', () => {
    expect(commentRecipients(input({ assistant: { available: false, busy: false } })).map((r) => r.id)).not.toContain('side:r1')
    expect(commentRecipients(input({ runId: '' })).map((r) => r.id)).not.toContain('side:r1')
  })

  it('does not list the roster side agent twice', () => {
    expect(commentRecipients(input()).filter((r) => r.id === 'side:r1')).toHaveLength(1)
  })

  it('marks the Assistant busy while it answers', () => {
    const assistant = commentRecipients(input({ assistant: { available: true, busy: true } })).find((r) => r.id === 'side:r1')
    expect(assistant?.busy).toBe(true)
    expect(commentRecipients(input()).every((r) => !r.busy)).toBe(true)
  })
})

describe('resolveCommentRecipient', () => {
  const several = commentRecipients(input())
  const assistantOnly = commentRecipients(input({ participants: [] }))

  it('uses the comment own recipient', () => {
    expect(resolveCommentRecipient(several, 'run:t-1', '').recipient?.id).toBe('run:t-1')
    expect(resolveCommentRecipient(several, 'run:t-1', '').problem).toBeNull()
  })

  it('falls back to the last recipient used in the run', () => {
    expect(resolveCommentRecipient(several, '', 'task:h1').recipient?.id).toBe('task:h1')
  })

  it('selects the only available recipient', () => {
    expect(resolveCommentRecipient(assistantOnly, '', '').recipient?.id).toBe('side:r1')
  })

  it('asks to choose when several are available and none was used before', () => {
    expect(resolveCommentRecipient(several, '', '')).toEqual({ recipient: null, problem: CHOOSE_AGENT })
  })

  it('explains an empty list', () => {
    const r = resolveCommentRecipient([], '', '')
    expect(r.recipient).toBeNull()
    expect(r.problem).toMatch(/no agent/i)
  })

  it('explains a chosen recipient that left the roster and keeps asking for a choice', () => {
    const r = resolveCommentRecipient(several, 'run:t-9', '')
    expect(r.recipient).toBeNull()
    expect(r.problem).toMatch(/no longer available/i)
  })

  it('ignores a gone last recipient and chooses again', () => {
    expect(resolveCommentRecipient(several, '', 'run:t-9')).toEqual({ recipient: null, problem: CHOOSE_AGENT })
  })

  it('resolves a busy Assistant with the reason Send must wait', () => {
    const busy = commentRecipients(input({ participants: [], assistant: { available: true, busy: true } }))
    const r = resolveCommentRecipient(busy, '', '')
    expect(r.recipient?.id).toBe('side:r1')
    expect(r.problem).toBe(ASSISTANT_BUSY)
  })
})
