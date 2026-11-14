import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from './App'

class Socket {
  static last: Socket
  onmessage: ((event: { data: string }) => void) | null = null
  constructor() { Socket.last = this }
  close() {}
}

type Json = Record<string, unknown>
const spend = (tokens: number, usd = 0) => ({ tokens, money_nano_usd: Math.round(usd * 1e9) })
const budgetView = (scope: string, kind: string, over: Json = {}): Json => ({
  scope, kind, revision: 1, base_source: 'none', override: { kind: 'none' }, spent: spend(0), evaluation: {}, actions: ['update'], ...over,
})
const limited = (scope: string, kind: string, tokens: number, spent: number, over: Json = {}) =>
  budgetView(scope, kind, {
    base: { tokens, stop_policy: 'hard' }, base_source: 'declaration', effective: { tokens, stop_policy: 'hard' }, spent: spend(spent),
    evaluation: { tokens: spent >= tokens ? 'reached' : spent >= tokens * 0.8 ? 'warning' : 'ok' }, ...over,
  })

type Setup = { budgets?: Json[]; events?: Json[]; stageStatus?: string; capability?: boolean; autonomous?: boolean }

function setup({ budgets = [], events = [], stageStatus = 'running', capability = true, autonomous = false }: Setup = {}) {
  const posts: { url: string; body: Json }[] = []
  const state = {
    budgets, events, stageStatus, secondStageStatus: 'pending', showPlan: true,
    statusResponseGate: null as Promise<void> | null, updateResponseGate: null as Promise<void> | null,
  }
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
    const url = String(input)
    const json = (value: unknown, status = 200) => ({ ok: status < 300, status, json: async () => value }) as Response
    if (init?.method === 'POST') posts.push({ url, body: init.body ? (JSON.parse(init.body as string) as Json) : {} })
    if (url === '/api/budgets/update') {
      state.stageStatus = 'running'
      state.showPlan = false
      const view = limited('stage:s1', 'stage', 1000, 1000, {
        override: { kind: 'limit', spec: { tokens: 5000, stop_policy: 'hard' } }, effective: { tokens: 5000, stop_policy: 'hard' },
        evaluation: { tokens: 'ok' }, revision: 2,
      })
      state.budgets = [view]
      if (state.updateResponseGate !== null) await state.updateResponseGate
      return json({ view })
    }
    if (url.includes('/api/status')) {
      if (state.stageStatus === 'running' && state.statusResponseGate !== null) await state.statusResponseGate
      return json({
        flow_name: 'budget-flow', run_id: 'run-b', flow_pause_state: 'none',
        stages: [
          { id: 's1', name: 's1', autonomous, status: state.stageStatus, updated_at: '2026-11-01T10:00:00Z', show_plan: state.showPlan },
          { id: 's2', name: 's2', status: state.secondStageStatus, updated_at: '2026-11-01T10:00:00Z' },
        ],
        capabilities: { budgets: capability, budget_export: true }, accounting: { health: 'ok', has_data: false, show_money: false },
      })
    }
    if (url.startsWith('/api/budgets/view')) {
      const scope = new URL(url, 'http://x').searchParams.get('scope')
      return json({ view: state.budgets.find((b) => b.scope === scope) })
    }
    if (url === '/api/budgets') return json({ budgets: state.budgets })
    if (url.includes('/api/events')) return json(state.events)
    if (url.includes('/api/mesh/')) return json({}, 404)
    if (url.includes('/plan')) return { ok: true, text: async () => '' } as Response
    return json([])
  })
  return { state, posts }
}

const noticeEvent = (type: string, stageId: string, payload: Json, seq?: number) => ({ type, stage_id: stageId, timestamp: '2026-11-01T10:00:00Z', ...(seq === undefined ? {} : { seq }), data: payload })
const runWarning = noticeEvent('budget_warning', '', { kind: 'warning', scope: 'run', metric: 'tokens', limit: { tokens: 100000, stop_policy: 'hard' }, spent: spend(82000), revision: 1 })
const runBudget = (spentTokens: number) => limited('run', 'run', 100000, spentTokens, { revision: 5 })

const requestCount = (path: string) => vi.mocked(fetch).mock.calls.filter(([url]) => String(url) === path).length

async function emitSocket(event: Json) {
  expect(Socket.last.onmessage).toBeTypeOf('function')
  await act(async () => Socket.last.onmessage?.({ data: JSON.stringify(event) }))
}

function gate() {
  let release!: () => void
  const promise = new Promise<void>((resolve) => { release = resolve })
  return { promise, release }
}

// A changed rendered value proves the response was committed, beyond request initiation.
const waitForRunSpend = (spentTokens: number) =>
  screen.findByRole('button', { name: `Run budget: ${spentTokens / 1000}k / 100k. Open budget` })

async function openRunDraft() {
  // Control polling without faking the feed flush or Testing Library's wait timers.
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
  const { state, posts } = setup({ budgets: [runBudget(91000)], events: [runWarning] })
  render(<App />)
  await screen.findByText('Budget 80% · Run: 82k / 100k tokens')
  fireEvent.click(screen.getByRole('button', { name: 'Edit budget for Run' }))
  const dialog = await screen.findByRole('dialog', { name: 'Budget · Run' })
  await within(dialog).findByText('91k tokens · $0.00')
  fireEvent.click(within(dialog).getByRole('radio', { name: 'Custom limit' }))
  const tokens = within(dialog).getByLabelText('Tokens')
  fireEvent.change(tokens, { target: { value: '777k' } })
  act(() => tokens.focus())

  const assertDraftKept = () => {
    const current = screen.getByRole('dialog', { name: 'Budget · Run' })
    expect(current).toBe(dialog)
    expect(within(current).getByLabelText('Tokens')).toBe(tokens)
    expect(tokens).toHaveValue('777k')
    expect(tokens).toHaveFocus()
    expect(within(current).getByRole('radio', { name: 'Custom limit' })).toBeChecked()
  }
  assertDraftKept()
  return { state, posts, assertDraftKept }
}

beforeEach(() => vi.stubGlobal('WebSocket', Socket))
afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  localStorage.clear()
})

describe('App budgets', () => {
  it('makes no budget requests when the capability is off', async () => {
    setup({ capability: false, budgets: [limited('run', 'run', 1000, 900)] })
    render(<App />)
    await screen.findByText('s1', { selector: '.stage-name' })
    await act(async () => {})
    expect(vi.mocked(fetch).mock.calls.some(([url]) => String(url).startsWith('/api/budgets'))).toBe(false)
    expect(document.querySelector('[data-metric="budget"]')).toBeNull()
  })

  it('a run notice shows in the stage feed and its pencil opens the dialog on the current budget', async () => {
    setup({ budgets: [limited('run', 'run', 100000, 91000, { revision: 5 })], events: [runWarning] })
    render(<App />)
    const row = await screen.findByText('Budget 80% · Run: 82k / 100k tokens')
    expect(row).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Edit budget for Run' }))
    const dialog = await screen.findByRole('dialog', { name: 'Budget · Run' })
    // The dialog shows the CURRENT figures (91k), not the notice's 82k.
    await within(dialog).findByText('91k tokens · $0.00')
  })

  it('shows a budget tile in the header only for a run with a limit, and it opens the dialog', async () => {
    const { state } = setup({ budgets: [budgetView('run', 'run')] })
    render(<App />)
    await screen.findByText('s1', { selector: '.stage-name' })
    await act(async () => {})
    expect(document.querySelector('[data-metric="budget"]')).toBeNull()
    state.budgets = [limited('run', 'run', 100000, 82000)]
    await act(async () => Socket.last.onmessage?.({ data: JSON.stringify({ type: 'budget_warning', stage_id: '', data: { scope: 'run', spent: {} } }) }))
    const tile = await waitFor(() => {
      const el = document.querySelector<HTMLElement>('.metric-budget')
      expect(el).not.toBeNull()
      return el as HTMLElement
    })
    expect(tile).toHaveAttribute('data-level', 'warning')
    fireEvent.click(tile)
    await screen.findByRole('dialog', { name: 'Budget · Run' })
  })

  it('a stage held by a budget shows the budget banner and Continue opens the dialog instead of continuing', async () => {
    const held = limited('stage:s1', 'stage', 1000, 1000, { hold: { reason: 'limit_reached', scope: 'stage:s1' }, actions: ['update', 'resume'] })
    const { posts } = setup({ stageStatus: 'paused', budgets: [held] })
    render(<App />)
    expect(await screen.findByText('Stopped by budget', { selector: '.attention-banner-title' })).toBeInTheDocument()
    expect(screen.getByText(/The budget of Stage s1 is used up/)).toBeInTheDocument()
    fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))
    await screen.findByRole('dialog', { name: 'Budget · Stage s1' })
    expect(posts.some((p) => p.url.includes('/continue'))).toBe(false)
  })

  it('a stage paused without a budget hold keeps the ordinary paused banner and Continue', async () => {
    const { posts } = setup({ stageStatus: 'paused', budgets: [limited('stage:s1', 'stage', 1000, 100)] })
    render(<App />)
    expect(await screen.findByText('Stage is paused', { selector: '.attention-banner-title' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    await waitFor(() => expect(posts.some((p) => p.url.includes('/continue'))).toBe(true))
  })

  it('the Cost tab lists every scope with Edit, including scopes without a limit', async () => {
    setup({ budgets: [budgetView('run', 'run'), limited('stage:s1', 'stage', 1000, 850), budgetView('stage:s2', 'stage')] })
    render(<App />)
    await screen.findByText('s1', { selector: '.stage-name' })
    fireEvent.click(screen.getByRole('tab', { name: 'Cost' }))
    const section = await screen.findByRole('region', { name: 'Budgets' })
    await within(section).findByText(/850 \/ 1k tokens · Hard stop · Budget 85%/)
    expect(within(section).getByText(/No limit/, { selector: '[data-scope="stage:s2"] .cost-budgets-figures' })).toBeInTheDocument()
    expect(within(section).getAllByRole('button', { name: /^Edit budget for/ })).toHaveLength(3)
    fireEvent.click(within(section).getByRole('button', { name: 'Edit budget for Stage s2' }))
    await screen.findByRole('dialog', { name: 'Budget · Stage s2' })
  })

  it('the stage badge appears only from 80% and is a separate button', async () => {
    setup({ budgets: [limited('stage:s1', 'stage', 1000, 850), limited('stage:s2', 'stage', 1000, 100)] })
    render(<App />)
    const badge = await screen.findByRole('button', { name: /Budget for stage s1: Budget 85%/ })
    expect(badge.closest('.stage-actions')).not.toBeNull()
    expect(screen.queryByRole('button', { name: /Budget for stage s2/ })).toBeNull()
  })

  it('the open dialog and its draft survive status polls', async () => {
    const { state, posts, assertDraftKept } = await openRunDraft()
    for (const spentTokens of [92000, 93000]) {
      const statusBefore = requestCount('/api/status')
      const budgetsBefore = requestCount('/api/budgets')
      state.budgets = [runBudget(spentTokens)]
      await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
      await waitForRunSpend(spentTokens)
      expect(requestCount('/api/status')).toBe(statusBefore + 1)
      expect(requestCount('/api/budgets')).toBeGreaterThan(budgetsBefore)
      assertDraftKept()
    }
    expect(posts).toEqual([])
  })

  it('the open dialog and its draft survive budget events', async () => {
    const { state, posts, assertDraftKept } = await openRunDraft()
    for (const spentTokens of [92000, 93000]) {
      const statusBefore = requestCount('/api/status')
      const budgetsBefore = requestCount('/api/budgets')
      state.budgets = [runBudget(spentTokens)]
      await emitSocket(noticeEvent('budget_warning', '', {
        kind: 'warning', scope: 'run', metric: 'tokens',
        limit: { tokens: 100000, stop_policy: 'hard' }, spent: spend(spentTokens), revision: 5,
      }, spentTokens))
      await waitForRunSpend(spentTokens)
      // The event alone refreshes budgets: no status poll may stand in for it.
      expect(requestCount('/api/status')).toBe(statusBefore)
      expect(requestCount('/api/budgets')).toBeGreaterThan(budgetsBefore)
      assertDraftKept()
    }
    expect(posts).toEqual([])
  })

  it('the open dialog and its draft survive a question from another stage', async () => {
    const { state, posts, assertDraftKept } = await openRunDraft()
    const statusBefore = requestCount('/api/status')
    state.secondStageStatus = 'awaiting_user_input'
    state.budgets = [runBudget(94000)]
    await emitSocket(noticeEvent('stage_status_changed', 's2', { status: 'awaiting_user_input' }, 30))
    await waitForRunSpend(94000)
    await screen.findByRole('tab', { name: /Question/ })
    expect(requestCount('/api/status')).toBe(statusBefore + 1)
    const secondStage = document.querySelector('.stage-item[data-stage-id="s2"]')
    expect(secondStage).toHaveAttribute('data-status', 'awaiting_user_input')
    expect(secondStage).toHaveAttribute('data-attention', 'true')
    expect(screen.getByRole('tab', { name: 'Feed' })).toHaveAttribute('aria-selected', 'true')
    assertDraftKept()
    expect(posts).toEqual([])
  })

  // After Save and continue, focus must not drop to body when its target disappears, however late Running arrives.
  describe.each([
    { source: 'rail', delay: 0 },
    { source: 'detached-rail', delay: 0 },
    { source: 'detached-rail', delay: 3500 },
    { source: 'plan', delay: 3500 },
  ] as const)('focus restoration from $source with Running released $delay ms after close', ({ source, delay }) => {
    it('keeps focus through removal of the restored target', async () => {
      vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
      const held = limited('stage:s1', 'stage', 1000, 1000, {
        hold: { reason: 'limit_reached', scope: 'stage:s1' }, actions: ['update', 'resume'],
      })
      const { state, posts } = setup({ stageStatus: 'paused', budgets: [held], autonomous: true })
      const statusGate = gate()
      state.statusResponseGate = statusGate.promise
      const updateGate = gate()
      if (source === 'detached-rail') state.updateResponseGate = updateGate.promise
      render(<App />)
      // Until budgets load, Continue resumes the stage directly; the budget banner proves they have loaded.
      await screen.findByText('Stopped by budget', { selector: '.attention-banner-title' })
      const paused = screen.getByRole('tab', { name: /^Paused/ })
      expect(paused).toHaveAttribute('aria-selected', 'true')
      const opener = source === 'plan'
        ? screen.getByRole('button', { name: 'Continue' })
        : screen.getByRole('button', { name: /^Budget for stage s1:/ })
      const closeTarget = source === 'plan' ? opener : paused
      opener.focus()
      fireEvent.click(opener)
      const dialog = await screen.findByRole('dialog', { name: 'Budget · Stage s1' })
      fireEvent.click(await within(dialog).findByRole('radio', { name: 'Custom limit' }))
      fireEvent.change(within(dialog).getByLabelText('Tokens'), { target: { value: '5k' } })
      const openerFocus = vi.spyOn(opener, 'focus')
      const statusBefore = requestCount('/api/status')
      try {
        fireEvent.click(within(dialog).getByRole('button', { name: 'Save and continue' }))
        if (source === 'detached-rail') {
          // A server notice can publish the saved view before the POST response closes the dialog.
          await waitFor(() => expect(posts).toHaveLength(1))
          await emitSocket(noticeEvent('budget_updated', 's1', {
            kind: 'updated', scope: 'stage:s1', revision: 2,
            limit: { tokens: 5000, stop_policy: 'hard' }, spent: spend(1000),
          }, 50))
          await waitFor(() => expect(opener.isConnected).toBe(false))
          expect(screen.getByRole('dialog', { name: 'Budget · Stage s1' })).toBe(dialog)
          expect(dialog).toContainElement(document.activeElement as HTMLElement)
          await act(async () => { updateGate.release() })
        }
        await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Budget · Stage s1' })).toBeNull())
        expect(posts).toEqual([{ url: '/api/budgets/update', body: {
          scope: 'stage:s1', expected_revision: 1,
          override: { kind: 'limit', spec: { tokens: 5000, stop_policy: 'hard' } }, resume: true,
        } }])
        expect(requestCount('/api/status')).toBe(statusBefore + 1)
        expect(document.querySelector('.stage-item[data-stage-id="s1"]')).toHaveAttribute('data-status', 'paused')
        expect(screen.queryByRole('button', { name: /^Budget for stage s1:/ })).toBeNull()
        if (source === 'plan') {
          expect(opener.isConnected).toBe(true)
          expect(openerFocus).toHaveBeenCalled()
        } else {
          expect(opener.isConnected).toBe(false)
          if (source === 'detached-rail') expect(openerFocus).not.toHaveBeenCalled()
        }
        expect(closeTarget).toHaveFocus()

        // Interval polls are frozen; every Running response waits on the same gate.
        if (delay > 0) await act(async () => { await new Promise((resolve) => setTimeout(resolve, delay)) })
        expect(requestCount('/api/status')).toBe(statusBefore + 1)
        expect(document.querySelector('.stage-item[data-stage-id="s1"]')).toHaveAttribute('data-status', 'paused')
        expect(closeTarget).toHaveFocus()

        await act(async () => { statusGate.release() })
        await waitFor(() => expect(document.querySelector('.stage-item[data-stage-id="s1"]')).toHaveAttribute('data-status', 'running'))
        expect(paused.isConnected).toBe(false)
        expect(opener.isConnected).toBe(false)
        const feed = screen.getByRole('tab', { name: 'Feed' })
        expect(feed).toHaveAttribute('aria-selected', 'true')
        await waitFor(() => expect(feed).toHaveFocus())
        expect(document.activeElement).not.toBe(document.body)
        expect(document.activeElement?.isConnected).toBe(true)
      } finally {
        await act(async () => { updateGate.release(); statusGate.release() })
      }
    }, 10000)
  })
})
