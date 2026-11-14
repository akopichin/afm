import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { AfmEvent } from '../../types'
import { lastBudgetEventKey, useBudgets } from './use-budgets'

const wire = (scope: string, revision = 1) => ({ scope, kind: scope === 'run' ? 'run' : 'stage', revision, base_source: 'none', override: { kind: 'none' }, spent: {}, evaluation: {} })

function mockBudgets(list: () => unknown[]) {
  return vi.spyOn(globalThis, 'fetch').mockImplementation(async () => ({ ok: true, status: 200, json: async () => ({ budgets: list() }) }) as Response)
}

afterEach(() => vi.restoreAllMocks())

describe('useBudgets', () => {
  it('makes no request while the capability is off', async () => {
    const spy = mockBudgets(() => [wire('run')])
    const { result } = renderHook(() => useBudgets({ enabled: false, runId: 'r', tick: 1, eventKey: '' }))
    await act(async () => {})
    expect(spy).not.toHaveBeenCalled()
    expect(result.current.views).toEqual([])
  })

  it('reloads on each status tick and on a new budget event key', async () => {
    let list = [wire('run', 1)]
    const spy = mockBudgets(() => list)
    const { result, rerender } = renderHook((p: { tick: number; eventKey: string }) => useBudgets({ enabled: true, runId: 'r', ...p }), { initialProps: { tick: 1, eventKey: '' } })
    await waitFor(() => expect(result.current.byScope.get('run')?.revision).toBe(1))
    list = [wire('run', 2)]
    rerender({ tick: 2, eventKey: '' })
    await waitFor(() => expect(result.current.byScope.get('run')?.revision).toBe(2))
    list = [wire('run', 3)]
    rerender({ tick: 2, eventKey: 'budget_updated:7' })
    await waitFor(() => expect(result.current.byScope.get('run')?.revision).toBe(3))
    expect(spy).toHaveBeenCalledTimes(3)
  })

  it('keeps the last list when a poll fails', async () => {
    let fail = false
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () => (fail ? ({ ok: false, status: 503, json: async () => ({}) } as Response) : ({ ok: true, status: 200, json: async () => ({ budgets: [wire('run')] }) } as Response)))
    const { result, rerender } = renderHook((p: { tick: number }) => useBudgets({ enabled: true, runId: 'r', eventKey: '', ...p }), { initialProps: { tick: 1 } })
    await waitFor(() => expect(result.current.views).toHaveLength(1))
    fail = true
    rerender({ tick: 2 })
    await act(async () => {})
    expect(result.current.views).toHaveLength(1)
  })

  it('a view applied from a response is shown at once and wins over an older in-flight poll', async () => {
    let release: () => void = () => {}
    vi.spyOn(globalThis, 'fetch').mockImplementation(
      () => new Promise((resolve) => {
        release = () => resolve({ ok: true, status: 200, json: async () => ({ budgets: [wire('run', 1)] }) } as Response)
      }),
    )
    const { result } = renderHook(() => useBudgets({ enabled: true, runId: 'r', tick: 1, eventKey: '' }))
    act(() => result.current.apply({ ...(result.current.views[0] ?? ({} as never)), scope: 'run', kind: 'run', revision: 9 } as never))
    expect(result.current.byScope.get('run')?.revision).toBe(9)
    await act(async () => release())
    expect(result.current.byScope.get('run')?.revision).toBe(9)
  })

  it('does not show the previous run list for a new run', async () => {
    mockBudgets(() => [wire('run')])
    const { result, rerender } = renderHook((p: { runId: string }) => useBudgets({ enabled: true, tick: 1, eventKey: '', ...p }), { initialProps: { runId: 'a' } })
    await waitFor(() => expect(result.current.views).toHaveLength(1))
    vi.spyOn(globalThis, 'fetch').mockImplementation(() => new Promise(() => {}))
    rerender({ runId: 'b' })
    expect(result.current.views).toEqual([])
  })
})

describe('lastBudgetEventKey', () => {
  const ev = (type: string, receipt: number): AfmEvent => ({ type, stageId: '', payload: {}, timestamp: 't', receipt })

  it('is empty without budget events and changes with each new one', () => {
    expect(lastBudgetEventKey([ev('agent_action', 1)])).toBe('')
    const first = lastBudgetEventKey([ev('budget_warning', 1), ev('agent_action', 2)])
    const second = lastBudgetEventKey([ev('budget_warning', 1), ev('agent_action', 2), ev('budget_updated', 3)])
    expect(first).not.toBe('')
    expect(second).not.toBe(first)
  })
})
