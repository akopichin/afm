import { describe, expect, it } from 'vitest'
import { projectSideAgent } from './side-agent-projection'
import type { SideAgentEvent } from '../../types/side-agent'

function ev(seq: number, type: SideAgentEvent['type'], data: unknown, turnId = ''): SideAgentEvent {
  return { schemaVersion: 1, seq, conversationId: 'c1', turnId, timestamp: '2026-10-01T10:00:00Z', type, data } as SideAgentEvent
}
const queued = (seq: number, id: string, stageId = '') =>
  ev(seq, 'message_queued', { messageId: id, clientMessageId: `c-${id}`, message: `text ${id}`, stageId })
const accepted = (seq: number, turnId: string, data: object = {}) =>
  ev(seq, 'turn_accepted', { message: '', clientMessageId: '', ...data }, turnId)

describe('projectSideAgent', () => {
  it('keeps queued messages in arrival order and drops cancelled, failed and claimed ones', () => {
    const p = projectSideAgent([
      queued(1, 'b'), queued(2, 'c'), queued(3, 'd'), queued(4, 'e'),
      ev(5, 'message_cancelled', { messageId: 'c' }),
      ev(6, 'message_failed', { messageId: 'e', code: 'context_limit', message: '' }),
      accepted(7, 't1', { messageIds: ['b'] }),
    ])
    expect(p.queue.map((m) => m.messageId)).toEqual(['d'])
    expect(p.activeTurnId).toBe('t1')
    expect(p.activeSource).toEqual({ kind: 'modal' })
  })

  it('carries text, stage and seq of the queued message', () => {
    expect(projectSideAgent([queued(4, 'b', 's1')]).queue).toEqual([{ messageId: 'b', text: 'text b', stageId: 's1', seq: 4 }])
  })

  it('a new queued message does not replace the active turn, and an old terminal does not clear it', () => {
    const p = projectSideAgent([
      accepted(1, 't1', { message: 'a', clientMessageId: 'c1' }),
      queued(2, 'b'),
      ev(3, 'turn_completed', {}, 't0'),
    ])
    expect(p.activeTurnId).toBe('t1')
    expect(p.queue).toHaveLength(1)
  })

  it('a terminal of the active turn clears it but keeps the queue', () => {
    const p = projectSideAgent([accepted(1, 't1'), queued(2, 'b'), ev(3, 'turn_failed', { code: 'agent_error', message: '' }, 't1')])
    expect(p.activeTurnId).toBe('')
    expect(p.activeSource).toBeNull()
    expect(p.queue).toHaveLength(1)
  })

  it('recognizes a Mesh turn and an unseen acceptance', () => {
    const mesh = accepted(1, 't2', { sourceMessageId: 'm1', sourceSenderName: 'QA' })
    expect(projectSideAgent([mesh]).activeSource).toEqual({ kind: 'mesh', sender: 'QA' })
    const started = projectSideAgent([ev(1, 'turn_started', { command: 'x', contextFormatVersion: 'v3' }, 't9')])
    expect(started).toMatchObject({ activeTurnId: 't9', activeSource: null })
  })
})
