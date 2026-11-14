import { describe, expect, test } from 'vitest'
import type { BudgetView } from '../../types/budget'
import { draftFromView, isDraftClean, parseTokensInput, parseUsdInput, resolveDraft, tokensToInput } from './budget-draft'

const view = (override: BudgetView['override'], base: BudgetView['base'] = null): BudgetView => ({
  scope: 'run', kind: 'run', revision: 1, base, baseSource: base === null ? 'none' : 'declaration', override, effective: override.spec ?? base,
  spent: { tokens: 0, moneyNanoUsd: 0, unmetered: 0, unpriced: 0, notExplicit: 0 }, evaluation: { tokens: '', money: '' },
  parentLimits: [], hold: null, draining: false, actions: ['update'],
})

describe('parseTokensInput', () => {
  test.each([
    ['500k', 500_000],
    ['1.5m', 1_500_000],
    ['120000', 120_000],
    ['0.25K', 250],
    [' 2M ', 2_000_000],
  ])('%s → %d', (text, value) => {
    expect(parseTokensInput(text)).toEqual({ value })
  })

  test('empty means no limit; garbage and zero are errors', () => {
    expect(parseTokensInput('  ')).toEqual({ value: undefined })
    expect(parseTokensInput('abc').error).toBeDefined()
    expect(parseTokensInput('0').error).toBeDefined()
    expect(parseTokensInput('1.2345k').error).toBeDefined()
    expect(parseTokensInput('-5k').error).toBeDefined()
    expect(parseTokensInput('99999999999999999999').error).toBeDefined()
  })

  test('round-trips through the field text', () => {
    expect(tokensToInput(500_000)).toBe('500k')
    expect(tokensToInput(2_000_000)).toBe('2m')
    expect(tokensToInput(1234)).toBe('1234')
    expect(parseTokensInput(tokensToInput(1_250_000)).value).toBe(1_250_000)
  })
})

describe('parseUsdInput', () => {
  test('accepts plain and dollar-marked amounts', () => {
    expect(parseUsdInput('10')).toEqual({ value: 10 })
    expect(parseUsdInput('$12.50')).toEqual({ value: 12.5 })
    expect(parseUsdInput('7$')).toEqual({ value: 7 })
    expect(parseUsdInput('')).toEqual({ value: undefined })
  })

  test('rejects non-positive, non-numeric and absurd values', () => {
    expect(parseUsdInput('0').error).toBeDefined()
    expect(parseUsdInput('ten').error).toBeDefined()
    expect(parseUsdInput('1e3').error).toBeDefined()
    expect(parseUsdInput('2000000000').error).toBeDefined()
  })
})

describe('resolveDraft', () => {
  const custom = { mode: 'custom' as const, tokens: '', usd: '', policy: 'medium' as const }

  test('declared and unlimited need no input', () => {
    expect(resolveDraft({ ...custom, mode: 'declared' }).override).toEqual({ kind: 'none' })
    expect(resolveDraft({ ...custom, mode: 'unlimited' }).override).toEqual({ kind: 'unlimited' })
  })

  test('a custom limit needs at least one metric and carries the policy', () => {
    expect(resolveDraft(custom).errors.form).toMatch(/at least one/)
    expect(resolveDraft({ ...custom, tokens: '500k', usd: '10' }).override).toEqual({
      kind: 'limit', spec: { tokens: 500_000, moneyUsd: 10, stopPolicy: 'medium' },
    })
    expect(resolveDraft({ ...custom, usd: '5' }).override).toEqual({ kind: 'limit', spec: { moneyUsd: 5, stopPolicy: 'medium' } })
  })

  test('field errors are reported per field and block the override', () => {
    const result = resolveDraft({ ...custom, tokens: 'x', usd: 'y' })
    expect(result.override).toBeNull()
    expect(result.errors.tokens).toBeDefined()
    expect(result.errors.usd).toBeDefined()
  })
})

describe('draft against the saved view', () => {
  test('the draft starts equal to the current override', () => {
    const v = view({ kind: 'limit', spec: { tokens: 200_000, moneyUsd: 10, stopPolicy: 'info' } }, { tokens: 100_000, stopPolicy: 'hard' })
    const draft = draftFromView(v)
    expect(draft).toEqual({ mode: 'custom', tokens: '200k', usd: '10', policy: 'info' })
    expect(isDraftClean(draft, v)).toBe(true)
    expect(isDraftClean({ ...draft, tokens: '300k' }, v)).toBe(false)
  })

  test('no override means the declared choice, seeded from the base', () => {
    const v = view({ kind: 'none' }, { tokens: 100_000, stopPolicy: 'medium' })
    const draft = draftFromView(v)
    expect(draft).toMatchObject({ mode: 'declared', tokens: '100k', policy: 'medium' })
    expect(isDraftClean(draft, v)).toBe(true)
    expect(isDraftClean({ ...draft, mode: 'unlimited' }, v)).toBe(false)
  })

  test('an invalid draft is never clean', () => {
    const v = view({ kind: 'none' })
    expect(isDraftClean({ mode: 'custom', tokens: 'bad', usd: '', policy: 'hard' }, v)).toBe(false)
  })
})
