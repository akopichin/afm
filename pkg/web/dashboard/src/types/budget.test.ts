import { describe, expect, test } from 'vitest'
import {
  budgetEventScope,
  budgetUpdateToWire,
  isRunAgentBudgetNotice,
  isRunBudgetNotice,
  parseBudgetNotice,
  parseBudgetUpdateOutcome,
  parseBudgetView,
} from './budget'

const wireView = {
  scope: 'stage:impl',
  kind: 'stage',
  revision: 3,
  base: { tokens: 100000, stop_policy: 'hard' },
  base_source: 'declaration',
  override: { kind: 'limit', spec: { tokens: 200000, money_usd: 10, stop_policy: 'medium' } },
  effective: { tokens: 200000, money_usd: 10, stop_policy: 'medium' },
  spent: { tokens: 82000, money_nano_usd: 8_200_000_000, records: 4, unpriced: 1 },
  evaluation: { tokens: 'ok', money: 'warning' },
  parent_limits: [{ scope: 'run', limit: { money_usd: 50, stop_policy: 'hard' }, spent: { tokens: 1, money_nano_usd: 5 }, evaluation: { money: 'ok' } }],
  hold: { reason: 'parent_limit_reached', scope: 'run', metric: 'money' },
  draining: true,
  actions: ['update', 'resume', 'bogus'],
}

describe('parseBudgetView', () => {
  test('maps the contracts/budget JSON into the client shape', () => {
    const view = parseBudgetView(wireView)
    expect(view).toMatchObject({
      scope: 'stage:impl',
      kind: 'stage',
      revision: 3,
      base: { tokens: 100000, stopPolicy: 'hard' },
      override: { kind: 'limit', spec: { tokens: 200000, moneyUsd: 10, stopPolicy: 'medium' } },
      effective: { tokens: 200000, moneyUsd: 10, stopPolicy: 'medium' },
      spent: { tokens: 82000, moneyNanoUsd: 8_200_000_000, unpriced: 1, unmetered: 0 },
      evaluation: { tokens: 'ok', money: 'warning' },
      hold: { reason: 'parent_limit_reached', scope: 'run', metric: 'money' },
      draining: true,
      actions: ['update', 'resume'],
    })
    expect(view?.parentLimits[0]).toMatchObject({ scope: 'run', limit: { moneyUsd: 50 } })
  })

  test('a scope without limit has null base/effective and no hold', () => {
    const view = parseBudgetView({ scope: 'run', kind: 'run', revision: 0, base_source: 'none', override: { kind: 'none' }, spent: {}, evaluation: {} })
    expect(view).toMatchObject({ base: null, effective: null, hold: null, override: { kind: 'none' }, actions: [] })
  })

  test('a response without scope is not a view', () => {
    expect(parseBudgetView({ kind: 'run' })).toBeNull()
    expect(parseBudgetView(null)).toBeNull()
  })
})

describe('parseBudgetNotice', () => {
  test('reads the payload; the kind falls back to the event type', () => {
    const notice = parseBudgetNotice('budget_limit_reached', {
      scope: 'run', metric: 'tokens', limit: { tokens: 1000, stop_policy: 'medium' }, spent: { tokens: 1000, money_nano_usd: 0 }, policy: 'medium', revision: 2,
    })
    expect(notice).toMatchObject({ kind: 'limit_reached', scope: 'run', metric: 'tokens', policy: 'medium', revision: 2, limit: { tokens: 1000, stopPolicy: 'medium' } })
    expect(parseBudgetNotice('budget_resumed', { scope: 'run', spent: {} })?.kind).toBe('resumed')
  })

  test('rejects other events and payloads without a scope', () => {
    expect(parseBudgetNotice('agent_action', { scope: 'run' })).toBeNull()
    expect(parseBudgetNotice('budget_warning', { kind: 'warning' })).toBeNull()
  })
})

describe('budget event routing', () => {
  const event = (type: string, stageId: string, scope: string) => ({ type, stageId, payload: { scope } })

  test('a run notice has no stage and scope run; a run agent notice has no stage and a task scope', () => {
    expect(isRunBudgetNotice(event('budget_warning', '', 'run'))).toBe(true)
    expect(isRunBudgetNotice(event('budget_warning', 'impl', 'stage:impl'))).toBe(false)
    expect(isRunBudgetNotice(event('agent_action', '', 'run'))).toBe(false)
    expect(isRunAgentBudgetNotice(event('budget_limit_reached', '', 'task:t-1'))).toBe(true)
    expect(isRunAgentBudgetNotice(event('budget_limit_reached', 'impl', 'task:t-1'))).toBe(false)
    expect(budgetEventScope({ type: 'budget_updated', payload: 'oops' })).toBeNull()
  })
})

describe('wire conversion', () => {
  test('update request uses the contracts field names and omits false flags and empty metrics', () => {
    expect(
      budgetUpdateToWire({ scope: 'run', expectedRevision: 4, override: { kind: 'limit', spec: { tokens: 500000, stopPolicy: 'info' } }, saveToYaml: false, resume: true }),
    ).toEqual({ scope: 'run', expected_revision: 4, override: { kind: 'limit', spec: { tokens: 500000, stop_policy: 'info' } }, resume: true })
    expect(budgetUpdateToWire({ scope: 'run', expectedRevision: 0, override: { kind: 'unlimited' }, saveToYaml: true, resume: false })).toEqual({
      scope: 'run', expected_revision: 0, override: { kind: 'unlimited' }, save_to_yaml: true,
    })
  })

  test('update outcome carries the export result and a resume error next to the view', () => {
    const outcome = parseBudgetUpdateOutcome({
      view: wireView,
      export: { status: 'failed', path: 'flow.yaml', error: 'file changed' },
      resume_error: { code: 'budget_still_limited', message: 'still over' },
    })
    expect(outcome).toMatchObject({ export: { status: 'failed', path: 'flow.yaml', error: 'file changed' }, resumeError: { code: 'budget_still_limited', message: 'still over' } })
    expect(parseBudgetUpdateOutcome({ view: { kind: 'run' } })).toBeNull()
  })
})
