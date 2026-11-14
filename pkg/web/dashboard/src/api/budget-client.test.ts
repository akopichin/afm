import { afterEach, describe, expect, test, vi } from 'vitest'
import { BudgetApiError, fetchBudgetView, fetchBudgets, resumeBudget, updateBudget } from './budget-client'

const view = { scope: 'run', kind: 'run', revision: 1, base_source: 'none', override: { kind: 'none' }, spent: {}, evaluation: {} }

function respond(status: number, body: unknown): Response {
  return { ok: status >= 200 && status < 300, status, json: async () => body } as Response
}

describe('budget-client', () => {
  afterEach(() => vi.restoreAllMocks())

  test('fetchBudgets reads the list and drops entries without a scope', async () => {
    const spy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(respond(200, { budgets: [view, { kind: 'run' }] }))
    const list = await fetchBudgets()
    expect(spy).toHaveBeenCalledWith('/api/budgets', undefined)
    expect(list.map((v) => v.scope)).toEqual(['run'])
  })

  test('fetchBudgetView encodes the scope and returns the yaml block', async () => {
    const spy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      respond(200, { view, yaml: { available: true, file: 'flow.yaml', declared: { tokens: 1000, stop_policy: 'hard' } } }),
    )
    const result = await fetchBudgetView('stage:a b')
    expect(spy).toHaveBeenCalledWith('/api/budgets/view?scope=stage%3Aa%20b', undefined)
    expect(result.yaml).toMatchObject({ available: true, file: 'flow.yaml', declared: { tokens: 1000 } })
  })

  test('updateBudget posts the strict request and returns a saved result with its resume error', async () => {
    const spy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(respond(200, { view, resume_error: { code: 'budget_still_limited', message: 'over' } }))
    const outcome = await updateBudget({ scope: 'run', expectedRevision: 1, override: { kind: 'none' }, saveToYaml: false, resume: true })
    const [url, init] = spy.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/budgets/update')
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body as string)).toEqual({ scope: 'run', expected_revision: 1, override: { kind: 'none' }, resume: true })
    expect(outcome.resumeError).toEqual({ code: 'budget_still_limited', message: 'over' })
  })

  test('errors keep status, code and message; callers branch on the code', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(respond(409, { error: 'budget_revision_conflict', message: 'revision moved' }))
    const err = await updateBudget({ scope: 'run', expectedRevision: 0, override: { kind: 'none' }, saveToYaml: false, resume: false }).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(BudgetApiError)
    expect(err).toMatchObject({ status: 409, code: 'budget_revision_conflict', message: 'revision moved' })
  })

  test('a failure without a JSON body still names its status', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: false, status: 503, json: async () => { throw new Error('no body') } } as unknown as Response)
    const err = await resumeBudget('run', 1).catch((e: unknown) => e)
    expect(err).toMatchObject({ status: 503, code: '' })
    expect((err as Error).message).toContain('503')
  })

  test('resumeBudget posts scope and expected revision', async () => {
    const spy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(respond(200, { view }))
    await resumeBudget('run', 7)
    const [url, init] = spy.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/budgets/resume')
    expect(JSON.parse(init.body as string)).toEqual({ scope: 'run', expected_revision: 7 })
  })
})
