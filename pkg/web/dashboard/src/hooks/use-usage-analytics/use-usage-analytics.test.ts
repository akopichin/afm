import { act, renderHook, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import fullBreakdown from '../../../../../server/testdata/usage-analytics/full-breakdown.json'
import fullJobs from '../../../../../server/testdata/usage-analytics/full-jobs.json'
import fullCharts from '../../../../../server/testdata/usage-analytics/full-charts.json'
import noMoneyCharts from '../../../../../server/testdata/usage-analytics/no-money-charts.json'
import { UsageAnalyticsRequestError, type UsageRequest } from '../../api/usage-analytics-client'
import { parseUsageAnalytics, type UsageAnalytics, type UsageAnalyticsStatus } from '../../types/usage-analytics'
import { useUsageAnalytics } from './use-usage-analytics'

const getUsageAnalytics = vi.fn<(request: UsageRequest, signal?: AbortSignal) => Promise<UsageAnalytics>>()

vi.mock('../../api/usage-analytics-client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/usage-analytics-client')>()),
  getUsageAnalytics: (request: UsageRequest, signal?: AbortSignal) => getUsageAnalytics(request, signal),
}))

type Call = { request: UsageRequest; signal: AbortSignal | undefined; resolve: (data: UsageAnalytics) => void; reject: (error: unknown) => void }
let calls: Call[] = []

// answerFor builds the response the server would give to the request, from the golden fixtures.
function answerFor(
  request: UsageRequest,
  extra: Record<string, unknown> = {},
  base: object = { breakdown: fullBreakdown, jobs: fullJobs, charts: fullCharts }[request.view],
): UsageAnalytics {
  const query = {
    view: request.view, group: request.group, scope: request.scope, stage_id: request.stageId || undefined,
    agent: request.agent || undefined, model: request.model || undefined,
    sort: request.sort ?? 'time', dir: request.dir ?? 'desc', limit: request.limit ?? undefined,
  }
  const data = parseUsageAnalytics({ ...base, query, ...extra })
  if (data === null) throw new Error('bad fixture')
  return data
}

type Props = { open: boolean; status: Pick<UsageAnalyticsStatus, 'revision' | 'mode'> | null; runEpoch: number; statusAvailable?: boolean }

const full: Props['status'] = { revision: 'r1', mode: 'full' }

beforeEach(() => {
  calls = []
  getUsageAnalytics.mockReset()
  getUsageAnalytics.mockImplementation(
    (request, signal) => new Promise<UsageAnalytics>((resolve, reject) => { calls.push({ request, signal, resolve, reject }) }),
  )
})

const initial: Props = { open: true, status: full, runEpoch: 0 }

function mount(props: Props = initial) {
  return renderHook((p: Props) => useUsageAnalytics(p), { initialProps: props })
}

async function answer(index: number, extra: Record<string, unknown> = {}): Promise<void> {
  const call = calls[index]
  if (call === undefined) throw new Error(`no call ${index}`)
  await act(async () => call.resolve(answerFor(call.request, extra)))
}

describe('useUsageAnalytics — requests', () => {
  test('makes no request while closed or without the capability', () => {
    mount({ ...initial, open: false })
    mount({ ...initial, status: null })
    expect(calls).toHaveLength(0)
  })

  test('opening requests the mode default once and exposes the data', async () => {
    const { result } = mount()
    expect(result.current.state).toBe('loading')
    expect(result.current.data).toBeNull()
    expect(calls).toHaveLength(1)
    expect(calls[0]?.request).toMatchObject({ view: 'breakdown', group: 'model', scope: 'all', sort: 'cost', dir: 'desc' })
    await answer(0)
    expect(result.current.state).toBe('ready')
    expect(result.current.data?.view).toBe('breakdown')
  })

  test('closing stops requests, reopening fetches again and shows the kept data meanwhile', async () => {
    const { result, rerender } = mount()
    await answer(0)
    rerender({ ...initial, open: false })
    expect(result.current.state).toBe('idle')
    rerender({ ...initial, status: { revision: 'r2', mode: 'full' }, open: false })
    expect(calls).toHaveLength(1)
    rerender({ ...initial, status: { revision: 'r2', mode: 'full' } })
    expect(calls).toHaveLength(2)
    expect(result.current.data).not.toBeNull()
    expect(result.current.state).toBe('ready')
  })

  test('a new status revision refetches the same query; an unchanged revision does not', async () => {
    const { rerender } = mount()
    await answer(0)
    rerender({ ...initial })
    expect(calls).toHaveLength(1)
    rerender({ ...initial, status: { revision: 'r2', mode: 'full' } })
    expect(calls).toHaveLength(2)
    expect(calls[1]?.request).toEqual(calls[0]?.request)
  })

  test('a status revision change aborts the request still in flight', () => {
    const { rerender } = mount()
    rerender({ ...initial, status: { revision: 'r2', mode: 'full' } })
    expect(calls[0]?.signal?.aborted).toBe(true)
    expect(calls[1]?.signal?.aborted).toBe(false)
  })

  test('unmounting aborts the request', () => {
    const { unmount } = mount()
    unmount()
    expect(calls[0]?.signal?.aborted).toBe(true)
  })
})

describe('useUsageAnalytics — stale answers', () => {
  test('a late answer for the previous query never replaces the new one', async () => {
    const { result } = mount()
    act(() => result.current.setGroup('agent'))
    expect(calls).toHaveLength(2)
    expect(calls[0]?.signal?.aborted).toBe(true)
    await answer(1)
    await answer(0)
    expect(result.current.data?.query.group).toBe('agent')
  })

  test('data of another query is not shown while the new one loads', async () => {
    const { result } = mount()
    await answer(0)
    act(() => result.current.setView('charts'))
    expect(result.current.data).toBeNull()
    expect(result.current.state).toBe('loading')
  })

  test('an answer that does not describe the request is an error, not data', async () => {
    const { result } = mount()
    await act(async () => calls[0]?.resolve(answerFor({ ...(calls[0]?.request as UsageRequest), group: 'agent' })))
    expect(result.current.data).toBeNull()
    expect(result.current.state).toBe('error')
  })
})

describe('useUsageAnalytics — failures', () => {
  test('first failure is an error with a retry; retry fetches again', async () => {
    const { result } = mount()
    await act(async () => calls[0]?.reject(new UsageAnalyticsRequestError(500, null, 'boom')))
    expect(result.current.state).toBe('error')
    expect(result.current.error).toBe('boom')
    act(() => result.current.retry())
    expect(calls).toHaveLength(2)
    await answer(1)
    expect(result.current.state).toBe('ready')
  })

  test('a failed refresh keeps the last data and marks it stale until a refresh succeeds', async () => {
    const { result, rerender } = mount()
    await answer(0)
    rerender({ ...initial, status: { revision: 'r2', mode: 'full' } })
    await act(async () => calls[1]?.reject(new UsageAnalyticsRequestError(500, null, 'boom')))
    expect(result.current.state).toBe('stale')
    expect(result.current.data).not.toBeNull()
    rerender({ ...initial, status: { revision: 'r3', mode: 'full' } })
    await answer(2)
    expect(result.current.state).toBe('ready')
  })

  test('a lost status marks the kept data stale without a request; its return makes it ready again', async () => {
    const { result, rerender } = mount()
    await answer(0)
    rerender({ ...initial, statusAvailable: false })
    expect(result.current.state).toBe('stale')
    expect(result.current.data?.view).toBe('breakdown')
    rerender({ ...initial, statusAvailable: true })
    expect(result.current.state).toBe('ready')
    expect(calls).toHaveLength(1)
  })

  test('an aborted request is not a failure', async () => {
    const { result, rerender } = mount()
    rerender({ ...initial, status: { revision: 'r2', mode: 'full' } })
    await act(async () => calls[0]?.reject(new DOMException('aborted', 'AbortError')))
    expect(result.current.state).toBe('loading')
  })
})

describe('useUsageAnalytics — modes and runs', () => {
  test('a stored cost sort is never requested once money is off', async () => {
    const { result, rerender } = mount()
    act(() => result.current.setSort('cost', 'asc'))
    expect(calls.at(-1)?.request).toMatchObject({ sort: 'cost', dir: 'asc' })
    rerender({ ...initial, status: { revision: 'r1', mode: 'no_money' } })
    expect(calls.at(-1)?.request).toMatchObject({ sort: 'tokens', dir: 'desc' })
    expect(result.current.query.sort).toEqual({ key: 'tokens', dir: 'desc' })
    expect(result.current.metrics).toEqual(['agent_time', 'tokens'])
  })

  test('charts metric is normalized to an available one', () => {
    const { result, rerender } = mount()
    act(() => result.current.setMetric('cost'))
    rerender({ ...initial, status: { revision: 'r1', mode: 'time_only' } })
    expect(result.current.query.metric).toBe('agent_time')
  })

  test('a new run resets the selection and drops the old data', async () => {
    const { result, rerender } = mount()
    act(() => result.current.setGroup('agent'))
    await answer(1)
    expect(result.current.data).not.toBeNull()
    rerender({ ...initial, runEpoch: 1 })
    expect(result.current.query.group).toBe('model')
    expect(result.current.data).toBeNull()
    expect(calls.at(-1)?.request.group).toBe('model')
  })

  test('a mode change at an unchanged revision refetches even when the request is the same', async () => {
    const { result, rerender } = mount()
    act(() => result.current.setSort('time', 'desc'))
    await answer(1)
    const before = calls.length
    rerender({ ...initial, status: { revision: 'r1', mode: 'no_money' } })
    expect(calls).toHaveLength(before + 1)
    expect(calls.at(-1)?.request).toMatchObject({ sort: 'time', dir: 'desc' })
    expect(result.current.data).toBeNull()
    expect(result.current.state).toBe('loading')
  })

  test('charts: switching no_money to full at one revision loads the cost data', async () => {
    const { result, rerender } = mount({ ...initial, status: { revision: 'r1', mode: 'no_money' } })
    act(() => result.current.setView('charts'))
    await act(async () => calls.at(-1)?.resolve(answerFor(calls.at(-1)!.request, {}, noMoneyCharts)))
    expect(result.current.data?.mode).toBe('no_money')
    const before = calls.length
    rerender({ ...initial, status: { revision: 'r1', mode: 'full' } })
    expect(calls).toHaveLength(before + 1)
    await answer(before)
    expect(result.current.data?.mode).toBe('full')
    expect(result.current.query.metric).toBe('cost')
  })

  test('lastData keeps the previous answer of this run while a new filter loads', async () => {
    const { result } = mount()
    await answer(0)
    const first = result.current.data
    act(() => result.current.setGroup('agent'))
    expect(result.current.data).toBeNull()
    expect(first).not.toBeNull()
    expect(result.current.lastData).toBe(first)
  })

  test('lastData never carries the answer of another run', async () => {
    const { result, rerender } = mount()
    await answer(0)
    expect(result.current.lastData).not.toBeNull()
    rerender({ ...initial, runEpoch: 1 })
    expect(result.current.lastData).toBeNull()
    await act(async () => calls.at(-1)?.reject(new Error('boom')))
    expect(result.current.state).toBe('error')
    expect(result.current.lastData).toBeNull()
  })

  test('the selection survives closing and reopening within one run', async () => {
    const { result, rerender } = mount()
    act(() => result.current.setGroup('agent'))
    rerender({ ...initial, open: false })
    rerender({ ...initial })
    expect(result.current.query.group).toBe('agent')
  })
})

describe('useUsageAnalytics — jobs paging and focus', () => {
  async function openJobs() {
    const hook = mount()
    act(() => hook.result.current.drill({ agent: 'stage:build' }))
    await answer(1, { jobs: { ...fullJobs.jobs, total: 25, limit: 10, next_cursor: 'c.10', prev_cursor: undefined } })
    return hook
  }

  test('drill requests the jobs of that agent', async () => {
    const { result } = await openJobs()
    expect(calls[1]?.request).toMatchObject({ view: 'jobs', agent: 'stage:build', limit: 10, cursor: '' })
    expect(result.current.page).toBe(0)
    expect(result.current.pageCount).toBe(3)
  })

  test('next and previous use the cursors of the answer', async () => {
    const { result } = await openJobs()
    act(() => result.current.nextPage())
    expect(calls[2]?.request.cursor).toBe('c.10')
    // The visible rows are still the first page until the answer arrives.
    expect(result.current.page).toBe(0)
    await answer(2, { jobs: { ...fullJobs.jobs, total: 25, limit: 10, next_cursor: 'c.20', prev_cursor: 'p.0' } })
    expect(result.current.page).toBe(1)
    act(() => result.current.prevPage())
    expect(calls[3]?.request.cursor).toBe('p.0')
    expect(result.current.page).toBe(1)
    await answer(3, { jobs: { ...fullJobs.jobs, total: 25, limit: 10, next_cursor: 'c.10' } })
    expect(result.current.page).toBe(0)
  })

  test('no cursor — no page change and no request', async () => {
    const { result } = await openJobs()
    act(() => result.current.prevPage())
    expect(calls).toHaveLength(2)
  })

  test('focusJob requests the page with the job and takes the page number from its index', async () => {
    const { result } = await openJobs()
    act(() => result.current.focusJob('job-17'))
    expect(calls[2]?.request).toMatchObject({ view: 'jobs', focusJob: 'job-17', sort: 'started', dir: 'asc', agent: '' })
    await answer(2, { jobs: { ...fullJobs.jobs, total: 25, limit: 10, focus: { status: 'found', job_id: 'job-17', index: 17 } } })
    expect(result.current.page).toBe(1)
    expect(result.current.query.highlightJobId).toBe('job-17')
  })

  test('changing a filter clears the highlight', async () => {
    const { result } = await openJobs()
    act(() => result.current.focusJob('job-17'))
    act(() => result.current.setModel('anthropic/claude-opus-4-8'))
    expect(result.current.query.highlightJobId).toBe('')
    expect(calls.at(-1)?.request.focusJob).toBe('')
  })

  test('a stale cursor goes back to the first page by itself', async () => {
    const { result } = await openJobs()
    act(() => result.current.nextPage())
    await act(async () => calls[2]?.reject(new UsageAnalyticsRequestError(409, 'stale_cursor', 'old cursor')))
    expect(calls).toHaveLength(4)
    expect(calls[3]?.request.cursor).toBe('')
    expect(result.current.page).toBe(0)
  })

  test('a stale cursor with a highlighted job returns to that job', async () => {
    const { result } = await openJobs()
    act(() => result.current.focusJob('job-17'))
    await answer(2, { jobs: { ...fullJobs.jobs, total: 25, limit: 10, next_cursor: 'c.20', focus: { status: 'found', job_id: 'job-17', index: 17 } } })
    act(() => result.current.nextPage())
    await act(async () => calls[3]?.reject(new UsageAnalyticsRequestError(409, 'stale_cursor', 'old cursor')))
    expect(calls[4]?.request).toMatchObject({ focusJob: 'job-17', cursor: '' })
  })

  test('an invalid-param answer is an error and is not retried in a loop', async () => {
    const { result } = mount()
    await act(async () => calls[0]?.reject(new UsageAnalyticsRequestError(400, 'invalid_param', 'bad')))
    await waitFor(() => expect(result.current.state).toBe('error'))
    expect(calls).toHaveLength(1)
  })
})
