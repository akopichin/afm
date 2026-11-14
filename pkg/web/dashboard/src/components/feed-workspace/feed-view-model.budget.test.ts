import { describe, expect, test } from 'vitest'
import type { AfmEvent } from '../../types'
import { toFeedItems } from './feed-view-model'

const spend = { tokens: 82_000, money_nano_usd: 8_200_000_000 }
const notice = (type: string, payload: Record<string, unknown>, extra: Partial<AfmEvent> = {}): AfmEvent => ({
  type,
  stageId: '',
  timestamp: '2026-11-01T10:00:00Z',
  payload: { scope: 'run', spent: spend, revision: 1, ...payload },
  ...extra,
})

describe('budget notices in the feed', () => {
  test('the 80% warning is a warning row with both figures and an edit button for the scope', () => {
    const [item] = toFeedItems([notice('budget_warning', { kind: 'warning', metric: 'tokens', limit: { tokens: 100_000, money_usd: 10, stop_policy: 'hard' } })])
    expect(item).toMatchObject({
      tone: 'warning',
      kind: 'status',
      actor: 'system',
      text: 'Budget 80% · Run: 82k / 100k tokens · $8.20 / $10.00',
      budgetScope: 'run',
      budgetEditable: true,
    })
  })

  test.each([
    ['hard', 'danger', '— stopped'],
    ['medium', 'warning', '— finishing active work, nothing new starts'],
    ['info', 'warning', '— limit exceeded, work continues'],
  ])('a %s limit names the action', (policy, tone, action) => {
    const [item] = toFeedItems([
      notice('budget_limit_reached', { kind: 'limit_reached', scope: 'stage:impl', metric: 'tokens', policy, limit: { tokens: 80_000, stop_policy: policy } }, { stageId: 'impl' }),
    ])
    expect(item?.tone).toBe(tone)
    expect(item?.text).toContain('Stage impl')
    expect(item?.text.endsWith(action)).toBe(true)
    expect(item).toMatchObject({ budgetScope: 'stage:impl', budgetEditable: true })
  })

  test('updated and resumed rows are neutral and carry no edit button', () => {
    const items = toFeedItems([
      notice('budget_updated', { kind: 'updated', limit: { tokens: 200_000, stop_policy: 'hard' } }),
      notice('budget_resumed', { kind: 'resumed' }),
    ])
    expect(items.map((i) => i.tone)).toEqual(['neutral', 'neutral'])
    expect(items.map((i) => i.budgetEditable)).toEqual([false, false])
  })

  test('scope names come from the supplied labels', () => {
    const names = new Map([['task:t1', 'Helper Reviewer · impl']])
    const [item] = toFeedItems([notice('budget_warning', { kind: 'warning', scope: 'task:t1', limit: { tokens: 100_000, stop_policy: 'hard' } })], undefined, names)
    expect(item?.text).toContain('Helper Reviewer · impl')
  })

  test('the same threshold of the same revision is one row; a new revision is another', () => {
    const warning = (revision: number, receipt: number) => notice('budget_warning', { kind: 'warning', metric: 'tokens', revision, limit: { tokens: 100_000, stop_policy: 'hard' } }, { receipt })
    const items = toFeedItems([warning(1, 1), warning(1, 2), warning(2, 3)])
    expect(items.map((i) => i.key)).toEqual(['budget:warning:run:tokens:1', 'budget:warning:run:tokens:2'])
  })

  test('a budget event without a scope is not rendered', () => {
    expect(toFeedItems([{ type: 'budget_warning', stageId: '', timestamp: '2026-11-01T10:00:00Z', payload: { kind: 'warning' } }])).toEqual([])
  })
})
