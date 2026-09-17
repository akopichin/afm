import { describe, it, expect } from 'vitest'
import type { AfmEvent } from '../../types'
import { toFeedItems, groupFeedItems, formatEventGap } from './feed-view-model'

const ev = (type: string, payload: unknown, stageId: string, timestamp: string, seq?: number): AfmEvent =>
  ({ type, payload, stageId, timestamp, ...(seq !== undefined ? { seq } : {}) }) as AfmEvent

describe('toFeedItems — mapping (parity with the old feed formatting)', () => {
  it('maps known event types to text, actor and tone', () => {
    const items = toFeedItems([
      ev('stage_status_changed', 'running', 's1', '2026-07-10T10:00:00Z'),
      ev('agent_action', { tool: 'read_file', detail: 'src/x.ts' }, 's1', '2026-07-10T10:00:01Z'),
      ev('agent_completed', 'implementation', 's1', '2026-07-10T10:00:02Z'),
      ev('approved', {}, 's1', '2026-07-10T10:00:04Z'),
      ev('hook_failed', { hook: 'post', error: 'boom' }, 's1', '2026-07-10T10:00:05Z'),
      ev('custom_unknown', null, '', '2026-07-10T10:00:06Z'),
    ])
    expect(items[0]).toMatchObject({ text: '→ running', actor: 'system', side: 'left' })
    expect(items[1]).toMatchObject({ text: 'read_file: src/x.ts', actor: 'agent', side: 'left', mono: true, kind: 'tool' })
    expect(items[2]).toMatchObject({ text: 'agent implementation completed', tone: 'success', kind: 'success' })
    expect(items[3]).toMatchObject({ text: 'approved', actor: 'user', side: 'right', tone: 'success' })
    expect(items[4]).toMatchObject({ text: 'post-hook failed: boom', tone: 'danger' })
    // Неизвестный тип — падать нельзя, показываем сам тип.
    expect(items[5]).toMatchObject({ text: 'custom_unknown', actor: 'system' })
  })

  it('renders an agent text action as clean prose (no "text:" prefix, not mono)', () => {
    const items = toFeedItems([ev('agent_action', { tool: 'text', detail: 'I initialized the build.' }, 's1', '2026-07-10T10:00:00Z')])
    expect(items[0]).toMatchObject({ text: 'I initialized the build.', kind: 'message', mono: false, actor: 'agent' })
  })

  it('flags ONLY the agent narrative (agent_action tool=text) as markdown', () => {
    const items = toFeedItems([
      ev('agent_action', { tool: 'text', detail: '## H' }, 's1', '2026-07-10T10:00:00Z'),
      ev('agent_action', { tool: 'Read', detail: 'x.ts' }, 's1', '2026-07-10T10:00:01Z'),
      ev('agent_note', { text: '**literal**' }, 's1', '2026-07-10T10:00:02Z'),
      ev('dialog_answer', { phase: 'planning', id: 'q1', title: 'reply' }, 's1', '2026-07-10T10:00:03Z'),
      ev('dialog_question', { phase: 'planning', id: 'q1', title: 'ask' }, 's1', '2026-07-10T10:00:04Z'),
    ])
    expect(items[0]?.markdown).toBe(true)
    // Обычный tool, заметка пользователя и оба dialog-элемента — plain (markdown не выставлен).
    expect(items[1]?.markdown).toBeUndefined()
    expect(items[2]?.markdown).toBeUndefined()
    expect(items[3]?.markdown).toBeUndefined()
    expect(items[4]?.markdown).toBeUndefined()
  })

  it('maps status transitions to tones', () => {
    const items = toFeedItems([
      ev('stage_status_changed', 'done', 's1', '2026-07-10T10:00:00Z'),
      ev('stage_status_changed', 'failed', 's1', '2026-07-10T10:00:01Z'),
      ev('stage_status_changed', 'awaiting_approval', 's1', '2026-07-10T10:00:02Z'),
    ])
    expect(items[0]?.tone).toBe('success')
    expect(items[1]?.tone).toBe('danger')
    expect(items[2]?.tone).toBe('accent')
  })

  it('computes a static gap-from-previous duration per item', () => {
    const items = toFeedItems([
      ev('stage_status_changed', 'running', 's1', '2026-07-10T10:00:00.000Z'),
      ev('agent_action', { tool: 'a' }, 's1', '2026-07-10T10:00:05.000Z'),
      ev('agent_action', { tool: 'b' }, 's1', '2026-07-10T10:01:35.000Z'),
    ])
    expect(items[0]?.gap).toBe('—')
    expect(items[1]?.gap).toBe('5s')
    expect(items[2]?.gap).toBe('1m')
  })

  it('gives same-timestamp same-type events unique stable keys', () => {
    const items = toFeedItems([
      ev('agent_action', { tool: 'a' }, 's1', '2026-07-10T10:00:00Z'),
      ev('agent_action', { tool: 'b' }, 's1', '2026-07-10T10:00:00Z'),
    ])
    expect(items[0]?.key).not.toBe(items[1]?.key)
  })

  it('prefers seq for the key when present', () => {
    const items = toFeedItems([ev('approved', {}, 's1', '2026-07-10T10:00:00Z', 42)])
    expect(items[0]?.key).toBe('seq:42')
  })
})

describe('dialog_question/dialog_answer — реальный текст диалога, ask_user/user_answered вытеснены', () => {
  it('dialog_question maps to a navigable agent item carrying phase/id from the payload', () => {
    const items = toFeedItems([
      ev('dialog_question', { phase: 'planning', id: 'q1', title: 'Which approach?' }, 's1', '2026-07-10T10:00:00Z'),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({
      actor: 'agent',
      side: 'left',
      kind: 'dialog',
      text: 'Which approach?',
      navigable: true,
      phase: 'planning',
      id: 'q1',
    })
  })

  it('dialog_answer maps to a navigable user item on the right side', () => {
    const items = toFeedItems([
      ev('dialog_answer', { phase: 'planning', id: 'q1', title: 'Option A' }, 's1', '2026-07-10T10:00:00Z'),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({
      actor: 'user',
      side: 'right',
      kind: 'message',
      text: 'Option A',
      navigable: true,
      phase: 'planning',
      id: 'q1',
    })
  })

  it('ask_user and user_answered produce no feed item — replaced by dialog_question/dialog_answer', () => {
    const items = toFeedItems([
      ev('ask_user', { phase: 'planning', id: 'q1' }, 's1', '2026-07-10T10:00:00Z'),
      ev('user_answered', { phase: 'planning', id: 'q1' }, 's1', '2026-07-10T10:00:01Z'),
    ])
    expect(items).toHaveLength(0)
  })

  it('falls back to a generic label when title is missing or empty, and stays navigable', () => {
    const items = toFeedItems([
      ev('dialog_question', { phase: 'planning', id: 'q1' }, 's1', '2026-07-10T10:00:00Z'),
      ev('dialog_answer', { phase: 'planning', id: 'q1', title: '' }, 's1', '2026-07-10T10:00:01Z'),
    ])
    expect(items[0]).toMatchObject({ text: 'question', navigable: true })
    expect(items[1]).toMatchObject({ text: 'reply', navigable: true })
  })

  it('grouping preserves phase/id/navigable on each item', () => {
    const groups = groupFeedItems(
      toFeedItems([
        ev('dialog_question', { phase: 'planning', id: 'q1', title: 'Q1?' }, 's1', '2026-07-10T10:00:00Z'),
      ]),
    )
    expect(groups[0]?.items[0]).toMatchObject({ phase: 'planning', id: 'q1', navigable: true })
  })
})

describe('agent_note — заметка агенту справа своим текстом, но НЕ кликабельна', () => {
  it('maps to a right-side user message carrying the payload text, non-navigable', () => {
    const items = toFeedItems([
      ev('agent_note', { id: 42, text: 'Учти что API возвращает 500' }, 's1', '2026-07-10T10:00:00Z'),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({
      actor: 'user',
      side: 'right',
      kind: 'message',
      text: 'Учти что API возвращает 500',
      mono: false,
    })
    // navigable опущен → FeedGroupView отрендерит обычный <div>, не кнопку.
    expect(items[0]?.navigable).toBeUndefined()
  })
})

describe('groupFeedItems', () => {
  it('merges consecutive same-side, same-stage items into one group', () => {
    const groups = groupFeedItems(
      toFeedItems([
        ev('stage_status_changed', 'running', 's1', '2026-07-10T10:00:00Z'),
        ev('agent_action', { tool: 'a' }, 's1', '2026-07-10T10:00:01Z'),
        ev('dialog_answer', { phase: 'planning', id: 'q1', title: 'yes' }, 's1', '2026-07-10T10:00:02Z'),
        ev('agent_action', { tool: 'b' }, 's1', '2026-07-10T10:00:03Z'),
      ]),
    )
    // system+agent для s1 — оба left, но actor различается (system vs agent) →
    // это два разных пузыря; затем user (right); затем снова agent (left).
    expect(groups.map((g) => `${g.side}:${g.actor}`)).toEqual([
      'left:system',
      'left:agent',
      'right:user',
      'left:agent',
    ])
    expect(groups[3]?.items).toHaveLength(1)
  })

  it('breaks a group when the stage changes', () => {
    const groups = groupFeedItems(
      toFeedItems([
        ev('agent_action', { tool: 'a' }, 's1', '2026-07-10T10:00:00Z'),
        ev('agent_action', { tool: 'b' }, 's2', '2026-07-10T10:00:01Z'),
      ]),
    )
    expect(groups).toHaveLength(2)
    expect(groups[0]?.stageId).toBe('s1')
    expect(groups[1]?.stageId).toBe('s2')
  })
})

describe('formatEventGap', () => {
  it('formats seconds/minutes/hours/days and em-dash for invalid', () => {
    expect(formatEventGap(5000, 0)).toBe('5s')
    expect(formatEventGap(90_000, 0)).toBe('1m')
    expect(formatEventGap(7_200_000, 0)).toBe('2h')
    expect(formatEventGap(172_800_000, 0)).toBe('2d')
    expect(formatEventGap(NaN, 0)).toBe('—')
  })
})
