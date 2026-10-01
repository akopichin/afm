import { describe, it, expect } from 'vitest'
import { toSideAgentFeedItems, sideAgentFeedGroups } from './side-agent-feed-model'
import type { SideAgentEvent } from '../../types/side-agent'

const ev = (over: Partial<SideAgentEvent> & Pick<SideAgentEvent, 'type' | 'data'>): SideAgentEvent =>
  ({
    schemaVersion: 1,
    seq: 1,
    conversationId: 'c1',
    turnId: 't1',
    timestamp: '2026-10-01T10:00:00Z',
    ...over,
  }) as SideAgentEvent

describe('toSideAgentFeedItems', () => {
  it('maps turn_accepted to a right-side user message (plain, not markdown)', () => {
    const items = toSideAgentFeedItems([
      ev({ type: 'turn_accepted', seq: 1, data: { message: '**hi**', clientMessageId: 'm1' } }),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ actor: 'user', side: 'right', kind: 'message', text: '**hi**' })
    expect(items[0]?.markdown).toBeUndefined()
  })

  it('maps assistant_text to a left-side agent markdown message', () => {
    const items = toSideAgentFeedItems([
      ev({ type: 'assistant_text', seq: 2, data: { text: '## heading' } }),
    ])
    expect(items[0]).toMatchObject({ actor: 'agent', side: 'left', kind: 'message', text: '## heading', markdown: true })
  })

  it('maps tool_action to a compact mono tool row', () => {
    const items = toSideAgentFeedItems([
      ev({ type: 'tool_action', seq: 3, data: { tool: 'Bash', detail: 'ls', logRef: 'r' } }),
    ])
    expect(items[0]).toMatchObject({ actor: 'agent', kind: 'tool', text: 'Bash: ls', mono: true })
  })

  it('maps tool_action with empty detail without a trailing colon', () => {
    const items = toSideAgentFeedItems([
      ev({ type: 'tool_action', seq: 3, data: { tool: 'Bash', detail: '', logRef: '' } }),
    ])
    expect(items[0]?.text).toBe('Bash')
  })

  it('maps turn_failed to a danger status with a human-readable code label', () => {
    const items = toSideAgentFeedItems([
      ev({ type: 'turn_failed', seq: 4, data: { code: 'context_limit', message: '' } }),
    ])
    expect(items[0]).toMatchObject({ actor: 'system', tone: 'danger', kind: 'status' })
    expect(items[0]?.text).toBe('Conversation too long for one request')
  })

  it('appends a non-empty turn_failed message after the code label', () => {
    const items = toSideAgentFeedItems([
      ev({ type: 'turn_failed', seq: 4, data: { code: 'agent_error', message: 'boom' } }),
    ])
    expect(items[0]?.text).toBe('The agent failed to answer: boom')
  })

  it('maps turn_interrupted to a warning status', () => {
    const items = toSideAgentFeedItems([
      ev({ type: 'turn_interrupted', seq: 5, data: { reason: 'user' } }),
    ])
    expect(items[0]).toMatchObject({ actor: 'system', tone: 'warning', kind: 'status', text: 'Interrupted: user' })
  })

  it('drops turn_started and turn_completed (no visible row)', () => {
    const items = toSideAgentFeedItems([
      ev({ type: 'turn_started', seq: 6, data: { command: 'claude', contextFormatVersion: 'v1' } }),
      ev({ type: 'turn_completed', seq: 7, data: {} }),
    ])
    expect(items).toHaveLength(0)
  })

  it('keys rows by seq (stable across replay), empty stageId', () => {
    const items = toSideAgentFeedItems([
      ev({ type: 'turn_accepted', seq: 11, data: { message: 'q', clientMessageId: 'm1' } }),
      ev({ type: 'assistant_text', seq: 12, data: { text: 'a' } }),
    ])
    expect(items.map((i) => i.key)).toEqual(['seq:11', 'seq:12'])
    expect(items.every((i) => i.stageId === '')).toBe(true)
  })
})

describe('sideAgentFeedGroups', () => {
  it('merges consecutive same-actor rows into one group and splits on actor change', () => {
    const groups = sideAgentFeedGroups([
      ev({ type: 'turn_accepted', seq: 1, data: { message: 'q', clientMessageId: 'm1' } }),
      ev({ type: 'assistant_text', seq: 2, data: { text: 'a' } }),
      ev({ type: 'tool_action', seq: 3, data: { tool: 'Bash', detail: 'ls', logRef: '' } }),
    ])
    expect(groups).toHaveLength(2)
    expect(groups[0]).toMatchObject({ actor: 'user', side: 'right' })
    expect(groups[0]?.items).toHaveLength(1)
    expect(groups[1]).toMatchObject({ actor: 'agent', side: 'left' })
    expect(groups[1]?.items).toHaveLength(2)
  })
})
