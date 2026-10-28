import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { App } from './App'

class StubWebSocket {
  static instances: StubWebSocket[] = []
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  constructor() {
    StubWebSocket.instances.push(this)
  }
  close() {
    /* no-op */
  }
}

const json = (body: unknown, status = 200): Response => new Response(JSON.stringify(body), { status })

const stage = {
  id: 's1', name: 'Propose', status: 'running', updated_at: '', interactive: false, autonomous: false,
  auto_approve: false, has_dialog: false, is_script: false, show_plan: true, show_dialog: false,
}
const wireTask = (id: string, title: string) => ({ id, profile_id: 'reviewer', title, mode: 'advisory', trigger: 'one_shot', status: 'running' })
const activityRow = (taskId: string, detail: string) => ({
  cursor: '10', event_id: `ev-${taskId}`, task_id: taskId, tool: 'Bash', detail, turn: 1, occurred_at: new Date().toISOString(),
})

type Activity = (taskId: string) => Promise<Response> | Response

function mockRun(activity: Activity, requests: string[] = [], runId: () => string = () => 'run-1') {
  return vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const url = typeof input === 'string' ? input : (input as Request).url
    requests.push(url)
    if (url.includes('/api/status')) {
      return json({ flow_name: 'demo', run_id: runId(), stages: [stage], capabilities: { collaboration: true }, accounting: { health: 'ok', has_data: false, show_money: true } })
    }
    if (url.includes('/api/stages/s1/assistants')) return json({ tasks: [wireTask('t1', 'Helper A'), wireTask('t2', 'Helper B')] })
    const match = /\/api\/assistants\/([^/]+)\/activity/.exec(url)
    if (match !== null) return activity(match[1] ?? '')
    if (url.includes('/api/assistants/') && url.includes('/events')) return json({ events: [], next_cursor: 0, has_more: false })
    if (url.includes('/plan')) return new Response('')
    if (url.includes('/api/mesh/')) return json({ error: 'off' }, 404)
    return json([])
  })
}

// The branch of a stage with helpers opens by itself, so the helper rows are there once the poll answered.
async function openHelper(title: string): Promise<void> {
  fireEvent.click(await screen.findByRole('button', { name: new RegExp(title) }, { timeout: 3000 }))
}

describe('App helper activity', () => {
  beforeEach(() => {
    StubWebSocket.instances = []
    vi.stubGlobal('WebSocket', StubWebSocket)
  })
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  test('the selected helper shows its saved history, and a live action over the socket', async () => {
    mockRun((id) => json({ entries: [activityRow(id, `saved by ${id}`)], after: '10', before: '0', has_more: false, has_older: false }))
    render(<App />)
    await openHelper('Helper A')

    expect(await screen.findByText('saved by t1')).toBeInTheDocument()

    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    act(() => {
      ws?.onmessage?.({
        data: JSON.stringify({
          type: 'assistant_activity', stage_id: '',
          data: { event_id: 'live-1', task_id: 't1', tool: 'WebFetch', detail: 'https://example.com/live', turn: 1, occurred_at: new Date().toISOString() },
        }),
      })
    })
    expect(await screen.findByText('https://example.com/live')).toBeInTheDocument()
    expect(screen.queryByRole('textbox')).toBeNull()
  })

  test("another helper's live action is not shown in the selected helper's feed", async () => {
    mockRun((id) => json({ entries: [activityRow(id, `saved by ${id}`)], after: '10', before: '0', has_more: false, has_older: false }))
    render(<App />)
    await openHelper('Helper A')
    await screen.findByText('saved by t1')

    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    act(() => {
      ws?.onmessage?.({
        data: JSON.stringify({
          type: 'assistant_activity', stage_id: '',
          data: { event_id: 'live-b', task_id: 't2', tool: 'Grep', detail: 'only for B', turn: 1, occurred_at: new Date().toISOString() },
        }),
      })
    })
    await act(async () => { await Promise.resolve() })
    expect(screen.queryByText('only for B')).toBeNull()
  })

  test('switching helpers quickly never mixes the previous helper into the new feed', async () => {
    const pending = new Map<string, (r: Response) => void>()
    mockRun((id) => new Promise<Response>((resolve) => { pending.set(id, resolve) }))
    render(<App />)
    await openHelper('Helper A')
    await waitFor(() => expect(pending.has('t1')).toBe(true))

    fireEvent.click(screen.getByRole('button', { name: /Helper B/ }))
    await waitFor(() => expect(pending.has('t2')).toBe(true))

    // A's late answer arrives after B was selected.
    await act(async () => { pending.get('t1')?.(json({ entries: [activityRow('t1', 'late from A')], after: '10', before: '0' })) })
    expect(screen.queryByText('late from A')).toBeNull()

    await act(async () => { pending.get('t2')?.(json({ entries: [activityRow('t2', 'fresh from B')], after: '10', before: '0' })) })
    expect(await screen.findByText('fresh from B')).toBeInTheDocument()
    expect(screen.queryByText('late from A')).toBeNull()
  })

  test('the selected helper view follows the socket state of the app', async () => {
    mockRun((id) => json({ entries: [activityRow(id, `saved by ${id}`)], after: '10', before: '0', has_more: false, has_older: false }))
    render(<App />)
    await openHelper('Helper A')
    await screen.findByText('saved by t1')

    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    act(() => ws?.onopen?.())
    await waitFor(() => expect(screen.queryByText(/live updates paused/i)).toBeNull())

    act(() => ws?.onclose?.())
    expect(await screen.findByText(/live updates paused/i)).toBeInTheDocument()
    expect(screen.getByText('saved by t1')).toBeInTheDocument()
  })

  test('a new run reloads the selected helper from the start instead of continuing the old history', async () => {
    const requests: string[] = []
    let run = 'run-1'
    mockRun((id) => json({ entries: [activityRow(id, `saved by ${id}`)], after: '10', before: '0' }), requests, () => run)
    render(<App />)
    await openHelper('Helper A')
    await screen.findByText('saved by t1')
    const tailRequests = () => requests.filter((u) => u.includes('/api/assistants/t1/activity') && !u.includes('after=')).length
    const before = tailRequests()

    run = 'run-2'
    await waitFor(() => expect(tailRequests()).toBeGreaterThan(before), { timeout: 8000 })
  }, 15000)

  test('helper activity never reaches the stage feed or the status refresh', async () => {
    const requests: string[] = []
    mockRun(() => json({ entries: [], after: '0', before: '0' }), requests)
    render(<App />)
    await screen.findByText('demo')
    const statusBefore = requests.filter((u) => u.includes('/api/status')).length

    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    act(() => {
      ws?.onmessage?.({
        data: JSON.stringify({
          type: 'assistant_activity', stage_id: 's1',
          data: { event_id: 'live-x', task_id: 't1', tool: 'Bash', detail: 'helper chatter', turn: 1, occurred_at: new Date().toISOString() },
        }),
      })
    })
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 30)) })

    expect(screen.queryByText('helper chatter')).toBeNull()
    expect(screen.queryByText(/assistant_activity/)).toBeNull()
    expect(requests.filter((u) => u.includes('/api/status')).length).toBe(statusBefore)
  })
})
