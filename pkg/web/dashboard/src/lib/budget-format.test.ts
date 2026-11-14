import { describe, expect, test } from 'vitest'
import type { BudgetNotice, BudgetView } from '../types/budget'
import {
  blockingParent,
  budgetFigures,
  formatBudgetMoney,
  formatBudgetTokens,
  holdText,
  holdTitle,
  noticeText,
  participantScope,
  remainingText,
  scopeLabel,
  sourceText,
  statusText,
  viewPercent,
} from './budget-format'

const spent = (tokens: number, usd: number, extra: Partial<BudgetView['spent']> = {}): BudgetView['spent'] => ({
  tokens, moneyNanoUsd: Math.round(usd * 1e9), unmetered: 0, unpriced: 0, notExplicit: 0, ...extra,
})

function view(over: Partial<BudgetView> = {}): BudgetView {
  return {
    scope: 'run', kind: 'run', revision: 1, base: null, baseSource: 'none', override: { kind: 'none' }, effective: null,
    spent: spent(0, 0), evaluation: { tokens: '', money: '' }, parentLimits: [], hold: null, draining: false, actions: [], ...over,
  }
}

describe('number formatting', () => {
  test('tokens are k/m like the YAML limits', () => {
    expect(formatBudgetTokens(950)).toBe('950')
    expect(formatBudgetTokens(82_000)).toBe('82k')
    expect(formatBudgetTokens(82_345)).toBe('82.3k')
    expect(formatBudgetTokens(1_500_000)).toBe('1.5m')
  })

  test('money keeps sub-cent tariffs visible', () => {
    expect(formatBudgetMoney(8.2)).toBe('$8.20')
    expect(formatBudgetMoney(0)).toBe('$0.00')
    expect(formatBudgetMoney(0.004)).toBe('$0.004')
  })
})

describe('budgetFigures', () => {
  test('lists every limited metric, the triggered one first', () => {
    const spec = { tokens: 100_000, moneyUsd: 10, stopPolicy: 'hard' as const }
    expect(budgetFigures(spec, spent(82_000, 8.2), { tokens: 'warning', money: 'warning' })).toBe('82k / 100k tokens · $8.20 / $10.00')
    expect(budgetFigures(spec, spent(10_000, 10), { tokens: 'ok', money: 'reached' })).toBe('$10.00 / $10.00 · 10k / 100k tokens')
  })

  test('an inexact figure says so', () => {
    expect(budgetFigures({ tokens: 1000, stopPolicy: 'hard' }, spent(10, 0, { unmetered: 2 }))).toBe('10 / 1k tokens (incomplete)')
    expect(budgetFigures({ moneyUsd: 5, stopPolicy: 'hard' }, spent(10, 1, { unpriced: 1 }))).toBe('$1.00 / $5.00 (incomplete)')
  })

  test('remaining shows left and overrun per metric', () => {
    expect(remainingText({ tokens: 100_000, moneyUsd: 10, stopPolicy: 'hard' }, spent(82_000, 10.4))).toBe('18k tokens left · over by $0.40')
  })
})

describe('scope labels', () => {
  const names = new Map([['task:t1', 'Helper Reviewer · impl']])

  test('known scopes get readable names, unknown ones a fallback', () => {
    expect(scopeLabel('run', names)).toBe('Run')
    expect(scopeLabel('stage:impl', names)).toBe('Stage impl')
    expect(scopeLabel('task:t1', names)).toBe('Helper Reviewer · impl')
    expect(scopeLabel('task:t9', names)).toBe('Helper task t9')
    expect(scopeLabel('task:t9', names, 'run_agent')).toBe('Run agent t9')
  })

  test('participants map to task scopes', () => {
    expect(participantScope({ id: 'helper:t1', kind: 'helper' })).toBe('task:t1')
    expect(participantScope({ id: 'run:t2', kind: 'run_agent' })).toBe('task:t2')
    expect(participantScope({ id: 'stage:impl', kind: 'stage_agent' })).toBeNull()
  })
})

describe('noticeText', () => {
  const base: BudgetNotice = { kind: 'warning', scope: 'stage:impl', limit: { tokens: 100_000, stopPolicy: 'hard' }, spent: spent(82_000, 0), revision: 1 }
  const names = new Map<string, string>()

  test('warning at 80% is a warning row with an edit button', () => {
    expect(noticeText(base, names)).toEqual({ text: 'Budget 80% · Stage impl: 82k / 100k tokens', tone: 'warning', editable: true })
  })

  test('the limit row names the concrete action by policy', () => {
    const limit = (policy: 'hard' | 'medium' | 'info'): BudgetNotice => ({ ...base, kind: 'limit_reached', policy, metric: 'tokens', limit: { tokens: 100_000, stopPolicy: policy } })
    expect(noticeText(limit('hard'), names)).toMatchObject({ tone: 'danger', editable: true })
    expect(noticeText(limit('hard'), names).text).toMatch(/— stopped$/)
    expect(noticeText(limit('medium'), names).text).toMatch(/— finishing active work, nothing new starts$/)
    expect(noticeText(limit('info'), names)).toMatchObject({ tone: 'warning' })
    expect(noticeText(limit('info'), names).text).toMatch(/— limit exceeded, work continues$/)
  })

  test('updated and resumed rows are neutral and have no edit button', () => {
    expect(noticeText({ ...base, kind: 'updated' }, names)).toMatchObject({ tone: 'neutral', editable: false })
    expect(noticeText({ ...base, kind: 'resumed' }, names)).toMatchObject({ tone: 'neutral', editable: false })
  })
})

describe('hold and status texts', () => {
  const names = new Map<string, string>()

  test('banner text follows the hold reason', () => {
    const held = (reason: NonNullable<BudgetView['hold']>['reason'], scope = 'run') => view({ scope: 'stage:impl', hold: { reason, scope } })
    expect(holdTitle(held('limit_reached'))).toBe('Stopped by budget')
    expect(holdText(held('limit_reached'), names)).toBe('The run budget is used up. Change the limit to continue.')
    expect(holdText(held('parent_limit_reached'), names)).toContain('budget of Run is used up')
    expect(holdTitle(held('rates_missing'))).toBe('Budget cannot be checked')
    expect(holdText(held('rates_missing'), names)).toContain('explicit rates')
    expect(holdText(held('accounting_unavailable'), names)).toContain('accounting is unavailable')
  })

  test('team rows get only a short status', () => {
    expect(statusText(undefined)).toBeNull()
    expect(statusText(view({ hold: { reason: 'limit_reached', scope: 'task:t1' } }))).toBe('Stopped by budget')
    const near = view({ effective: { tokens: 100, stopPolicy: 'hard' }, spent: spent(82, 0), evaluation: { tokens: 'warning', money: '' } })
    expect(viewPercent(near)).toBe(82)
    expect(statusText(near)).toBe('Budget 82%')
    expect(statusText(view({ effective: { tokens: 100, stopPolicy: 'hard' }, spent: spent(10, 0), evaluation: { tokens: 'ok', money: '' } }))).toBeNull()
  })
})

describe('blockingParent', () => {
  const parent = (level: 'ok' | 'reached') => ({
    scope: 'run',
    limit: { tokens: 1000, stopPolicy: 'hard' as const },
    spent: { tokens: 10, moneyNanoUsd: 0, unmetered: 0, unpriced: 0, notExplicit: 0 },
    evaluation: { tokens: level, money: '' as const },
  })

  test('an exhausted parent blocks', () => {
    expect(blockingParent(view({ scope: 'stage:impl', parentLimits: [parent('reached')] }))?.scope).toBe('run')
  })

  test('the ancestor hold still blocks after its limit was raised without Continue', () => {
    const held = view({ scope: 'stage:impl', hold: { reason: 'parent_limit_reached', scope: 'run' }, parentLimits: [parent('ok')] })
    expect(blockingParent(held)?.scope).toBe('run')
  })

  test('a hold of the scope itself is not a parent block', () => {
    expect(blockingParent(view({ scope: 'stage:impl', hold: { reason: 'limit_reached', scope: 'stage:impl' }, parentLimits: [parent('ok')] }))).toBeUndefined()
  })
})

describe('sourceText', () => {
  test('a profile default names the profile from the declaration', () => {
    expect(sourceText(view({ baseSource: 'profile', declarationProfile: 'hprof' }))).toBe('Default of profile hprof')
  })

  test('a profile default without an id and other sources keep the generic wording', () => {
    expect(sourceText(view({ baseSource: 'profile' }))).toBe('default of the helper profile')
    expect(sourceText(view({ baseSource: 'declaration', declarationProfile: 'hprof' }))).toBe('declared in the flow')
  })
})
