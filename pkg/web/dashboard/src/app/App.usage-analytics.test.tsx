import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import fullBreakdown from '../../../../server/testdata/usage-analytics/full-breakdown.json'
import noMoneyBreakdown from '../../../../server/testdata/usage-analytics/no-money-breakdown.json'
import { App } from './App'

class StubWebSocket {
  static instances: StubWebSocket[] = []
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  constructor() {
    StubWebSocket.instances.push(this)
  }
  close() {}
}

// down — the server is gone: every request fails like a killed process.
type StatusState = { analytics: { revision: string; mode: string; models?: number; agents?: number } | null; runId: string; down?: boolean }

// serve answers /api/status from the mutable state and /api/usage/analytics from the golden fixtures.
function serve(state: StatusState): void {
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    if (state.down === true) throw new TypeError('Failed to fetch')
    const url = typeof input === 'string' ? input : (input as Request).url
    if (url.includes('/api/status')) {
      const payload: Record<string, unknown> = {
        flow_name: 'demo',
        run_id: state.runId,
        stages: [{ id: 's1', name: 'Propose', status: 'running', updated_at: '', show_plan: true }],
        accounting: { health: 'ok', has_data: true, show_money: state.analytics?.mode === 'full' },
        capabilities: { usage_analytics: state.analytics !== null },
      }
      if (state.analytics !== null) payload.analytics = state.analytics
      return { ok: true, json: async () => payload } as Response
    }
    if (url.includes('/api/usage/analytics')) {
      const body = state.analytics?.mode === 'full' ? fullBreakdown : noMoneyBreakdown
      return { ok: true, status: 200, json: async () => ({ ...body, query: { view: 'breakdown', group: 'model', scope: 'all', sort: state.analytics?.mode === 'full' ? 'cost' : 'tokens', dir: 'desc' } }) } as Response
    }
    if (url.includes('/api/mesh/')) return { ok: false, status: 404, json: async () => ({}) } as Response
    if (url.includes('/plan')) return { ok: true, text: async () => '' } as Response
    return { ok: true, json: async () => [] } as Response
  })
}

// STATUS_POLL_MS — the /api/status poll interval of useStatus.
const STATUS_POLL_MS = 3_000

const analyticsCalls = (): string[] =>
  vi.mocked(fetch).mock.calls.map(([url]) => String(url)).filter((url) => url.includes('/api/usage/analytics'))

async function pushStatusRefresh(): Promise<void> {
  const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
  await act(async () => {
    ws?.onmessage?.({ data: JSON.stringify({ type: 'stage_status_changed', data: { status: 'running' }, stage_id: 's1' }) })
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

async function openCost(): Promise<void> {
  await waitFor(() => expect(screen.getByRole('tab', { name: 'Cost' })).toBeInTheDocument())
  fireEvent.click(screen.getByRole('tab', { name: 'Cost' }))
}

describe('App — Models & agents', () => {
  beforeEach(() => {
    StubWebSocket.instances = []
    vi.stubGlobal('WebSocket', StubWebSocket)
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  test('without the capability there is no entry and no request', async () => {
    serve({ analytics: null, runId: 'run-1' })
    render(<App />)
    await openCost()
    await waitFor(() => expect(screen.getByRole('heading', { level: 2, name: /usage & time/i })).toBeInTheDocument())
    expect(screen.queryByRole('button', { name: /models & agents/i })).toBeNull()
    expect(analyticsCalls()).toHaveLength(0)
  })

  test('the Cost overview makes no analytics request and takes the entry counts from /api/status', async () => {
    serve({ analytics: { revision: 'r1', mode: 'no_money', models: 3, agents: 5 }, runId: 'run-1' })
    render(<App />)
    await openCost()
    expect(await screen.findByText('3 models · 5 agents')).toBeInTheDocument()
    expect(analyticsCalls()).toHaveLength(0)
  })

  test('the entry is text-only when /api/status has no counts', async () => {
    serve({ analytics: { revision: 'r1', mode: 'no_money' }, runId: 'run-1' })
    render(<App />)
    await openCost()
    expect(await screen.findByRole('button', { name: /models & agents/i })).toBeInTheDocument()
    expect(screen.queryByText(/\d+ models?/)).toBeNull()
    expect(analyticsCalls()).toHaveLength(0)
  })

  test('without the Cost tab open nothing is requested', async () => {
    serve({ analytics: { revision: 'r1', mode: 'no_money' }, runId: 'run-1' })
    render(<App />)
    await waitFor(() => expect(screen.getByRole('tab', { name: 'Cost' })).toBeInTheDocument())
    expect(analyticsCalls()).toHaveLength(0)
  })

  test('opening the details requests the mode default once; back returns to the overview', async () => {
    serve({ analytics: { revision: 'r1', mode: 'no_money' }, runId: 'run-1' })
    render(<App />)
    await openCost()
    fireEvent.click(await screen.findByRole('button', { name: /models & agents/i }))
    await waitFor(() => expect(analyticsCalls()).toHaveLength(1))
    const params = new URL(analyticsCalls()[0] as string, 'http://afm.test').searchParams
    expect(Object.fromEntries(params)).toEqual({ view: 'breakdown', group: 'model', scope: 'all', sort: 'tokens', dir: 'desc' })
    expect(screen.queryByRole('table', { name: /time by stage|stage/i })).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: /back to cost overview/i }))
    expect(await screen.findByRole('button', { name: /models & agents/i })).toBeInTheDocument()
  })

  test('requests follow the status revision only while Cost is open, and the subview is remembered', async () => {
    const state: StatusState = { analytics: { revision: 'r1', mode: 'no_money' }, runId: 'run-1' }
    serve(state)
    render(<App />)
    await openCost()
    fireEvent.click(await screen.findByRole('button', { name: /models & agents/i }))
    await waitFor(() => expect(analyticsCalls()).toHaveLength(1))

    state.analytics = { revision: 'r2', mode: 'no_money' }
    await pushStatusRefresh()
    await waitFor(() => expect(analyticsCalls()).toHaveLength(2))

    fireEvent.click(screen.getByRole('tab', { name: 'Feed' }))
    state.analytics = { revision: 'r3', mode: 'no_money' }
    await pushStatusRefresh()
    await pushStatusRefresh()
    expect(analyticsCalls()).toHaveLength(2)

    fireEvent.click(screen.getByRole('tab', { name: 'Cost' }))
    await waitFor(() => expect(analyticsCalls()).toHaveLength(3))
    expect(screen.getByRole('button', { name: /back to cost overview/i })).toBeInTheDocument()
  })

  test('losing the capability while the details are open returns to the overview', async () => {
    const state: StatusState = { analytics: { revision: 'r1', mode: 'no_money' }, runId: 'run-1' }
    serve(state)
    render(<App />)
    await openCost()
    fireEvent.click(await screen.findByRole('button', { name: /models & agents/i }))
    await waitFor(() => expect(analyticsCalls()).toHaveLength(1))

    state.analytics = null
    await pushStatusRefresh()
    await waitFor(() => expect(screen.getByRole('heading', { level: 2, name: /usage & time/i })).toBeInTheDocument())
    expect(screen.queryByRole('button', { name: /models & agents/i })).toBeNull()
  })

  test('a switch to full mode is requested with the cost sort', async () => {
    const state: StatusState = { analytics: { revision: 'r1', mode: 'no_money' }, runId: 'run-1' }
    serve(state)
    render(<App />)
    await openCost()
    fireEvent.click(await screen.findByRole('button', { name: /models & agents/i }))
    await waitFor(() => expect(analyticsCalls()).toHaveLength(1))
    state.analytics = { revision: 'r2', mode: 'full' }
    await pushStatusRefresh()
    await waitFor(() => expect(analyticsCalls()).toHaveLength(2))
    expect(new URL(analyticsCalls()[1] as string, 'http://afm.test').searchParams.get('sort')).toBe('cost')
  })

  test('a lost server keeps the last data with a stale marker and a stopped clock until it returns', async () => {
    vi.useFakeTimers({ now: Date.parse(noMoneyBreakdown.as_of), shouldAdvanceTime: true })
    const state: StatusState = { analytics: { revision: 'r1', mode: 'no_money' }, runId: 'run-1' }
    serve(state)
    render(<App />)
    await openCost()
    fireEvent.click(await screen.findByRole('button', { name: /models & agents/i }))
    await screen.findByRole('table', { name: /usage by/i })
    const totals = (): string | null => screen.getByLabelText('Filtered usage totals').textContent
    const advance = async (ms: number): Promise<void> => {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(ms)
      })
    }

    // The fixture has a running job, so its time grows while the status is fresh.
    const ticking = totals()
    await advance(5_000)
    expect(totals()).not.toBe(ticking)
    expect(screen.queryByText(/showing the last data/i)).toBeNull()

    // The regular status poll notices the loss and the return; one poll interval is enough for each.
    state.down = true
    await advance(STATUS_POLL_MS)
    expect(screen.getByText(/showing the last data/i)).toBeInTheDocument()
    expect(screen.getByRole('table', { name: /usage by/i })).toBeInTheDocument()
    const frozen = totals()
    expect(frozen).not.toBe(ticking)
    await advance(5_000)
    expect(totals()).toBe(frozen)

    state.down = false
    await advance(STATUS_POLL_MS)
    expect(screen.queryByText(/showing the last data/i)).toBeNull()
    expect(screen.getByRole('table', { name: /usage by/i })).toBeInTheDocument()
    // The revision did not change: neither the loss nor the return asks for analytics again.
    expect(analyticsCalls()).toHaveLength(1)
  })
})
