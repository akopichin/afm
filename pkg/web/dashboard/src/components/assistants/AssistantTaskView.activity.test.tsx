import { Profiler } from 'react'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { AssistantTaskView } from './AssistantTaskView'
import type { AssistantTask } from '../../types/assistant-task'
import type { AssistantActivityEntry } from '../../types/assistant-activity'

function makeTask(over: Partial<AssistantTask> = {}): AssistantTask {
  return {
    id: 't1', parentTaskId: null, profileId: 'reviewer', title: 'Reviewer', mode: 'advisory',
    trigger: 'one_shot', status: 'running', statusReason: '', depth: 1,
    watchPasses: null, watchLastCheckpointSeq: null, blocksRootCompletion: false, waived: false, waiverReason: '',
    ...over,
  }
}

type Row = { cursor: string; event_id: string; task_id: string; tool: string; detail: string; turn: number; occurred_at: string }
const row = (n: number, over: Partial<Row> = {}): Row => ({
  cursor: String(n * 10), event_id: `a${n}`, task_id: 't1', tool: 'Bash', detail: `cmd ${n}`, turn: 1,
  occurred_at: new Date(Date.now() - 20_000 + n * 1000).toISOString(), ...over,
})
const live = (n: number, over: Partial<AssistantActivityEntry> = {}): AssistantActivityEntry => ({
  eventId: `l${n}`, taskId: 't1', participantId: '', turn: 1, occurredAt: new Date().toISOString(),
  tool: 'WebFetch', detail: `live ${n}`, truncated: false, cursor: undefined, ...over,
})

const json = (body: unknown, status = 200): Response => new Response(JSON.stringify(body), { status })
const page = (entries: Row[], over: Record<string, unknown> = {}) => ({
  entries, after: entries.length > 0 ? entries[entries.length - 1]!.cursor : '0', before: '0', has_more: false, has_older: false, ...over,
})
const resultEvent = {
  seq: 1, type: 'task_result',
  data: { verdict: 'pass', summary: 'All good', findings: [] },
}

// routes fetch by URL: activity pages, task events (the result source) and everything else empty.
function mockApi(opts: { activity?: (url: string) => Promise<Response> | Response; events?: unknown[] } = {}) {
  return vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const url = typeof input === 'string' ? input : (input as Request).url
    if (url.includes('/activity')) return opts.activity ? opts.activity(url) : json(page([]))
    if (url.includes('/events')) {
      const events = opts.events ?? []
      return json({ events, next_cursor: events.length, has_more: false })
    }
    return json({})
  })
}

const activityRequests = (fetchMock: ReturnType<typeof mockApi>) =>
  fetchMock.mock.calls.filter(([url]) => String(url).includes('/activity')).length

const view = (task: AssistantTask, extra: Partial<Parameters<typeof AssistantTaskView>[0]> = {}) => (
  <AssistantTaskView task={task} runId="run-1" connected {...extra} />
)

describe('AssistantTaskView activity', () => {
  beforeEach(() => {
    vi.useRealTimers()
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('says it is loading before the first page, not that the helper is waiting', async () => {
    let release: (r: Response) => void = () => {}
    mockApi({ activity: () => new Promise<Response>((resolve) => { release = resolve }) })
    render(view(makeTask()))

    expect(screen.getByText('Loading activity…')).toBeInTheDocument()
    expect(screen.queryByText(/waiting for the first/i)).toBeNull()

    await act(async () => { release(json(page([]))) })
    expect(await screen.findByText(/waiting for the first message or tool call/i)).toBeInTheDocument()
    expect(screen.queryByText('Loading activity…')).toBeNull()
  })

  test('shows messages and tools before any result exists', async () => {
    mockApi({ activity: () => json(page([row(1, { tool: 'text', detail: 'Checking the **diff**' }), row(2, { tool: 'ToolSearch', detail: 'read file' })])) })
    const { container } = render(view(makeTask()))

    expect(await screen.findByText('ToolSearch')).toBeInTheDocument()
    expect(container.querySelector('.assistant-activity-message strong')).toHaveTextContent('diff')
    expect(screen.getByText(/Last action: ToolSearch · \d+s ago/)).toBeInTheDocument()
  })

  test('a live entry from the socket appears without a refetch of history', async () => {
    const fetchMock = mockApi()
    const { rerender } = render(view(makeTask()))
    await screen.findByText(/waiting for the first/i)

    const requests = () => fetchMock.mock.calls.filter(([url]) => String(url).includes('/activity')).length
    const before = requests()
    rerender(view(makeTask(), { liveActivity: [live(1)] }))
    expect(await screen.findByText('live 1')).toBeInTheDocument()
    expect(requests()).toBe(before)
  })

  test('Last action shows a live entry that arrived after the history read, not a row the socket dropped', async () => {
    mockApi({
      activity: () => json(page([row(1, { event_id: 'd', tool: 'Read' }), row(2, { event_id: 'f', tool: 'Edit' })])),
    })
    const seen = live(1, { eventId: 'd', tool: 'Read' })
    const { rerender } = render(view(makeTask(), { liveActivity: [seen] }))
    await screen.findByText(/Last action: Edit/)

    rerender(view(makeTask(), { liveActivity: [seen, live(2, { eventId: 'e', tool: 'WebFetch', detail: 'newest' })] }))
    expect(await screen.findByText(/Last action: WebFetch/)).toBeInTheDocument()
    expect(screen.queryByText(/Last action: Edit/)).toBeNull()
  })

  test("another helper's live entries are not shown", async () => {
    mockApi()
    render(view(makeTask(), { liveActivity: [live(1, { taskId: 'other' })] }))
    await screen.findByText(/waiting for the first/i)
    expect(screen.queryByText('live 1')).toBeNull()
  })

  test('the result card follows the history', async () => {
    mockApi({ activity: () => json(page([row(1, { detail: 'last tool' })])), events: [resultEvent] })
    render(view(makeTask({ status: 'completed' })))

    const region = await screen.findByRole('region', { name: 'Helper activity' })
    const tool = await within(region).findByText('last tool')
    const verdict = await within(region).findByText('All good')
    expect(tool.compareDocumentPosition(verdict) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  test.each(['failed', 'cancelled'] as const)('a %s helper keeps its history above the reason', async (status) => {
    mockApi({ activity: () => json(page([row(1, { detail: 'before the end' })])) })
    render(view(makeTask({ status, statusReason: 'it stopped' })))

    const region = await screen.findByRole('region', { name: 'Helper activity' })
    const tool = await within(region).findByText('before the end')
    const reason = within(region).getByRole('alert')
    expect(reason).toHaveTextContent('it stopped')
    expect(tool.compareDocumentPosition(reason) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  test('a paused helper keeps its history and shows why it paused', async () => {
    mockApi({ activity: () => json(page([row(1, { detail: 'before pause' })])) })
    render(view(makeTask({ status: 'paused', statusReason: 'paused by user' })))

    expect(await screen.findByText('before pause')).toBeInTheDocument()
    expect(screen.getByText('paused by user')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Continue' })).toBeInTheDocument()
  })

  test('a finished helper with no saved history says so, and still shows its result', async () => {
    mockApi({ activity: () => json(page([])), events: [resultEvent] })
    render(view(makeTask({ status: 'completed' })))

    expect(await screen.findByText(/no activity was recorded for this task/i)).toBeInTheDocument()
    expect(await screen.findByText('All good')).toBeInTheDocument()
  })

  test('unavailable history (404) is not an error or a waiting state', async () => {
    mockApi({ activity: () => json({ error: 'not found' }, 404) })
    render(view(makeTask()))

    expect(await screen.findByText(/activity is not available for this task/i)).toBeInTheDocument()
    expect(screen.queryByText(/waiting for the first/i)).toBeNull()
  })

  test('a failed first load is an error with a Retry button, not an empty feed', async () => {
    mockApi({ activity: () => json({ error: 'boom' }, 500) })
    render(view(makeTask()))

    expect(await screen.findByText(/could not load activity/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
    expect(screen.queryByText(/waiting for the first/i)).toBeNull()
  })

  test('Retry reloads a finished helper whose first load failed', async () => {
    let failing = true
    const fetchMock = mockApi({ activity: () => (failing ? json({ error: 'boom' }, 500) : json(page([row(1, { detail: 'recovered' })]))) })
    render(view(makeTask({ status: 'completed' })))

    const retry = await screen.findByRole('button', { name: 'Retry' })
    failing = false
    fireEvent.click(retry)
    expect(await screen.findByText('recovered')).toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes('/activity')).length).toBeGreaterThanOrEqual(2)
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
  })

  test('Retry reloads a finished helper whose first load failed while a live entry is on screen', async () => {
    let failing = true
    const fetchMock = mockApi({ activity: () => (failing ? json({ error: 'boom' }, 500) : json(page([row(1, { detail: 'recovered' })]))) })
    render(view(makeTask({ status: 'completed' }), { liveActivity: [live(1)] }))

    expect(await screen.findByText('live 1')).toBeInTheDocument()
    const retry = await within(await screen.findByRole('status')).findByRole('button', { name: 'Retry' })
    const requests = activityRequests(fetchMock)
    failing = false
    fireEvent.click(retry)
    expect(await screen.findByText('recovered')).toBeInTheDocument()
    expect(activityRequests(fetchMock)).toBeGreaterThan(requests)
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
  })

  test('Retry in the notice reloads a finished helper whose last refresh failed', async () => {
    let failing = false
    const fetchMock = mockApi({ activity: () => (failing ? json({ error: 'boom' }, 500) : json(page([row(1, { detail: 'kept' })]))) })
    const { rerender } = render(view(makeTask()))
    expect(await screen.findByText('kept')).toBeInTheDocument()

    failing = true
    rerender(view(makeTask({ status: 'completed' }))) // the closing read fails
    const retry = await within(await screen.findByRole('status')).findByRole('button', { name: 'Retry' })
    const requests = activityRequests(fetchMock)
    failing = false
    fireEvent.click(retry)
    await waitFor(() => expect(activityRequests(fetchMock)).toBeGreaterThan(requests))
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull())
    expect(screen.getByText('kept')).toBeInTheDocument()
  })

  describe('timers', () => {
    const settle = () => act(async () => { await vi.advanceTimersByTimeAsync(0) })
    const tick = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })
    beforeEach(() => vi.useFakeTimers())

    test('a finished helper is not polled', async () => {
      const fetchMock = mockApi({ activity: () => json(page([row(1)])) })
      render(view(makeTask({ status: 'completed' })))
      await settle()
      const requests = activityRequests(fetchMock)
      expect(requests).toBe(1)
      await tick(9000)
      expect(activityRequests(fetchMock)).toBe(requests)
    })

    test('a running helper is polled', async () => {
      const fetchMock = mockApi({ activity: () => json(page([row(1)])) })
      render(view(makeTask()))
      await settle()
      const requests = activityRequests(fetchMock)
      await tick(3000)
      expect(activityRequests(fetchMock)).toBeGreaterThan(requests)
    })

    test('Last action keeps the live entry that arrived after the read when a later poll returns nothing', async () => {
      mockApi({
        activity: (url) => json(url.includes('after=') ? page([], { after: '20' }) : page([row(1, { event_id: 'd', tool: 'Read' }), row(2, { event_id: 'f', tool: 'Edit' })])),
      })
      const seen = live(1, { eventId: 'd', tool: 'Read' })
      const { rerender } = render(view(makeTask(), { liveActivity: [seen] }))
      await settle()
      rerender(view(makeTask(), { liveActivity: [seen, live(2, { eventId: 'e', tool: 'WebFetch', detail: 'newest' })] }))
      expect(screen.getByText(/Last action: WebFetch/)).toBeInTheDocument()

      await tick(3000)
      expect(screen.getByText(/Last action: WebFetch/)).toBeInTheDocument()
      expect(screen.queryByText(/Last action: Edit/)).toBeNull()
    })

    test('a socket reconnect catches up once, without waiting for the poll', async () => {
      const fetchMock = mockApi({ activity: () => json(page([row(1)])) })
      const { rerender } = render(view(makeTask(), { connected: false }))
      await settle()
      const requests = activityRequests(fetchMock)
      rerender(view(makeTask(), { connected: true }))
      await settle()
      expect(activityRequests(fetchMock)).toBe(requests + 1)
    })

    test('only a running helper is called quiet; a paused one is waiting by design', async () => {
      const tenMinutesAgo = new Date(Date.now() - 10 * 60_000).toISOString()
      mockApi({ activity: () => json(page([row(1, { occurred_at: tenMinutesAgo })])) })
      const { rerender } = render(view(makeTask()))
      await settle()
      expect(screen.getByText(/no new actions for 10m/i)).toBeInTheDocument()

      rerender(view(makeTask({ status: 'paused' })))
      await settle()
      expect(screen.getByText(/last action/i)).toBeInTheDocument()
      expect(screen.queryByText(/no new actions/i)).toBeNull()
    })
  })

  test('a lost socket shows the reconnect notice but keeps the history', async () => {
    mockApi({ activity: () => json(page([row(1, { detail: 'kept' })])) })
    render(view(makeTask(), { connected: false }))

    expect(await screen.findByText('kept')).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent(/live updates paused/i)
    expect(screen.queryByText(/no new actions/i)).toBeNull()
  })

  test('there is no stage composer', async () => {
    mockApi({ activity: () => json(page([row(1)])) })
    render(view(makeTask()))
    await screen.findByText('cmd 1')
    expect(screen.queryByRole('textbox')).toBeNull()
    expect(screen.queryByRole('tab', { name: /message|revise/i })).toBeNull()
  })

  test('keeps the header, Advisory/Required and controls', async () => {
    mockApi()
    render(view(makeTask({ mode: 'required' })))
    expect(screen.getByText('Reviewer')).toBeInTheDocument()
    expect(screen.getByText('Required')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Pause' })).toBeInTheDocument()
  })

  test('switching helpers never shows the previous helper\'s late answer', async () => {
    const pending = new Map<string, (r: Response) => void>()
    const fetchSpy = mockApi({
      activity: (url) => new Promise<Response>((resolve) => { pending.set(url.includes('/t1/') ? 't1' : 't2', resolve) }),
    })
    const { rerender } = render(view(makeTask({ id: 't1' })))
    await waitFor(() => expect(pending.has('t1')).toBe(true))

    rerender(view(makeTask({ id: 't2', title: 'Other' })))
    await waitFor(() => expect(pending.has('t2')).toBe(true))
    expect(screen.queryByText('from t1')).toBeNull()

    await act(async () => { pending.get('t1')?.(json(page([row(1, { task_id: 't1', detail: 'from t1' })]))) })
    expect(screen.queryByText('from t1')).toBeNull()

    await act(async () => { pending.get('t2')?.(json(page([row(1, { task_id: 't2', event_id: 'b1', detail: 'from t2' })]))) })
    expect(await screen.findByText('from t2')).toBeInTheDocument()
    expect(screen.queryByText('from t1')).toBeNull()
    expect(fetchSpy).toHaveBeenCalled()
  })

  test('selecting a different helper while the old one has history shows no stale rows in any commit', async () => {
    mockApi({
      activity: (url) => json(page([row(1, { detail: url.includes('/t1/') ? 'only t1' : 'only t2' })])),
    })
    // onRender runs after each commit, so this sees every DOM state React showed, not only the settled one.
    const commits: string[] = []
    let container: HTMLElement = document.body
    const profiled = (task: AssistantTask) => (
      <Profiler id="view" onRender={() => commits.push(container.textContent ?? '')}>{view(task)}</Profiler>
    )
    const rendered = render(profiled(makeTask({ id: 't1' })))
    container = rendered.container
    expect(await screen.findByText('only t1')).toBeInTheDocument()

    commits.length = 0
    rendered.rerender(profiled(makeTask({ id: 't2' })))
    expect(await screen.findByText('only t2')).toBeInTheDocument()
    expect(commits.length).toBeGreaterThan(1)
    expect(commits.filter((text) => text.includes('only t1'))).toEqual([])
  })

  test('a new helper starts at the bottom even if the previous one was scrolled up', async () => {
    mockApi({ activity: () => json(page([row(1), row(2)])) })
    const { rerender } = render(view(makeTask({ id: 't1' })))
    const region = await screen.findByRole('region', { name: 'Helper activity' })
    Object.defineProperty(region, 'scrollHeight', { configurable: true, get: () => 1000 })
    Object.defineProperty(region, 'clientHeight', { configurable: true, get: () => 300 })
    region.scrollTop = 0
    fireEvent.scroll(region)
    expect(screen.getByRole('button', { name: 'Jump to latest' })).toBeInTheDocument()

    rerender(view(makeTask({ id: 't2' })))
    await screen.findByText('cmd 1')
    expect(screen.queryByRole('button', { name: 'Jump to latest' })).toBeNull()
  })

  test('expanding a long tool row does not lose the feed or hide Jump to latest', async () => {
    mockApi({ activity: () => json(page([row(1, { detail: 'y'.repeat(500) })])) })
    render(view(makeTask()))
    fireEvent.click(await screen.findByRole('button', { name: /show more/i }))
    expect(screen.getByRole('button', { name: /show less/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Jump to latest' })).toBeInTheDocument()
  })

  test('Last action stays the newest action after older pages are loaded', async () => {
    mockApi({
      activity: (url) =>
        url.includes('before=')
          ? json(page([row(1, { tool: 'OldestTool' })], { before: '0', has_older: false }))
          : json(page([row(5, { tool: 'NewestTool' })], { before: '50', has_older: true })),
    })
    render(view(makeTask()))
    expect(await screen.findByText(/^Last action: NewestTool/)).toBeInTheDocument()
    fireEvent.click(await screen.findByRole('button', { name: /load older/i }))
    expect(await screen.findByText('OldestTool')).toBeInTheDocument()
    expect(screen.getByText(/^Last action: NewestTool/)).toBeInTheDocument()
  })

  test('Load older prepends the older page', async () => {
    mockApi({
      activity: (url) =>
        url.includes('before=')
          ? json(page([row(1, { detail: 'older one' })], { before: '0', has_older: false }))
          : json(page([row(5, { detail: 'newest' })], { before: '50', has_older: true })),
    })
    render(view(makeTask()))
    fireEvent.click(await screen.findByRole('button', { name: /load older/i }))
    expect(await screen.findByText('older one')).toBeInTheDocument()
    expect(screen.getByText('Start of activity')).toBeInTheDocument()
  })
})
