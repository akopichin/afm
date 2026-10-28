import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AssistantActivityEntry } from '../../types/assistant-activity'
import type { MergeContext } from './activity-window'
import { MAX_ACTIVITY_ENTRIES, mergeActivity, useAssistantActivity } from './use-assistant-activity'

// API rows are flat; `cursor` is the offset after the entry's line.
type Row = { cursor: string; event_id: string; task_id: string; tool: string; detail: string; turn: number; occurred_at: string }

const row = (n: number, taskId = 'task-a', over: Partial<Row> = {}): Row => ({
  cursor: String(n * 10),
  event_id: `act-${taskId}-${n}`,
  task_id: taskId,
  tool: 'Bash',
  detail: `cmd ${n}`,
  turn: 1,
  occurred_at: new Date(Date.UTC(2026, 9, 27, 12, 0, n)).toISOString(),
  ...over,
})

const liveEntry = (n: number, taskId = 'task-a', over: Partial<AssistantActivityEntry> = {}): AssistantActivityEntry => ({
  eventId: `act-${taskId}-${n}`,
  taskId,
  participantId: '',
  turn: 1,
  occurredAt: new Date(Date.UTC(2026, 9, 27, 12, 0, n)).toISOString(),
  tool: 'Bash',
  detail: `cmd ${n}`,
  truncated: false,
  cursor: undefined,
  ...over,
})

type PageBody = { entries: Row[]; after: string; before: string; has_more?: boolean; has_older?: boolean }
const page = (entries: Row[], over: Partial<PageBody> = {}): PageBody => ({
  entries,
  after: entries.length > 0 ? entries[entries.length - 1]!.cursor : '0',
  before: '0',
  has_more: false,
  has_older: false,
  ...over,
})
const json = (body: unknown, status = 200): Response => new Response(JSON.stringify(body), { status })

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve: (value: T) => void = () => {}
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

type Props = { taskId: string | null; runId: string; live: AssistantActivityEntry[]; connected: boolean; terminal: boolean }
const base: Props = { taskId: 'task-a', runId: 'run-1', live: [], connected: true, terminal: false }
const mount = (over: Partial<Props> = {}) =>
  renderHook((p: Props) => useAssistantActivity(p.taskId, p.runId, p.live, p.connected, p.terminal), {
    initialProps: { ...base, ...over },
  })

const settle = () => act(async () => { await vi.advanceTimersByTimeAsync(0) })
const tick = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })

let urls: string[]
const mockFetch = (handler: (url: string) => Response | Promise<Response>) => {
  urls = []
  return vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const url = String(input)
    urls.push(url)
    return handler(url)
  })
}
const ids = (entries: AssistantActivityEntry[]) => entries.map((e) => e.eventId)

describe('useAssistantActivity', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('loads the tail and reports ready', async () => {
    mockFetch(() => json(page([row(1), row(2)])))
    const { result } = mount()
    expect(result.current.status).toBe('loading')
    await settle()
    expect(result.current.status).toBe('ready')
    expect(ids(result.current.entries)).toEqual(['act-task-a-1', 'act-task-a-2'])
    expect(urls).toEqual(['/api/assistants/task-a/activity?limit=100'])
  })

  it('does nothing without a selected task', async () => {
    const fetchMock = mockFetch(() => json(page([])))
    const { result } = mount({ taskId: null })
    await settle()
    expect(fetchMock).not.toHaveBeenCalled()
    expect(result.current.entries).toEqual([])
  })

  it('merges live and saved entries by event_id without duplicates', async () => {
    mockFetch(() => json(page([row(1), row(2)])))
    const { result, rerender } = mount()
    await settle()
    rerender({ ...base, live: [liveEntry(2), liveEntry(3)] })
    expect(ids(result.current.entries)).toEqual(['act-task-a-1', 'act-task-a-2', 'act-task-a-3'])
  })

  it('shows live entries of the selected task only, before the first page arrives', () => {
    mockFetch(() => new Promise<Response>(() => {}))
    const live = [liveEntry(1), liveEntry(2, 'task-b')]
    const { result } = mount({ live })
    expect(ids(result.current.entries)).toEqual(['act-task-a-1'])
    expect(result.current.status).toBe('loading')
  })

  it('keeps two identical tool calls as two rows', async () => {
    const same = { tool: 'Read', detail: '/etc/hosts' }
    mockFetch(() => json(page([row(1, 'task-a', same)])))
    const { result, rerender } = mount()
    await settle()
    rerender({ ...base, live: [liveEntry(2, 'task-a', same)] })
    expect(result.current.entries).toHaveLength(2)
    expect(result.current.entries.map((e) => e.detail)).toEqual(['/etc/hosts', '/etc/hosts'])
  })

  it('keeps the first turn on screen while a paused task adds a second turn through polling', async () => {
    mockFetch((url) => (url.includes('after=') ? json(page([row(2, 'task-a', { turn: 2 })])) : json(page([row(1, 'task-a', { turn: 1 })]))))
    // Paused is not terminal, so polling continues after the first turn finished.
    const { result } = mount({ terminal: false })
    await settle()
    await tick(3000)
    expect(result.current.entries.map((e) => [e.eventId, e.turn])).toEqual([
      ['act-task-a-1', 1],
      ['act-task-a-2', 2],
    ])
  })

  it('recovers entries the socket dropped by polling after the saved cursor', async () => {
    mockFetch((url) => (url.includes('after=') ? json(page([row(2), row(3)])) : json(page([row(1)]))))
    // The socket delivered only entry 3; entry 2 was lost while the socket stayed open.
    const { result } = mount({ live: [liveEntry(3)] })
    await settle()
    expect(ids(result.current.entries)).toEqual(['act-task-a-1', 'act-task-a-3'])
    await tick(3000)
    expect(urls[1]).toBe('/api/assistants/task-a/activity?limit=100&after=10')
    expect(ids(result.current.entries)).toEqual(['act-task-a-1', 'act-task-a-2', 'act-task-a-3'])
  })

  it('keeps polling while more pages are waiting', async () => {
    mockFetch((url) => {
      if (!url.includes('after=')) return json(page([row(1)]))
      return url.includes('after=10') ? json(page([row(2)], { has_more: true })) : json(page([row(3)]))
    })
    const { result } = mount()
    await settle()
    await tick(3000)
    expect(urls.slice(1)).toEqual([
      '/api/assistants/task-a/activity?limit=100&after=10',
      '/api/assistants/task-a/activity?limit=100&after=20',
    ])
    expect(result.current.entries).toHaveLength(3)
  })

  it('catches up immediately after a socket reconnect', async () => {
    mockFetch((url) => (url.includes('after=') ? json(page([row(2)])) : json(page([row(1)]))))
    const { result, rerender } = mount()
    await settle()
    rerender({ ...base, connected: false })
    await settle()
    expect(urls).toHaveLength(1)
    rerender({ ...base, connected: true })
    await settle()
    expect(urls).toHaveLength(2)
    expect(ids(result.current.entries)).toEqual(['act-task-a-1', 'act-task-a-2'])
  })

  it('polls once more when the task turns terminal, then stops', async () => {
    mockFetch((url) => (url.includes('after=') ? json(page([row(2)])) : json(page([row(1)]))))
    const { result, rerender } = mount()
    await settle()
    rerender({ ...base, terminal: true })
    await settle()
    expect(urls).toHaveLength(2)
    expect(ids(result.current.entries)).toEqual(['act-task-a-1', 'act-task-a-2'])
    await tick(30000)
    expect(urls).toHaveLength(2)
  })

  it('does not poll a task that is already terminal', async () => {
    mockFetch(() => json(page([row(1)])))
    mount({ terminal: true })
    await settle()
    await tick(30000)
    expect(urls).toHaveLength(1)
  })

  it('ignores a late response of the previous task after a switch', async () => {
    const a = deferred<Response>()
    mockFetch((url) => (url.includes('/task-a/') ? a.promise : json(page([row(1, 'task-b')]))))
    const { result, rerender } = mount()
    rerender({ ...base, taskId: 'task-b' })
    await settle()
    a.resolve(json(page([row(1, 'task-a')])))
    await settle()
    expect(ids(result.current.entries)).toEqual(['act-task-b-1'])
    expect(result.current.status).toBe('ready')
  })

  it('shows nothing of the previous task in any render while the new one loads', async () => {
    const b = deferred<Response>()
    mockFetch((url) => (url.includes('/task-a/') ? json(page([row(1, 'task-a')])) : b.promise))
    const renders: Array<{ taskId: string | null; entries: string[]; status: string }> = []
    const { rerender } = renderHook(
      (p: Props) => {
        const activity = useAssistantActivity(p.taskId, p.runId, p.live, p.connected, p.terminal)
        renders.push({ taskId: p.taskId, entries: ids(activity.entries), status: activity.status })
        return activity
      },
      { initialProps: base },
    )
    await settle()
    expect(renders.some((r) => r.entries.includes('act-task-a-1'))).toBe(true)

    renders.length = 0
    rerender({ ...base, taskId: 'task-b' })
    await settle()
    const ofB = renders.filter((r) => r.taskId === 'task-b')
    expect(ofB.length).toBeGreaterThan(0)
    expect(ofB.every((r) => r.entries.length === 0 && r.status === 'loading')).toBe(true)
  })

  it('drops a late response of the previous run', async () => {
    const first = deferred<Response>()
    let call = 0
    mockFetch(() => (call++ === 0 ? first.promise : json(page([row(5)]))))
    const { result, rerender } = mount({ runId: 'run-1' })
    rerender({ ...base, runId: 'run-2' })
    await settle()
    first.resolve(json(page([row(1)])))
    await settle()
    expect(ids(result.current.entries)).toEqual(['act-task-a-5'])
  })

  it('pages older entries on demand and ends when none are left', async () => {
    mockFetch((url) =>
      url.includes('before=') ? json(page([row(1), row(2)], { before: '0', has_older: false })) : json(page([row(3), row(4)], { before: '20', has_older: true })),
    )
    const { result } = mount()
    await settle()
    expect(result.current.hasOlder).toBe(true)
    act(() => result.current.loadOlder())
    await settle()
    expect(urls[1]).toBe('/api/assistants/task-a/activity?limit=100&before=20')
    expect(ids(result.current.entries)).toEqual(['act-task-a-1', 'act-task-a-2', 'act-task-a-3', 'act-task-a-4'])
    expect(result.current.hasOlder).toBe(false)
    act(() => result.current.loadOlder())
    await settle()
    expect(urls).toHaveLength(2)
  })

  it('walks older pages through the cursor each page returned', async () => {
    mockFetch((url) => {
      if (url.includes('before=20')) return json(page([row(3), row(4)], { before: '10', has_older: true }))
      if (url.includes('before=10')) return json(page([row(1), row(2)], { before: '0', has_older: false }))
      return json(page([row(5), row(6)], { before: '20', has_older: true }))
    })
    const { result } = mount()
    await settle()
    act(() => result.current.loadOlder())
    await settle()
    act(() => result.current.loadOlder())
    await settle()
    expect(urls.filter((u) => u.includes('before='))).toEqual([
      '/api/assistants/task-a/activity?limit=100&before=20',
      '/api/assistants/task-a/activity?limit=100&before=10',
    ])
    expect(ids(result.current.entries)).toEqual(['act-task-a-1', 'act-task-a-2', 'act-task-a-3', 'act-task-a-4', 'act-task-a-5', 'act-task-a-6'])
    expect(result.current.hasOlder).toBe(false)
  })

  it('does not duplicate rows when an older page overlaps what is loaded', async () => {
    mockFetch((url) =>
      url.includes('before=') ? json(page([row(2), row(3)], { has_older: false })) : json(page([row(3), row(4)], { before: '20', has_older: true })),
    )
    const { result } = mount()
    await settle()
    act(() => result.current.loadOlder())
    await settle()
    expect(ids(result.current.entries)).toEqual(['act-task-a-2', 'act-task-a-3', 'act-task-a-4'])
  })

  it('reloads the tail when the server rejects the older cursor', async () => {
    let tails = 0
    mockFetch((url) => {
      if (url.includes('before=')) return json({ error: 'invalid_cursor' }, 400)
      tails++
      return json(page([tails === 1 ? row(3) : row(9)], { before: '20', has_older: true }))
    })
    const { result } = mount()
    await settle()
    act(() => result.current.loadOlder())
    await settle()
    expect(tails).toBe(2)
    expect(ids(result.current.entries)).toEqual(['act-task-a-9'])
    expect(result.current.loadingOlder).toBe(false)
    expect(result.current.refreshFailing).toBe(false)
  })

  it('ignores an older page that arrives after the task was switched', async () => {
    const older = deferred<Response>()
    mockFetch((url) => {
      if (url.includes('before=')) return older.promise
      return json(page([row(5, url.includes('/task-b/') ? 'task-b' : 'task-a')], { before: '20', has_older: true }))
    })
    const { result, rerender } = mount()
    await settle()
    act(() => result.current.loadOlder())
    rerender({ ...base, taskId: 'task-b' })
    await settle()
    older.resolve(json(page([row(1, 'task-a')])))
    await settle()
    expect(ids(result.current.entries)).toEqual(['act-task-b-5'])
  })

  describe('bounded window', () => {
    const range = (from: number, to: number) => Array.from({ length: to - from }, (_, i) => row(from + i))
    const numbers = (entries: AssistantActivityEntry[]) => entries.map((e) => Number(e.eventId.split('-').at(-1)))
    const contiguous = (list: number[]) => list.every((n, i) => i === 0 || n === list[i - 1]! + 1)
    const limitOf = (url: string) => Number(new URL(url, 'http://x').searchParams.get('limit'))

    it('trims the oldest rows when forward polling outgrows the cap and says older history exists', async () => {
      const tail = range(1, MAX_ACTIVITY_ENTRIES)
      mockFetch((url) => (url.includes('after=') ? json(page(range(MAX_ACTIVITY_ENTRIES, MAX_ACTIVITY_ENTRIES + 3))) : json(page(tail))))
      const { result } = mount()
      await settle()
      expect(result.current.hasOlder).toBe(false)
      await tick(3000)
      expect(result.current.entries).toHaveLength(MAX_ACTIVITY_ENTRIES)
      expect(result.current.entries[0]?.eventId).toBe('act-task-a-3')
      expect(result.current.hasOlder).toBe(true)
      expect(result.current.olderCapped).toBe(true)
    })

    it('hides a ring entry without a saved neighbour once a trim moves the start of the window past it', async () => {
      const tail = range(0, MAX_ACTIVITY_ENTRIES)
      mockFetch((url) => (url.includes('after=') ? json(page(range(MAX_ACTIVITY_ENTRIES, MAX_ACTIVITY_ENTRIES + 100))) : json(page(tail))))
      const { result, rerender } = mount()
      await settle()
      rerender({ ...base, live: [liveEntry(9000), liveEntry(1500)] }) // 9000 was never saved
      expect(ids(result.current.entries)).toContain('act-task-a-9000')
      await tick(3000)
      expect(result.current.entries).toHaveLength(MAX_ACTIVITY_ENTRIES)
      expect(ids(result.current.entries)).not.toContain('act-task-a-9000')
    })

    it('puts an entry that arrived while a trimming forward read was in flight at the end of the trimmed rows', async () => {
      const forward = deferred<Response>()
      const tail = range(0, MAX_ACTIVITY_ENTRIES)
      mockFetch((url) => (url.includes('after=') ? forward.promise : json(page(tail))))
      const { result, rerender } = mount()
      await settle()
      await tick(3000)
      rerender({ ...base, live: [liveEntry(9000)] })
      forward.resolve(json(page(range(MAX_ACTIVITY_ENTRIES, MAX_ACTIVITY_ENTRIES + 100))))
      await settle()
      expect(numbers(result.current.entries).at(-1)).toBe(9000)
      expect(result.current.entries).toHaveLength(MAX_ACTIVITY_ENTRIES + 1)
    })

    it('does not page older once the bound is reached', async () => {
      mockFetch(() => json(page(range(0, MAX_ACTIVITY_ENTRIES), { before: '5', has_older: true })))
      const { result } = mount()
      await settle()
      expect(result.current.hasOlder).toBe(true)
      expect(result.current.olderCapped).toBe(true)
      act(() => result.current.loadOlder())
      await settle()
      expect(urls).toHaveLength(1)
    })

    it('asks only for as many older rows as still fit under the bound', async () => {
      mockFetch((url) =>
        url.includes('before=') ? json(page(range(50, 100), { before: '400', has_older: true })) : json(page(range(100, 2050), { before: '900', has_older: true })),
      )
      const { result } = mount()
      await settle()
      expect(result.current.olderCapped).toBe(false)
      act(() => result.current.loadOlder())
      await settle()
      expect(urls[1]).toBe('/api/assistants/task-a/activity?limit=50&before=900')
      expect(result.current.entries).toHaveLength(MAX_ACTIVITY_ENTRIES)
      expect(result.current.olderCapped).toBe(true)
    })

    it('keeps one contiguous window when a forward trim lands while an older page is in flight', async () => {
      const older = deferred<Response>()
      let inFlight = 0
      let maxInFlight = 0
      mockFetch(async (url) => {
        inFlight++
        maxInFlight = Math.max(maxInFlight, inFlight)
        try {
          if (url.includes('before=')) return await older.promise
          if (url.includes('after=')) return json(page(range(2050, 2150)))
          return json(page(range(100, 2050), { before: '1000', has_older: true }))
        } finally {
          inFlight--
        }
      })
      const { result } = mount()
      await settle()
      expect(result.current.entries).toHaveLength(1950)
      act(() => result.current.loadOlder())
      await tick(3000) // the poll tick only queues a forward read behind the older one
      expect(urls.filter((u) => u.includes('after='))).toHaveLength(0)
      older.resolve(json(page(range(50, 100).slice(-limitOf(urls[1]!)), { before: '500', has_older: true })))
      await settle()
      const list = numbers(result.current.entries)
      expect(contiguous(list)).toBe(true)
      expect(list.length).toBeLessThanOrEqual(MAX_ACTIVITY_ENTRIES)
      expect(list.at(-1)).toBe(2149)
      expect(maxInFlight).toBe(1)
    })
  })

  describe('one request at a time', () => {
    it('a poll tick while Load older is pending issues no second request, and the forward read follows once', async () => {
      const older = deferred<Response>()
      mockFetch((url) => {
        if (url.includes('before=')) return older.promise
        if (url.includes('after=')) return json(page([row(5)]))
        return json(page([row(3), row(4)], { before: '20', has_older: true }))
      })
      const { result } = mount()
      await settle()
      act(() => result.current.loadOlder())
      expect(result.current.loadingOlder).toBe(true)
      await tick(9000) // three poll ticks
      expect(urls).toHaveLength(2)
      older.resolve(json(page([row(1), row(2)], { has_older: false })))
      await settle()
      expect(urls).toHaveLength(3)
      expect(urls[2]).toContain('after=40')
      expect(ids(result.current.entries)).toEqual(['act-task-a-1', 'act-task-a-2', 'act-task-a-3', 'act-task-a-4', 'act-task-a-5'])
    })

    it('Load older clicked while a poll is in flight waits for it and is not lost', async () => {
      const forward = deferred<Response>()
      mockFetch((url) => {
        if (url.includes('before=')) return json(page([row(1)], { has_older: false }))
        if (url.includes('after=')) return forward.promise
        return json(page([row(2), row(3)], { before: '10', has_older: true }))
      })
      const { result } = mount()
      await settle()
      await tick(3000) // the poll hangs
      act(() => result.current.loadOlder())
      expect(result.current.loadingOlder).toBe(true)
      expect(urls).toHaveLength(2)
      forward.resolve(json(page([row(4)])))
      await settle()
      expect(urls[2]).toContain('before=10')
      expect(ids(result.current.entries)).toEqual(['act-task-a-1', 'act-task-a-2', 'act-task-a-3', 'act-task-a-4'])
      expect(result.current.loadingOlder).toBe(false)
    })

    it('runs queued work as tail, then older; a queued forward read is covered by the tail', async () => {
      const forward = deferred<Response>()
      let tails = 0
      mockFetch((url) => {
        if (url.includes('before=')) return json(page([row(1)], { has_older: false }))
        if (url.includes('after=')) return forward.promise
        tails++
        return json(page([row(tails === 1 ? 5 : 9)], { before: '40', has_older: true }))
      })
      const { result } = mount()
      await settle()
      await tick(3000) // forward hangs
      act(() => result.current.loadOlder())
      await tick(3000) // a second forward is queued behind it
      forward.resolve(json({ error: 'invalid_cursor' }, 400))
      await settle()
      expect(urls.map((u) => (u.includes('before=') ? 'older' : u.includes('after=') ? 'forward' : 'tail'))).toEqual(['tail', 'forward', 'tail', 'older'])
      expect(ids(result.current.entries)).toEqual(['act-task-a-1', 'act-task-a-9'])
    })

    it('runs Load older before the poll that was queued ahead of it finishes the round', async () => {
      const forward = deferred<Response>()
      let forwards = 0
      mockFetch((url) => {
        if (url.includes('before=')) return json(page([row(1)], { has_older: false }))
        if (url.includes('after=')) return forwards++ === 0 ? forward.promise : json(page([row(5)]))
        return json(page([row(2), row(3)], { before: '10', has_older: true }))
      })
      const { result } = mount()
      await settle()
      await tick(3000) // the first poll hangs
      await tick(3000) // the next tick queues a forward read behind it
      act(() => result.current.loadOlder())
      forward.resolve(json(page([row(4)])))
      await settle()
      expect(urls.map((u) => (u.includes('before=') ? 'older' : u.includes('after=') ? 'forward' : 'tail'))).toEqual(['tail', 'forward', 'older', 'forward'])
    })

    it('a 400 on the older request while a poll is queued still reloads the tail', async () => {
      const older = deferred<Response>()
      let tails = 0
      mockFetch((url) => {
        if (url.includes('before=')) return older.promise
        if (url.includes('after=')) return json(page([row(7)]))
        tails++
        return json(page([tails === 1 ? row(3) : row(9)], { before: '20', has_older: true }))
      })
      const { result } = mount()
      await settle()
      act(() => result.current.loadOlder())
      await tick(3000)
      older.resolve(json({ error: 'invalid_cursor' }, 400))
      await settle()
      expect(tails).toBe(2)
      expect(urls.some((u) => u.includes('after='))).toBe(false)
      expect(ids(result.current.entries)).toEqual(['act-task-a-9'])
      expect(result.current.loadingOlder).toBe(false)
    })
  })

  describe('live entries around the window', () => {
    it('shows a live entry that arrived while the tail was loading and is not in it', async () => {
      const first = deferred<Response>()
      mockFetch(() => first.promise)
      const { result, rerender } = mount()
      rerender({ ...base, live: [liveEntry(9)] })
      first.resolve(json(page([row(1), row(2)], { before: '5', has_older: true })))
      await settle()
      expect(ids(result.current.entries)).toEqual(['act-task-a-1', 'act-task-a-2', 'act-task-a-9'])
    })

    it('hides a ring entry before the tail window and shows it once Load older brings its neighbour', async () => {
      mockFetch((url) =>
        url.includes('before=') ? json(page([row(1)], { has_older: false })) : json(page([row(3)], { before: '20', has_older: true })),
      )
      const live = [liveEntry(1), liveEntry(2), liveEntry(3)] // 2 was never saved
      const { result } = mount({ live })
      await settle()
      expect(ids(result.current.entries)).toEqual(['act-task-a-3'])
      act(() => result.current.loadOlder())
      await settle()
      expect(ids(result.current.entries)).toEqual(['act-task-a-1', 'act-task-a-2', 'act-task-a-3'])
    })

    it('puts a new unsaved entry after the last saved one when the ring holds several saved entries', async () => {
      mockFetch(() => json(page([row(1), row(2), row(3)])))
      const { result, rerender } = mount()
      await settle()
      rerender({ ...base, live: [liveEntry(1), liveEntry(2), liveEntry(3), liveEntry(4)] })
      expect(ids(result.current.entries)).toEqual(['act-task-a-1', 'act-task-a-2', 'act-task-a-3', 'act-task-a-4'])
    })

    it('keeps an unsaved entry after the live end when a later saved one arrives and the poll confirms it', async () => {
      mockFetch((url) => (url.includes('after=') ? json(page([row(4)])) : json(page([row(1), row(2)], { before: '5', has_older: true }))))
      const { result, rerender } = mount()
      await settle()
      rerender({ ...base, live: [liveEntry(3), liveEntry(4)] }) // 3 was never saved
      expect(ids(result.current.entries)).toEqual(['act-task-a-1', 'act-task-a-2', 'act-task-a-3', 'act-task-a-4'])
      await tick(3000)
      expect(ids(result.current.entries)).toEqual(['act-task-a-1', 'act-task-a-2', 'act-task-a-3', 'act-task-a-4'])
    })

    it('puts an entry that arrived after the tail was requested at the end, behind a row the socket dropped', async () => {
      // D is in the ring; F was saved (the socket dropped it) and the tail read returned it before E was emitted.
      mockFetch(() => json(page([row(4), row(6)])))
      const { result, rerender } = mount({ live: [liveEntry(4)] })
      await settle()
      rerender({ ...base, live: [liveEntry(4), liveEntry(5)] })
      expect(ids(result.current.entries)).toEqual(['act-task-a-4', 'act-task-a-6', 'act-task-a-5'])
    })

    it('keeps the entry that arrived after the tail was requested in file order once a forward poll saves it', async () => {
      mockFetch((url) => (url.includes('after=') ? json(page([row(5)])) : json(page([row(4), row(6)]))))
      const { result, rerender } = mount({ live: [liveEntry(4)] })
      await settle()
      rerender({ ...base, live: [liveEntry(4), liveEntry(5)] })
      await tick(3000)
      expect(ids(result.current.entries)).toEqual(['act-task-a-4', 'act-task-a-6', 'act-task-a-5'])
    })

    it('puts an entry that arrived while the tail was in flight after the tail rows', async () => {
      const first = deferred<Response>()
      mockFetch(() => first.promise)
      const { result, rerender } = mount({ live: [liveEntry(4)] })
      rerender({ ...base, live: [liveEntry(4), liveEntry(5)] })
      first.resolve(json(page([row(4), row(6)])))
      await settle()
      expect(ids(result.current.entries)).toEqual(['act-task-a-4', 'act-task-a-6', 'act-task-a-5'])
    })

    it('puts an entry that arrived while a forward read was in flight after that read\'s rows', async () => {
      const forward = deferred<Response>()
      mockFetch((url) => (url.includes('after=') ? forward.promise : json(page([row(4)]))))
      const { result, rerender } = mount({ live: [liveEntry(4)] })
      await settle()
      await tick(3000)
      rerender({ ...base, live: [liveEntry(4), liveEntry(5)] })
      forward.resolve(json(page([row(6)])))
      await settle()
      expect(ids(result.current.entries)).toEqual(['act-task-a-4', 'act-task-a-6', 'act-task-a-5'])
    })

    it('keeps an unsaved entry that was in the ring at the tail request next to its anchor', async () => {
      mockFetch(() => json(page([row(4), row(6)])))
      const { result } = mount({ live: [liveEntry(4), liveEntry(5)] }) // 5 was emitted before the read, never saved
      await settle()
      expect(ids(result.current.entries)).toEqual(['act-task-a-4', 'act-task-a-5', 'act-task-a-6'])
    })
  })

  describe('catching up', () => {
    it('a finished task reads a multi-page backlog without waiting for the interval', async () => {
      mockFetch((url) => {
        if (url.includes('after=10')) return json(page([row(2)], { has_more: true }))
        if (url.includes('after=20')) return json(page([row(3)], { has_more: true }))
        if (url.includes('after=30')) return json(page([row(4)]))
        return json(page([row(1)]))
      })
      const { result, rerender } = mount()
      await settle()
      rerender({ ...base, terminal: true })
      await settle()
      expect(ids(result.current.entries)).toEqual(['act-task-a-1', 'act-task-a-2', 'act-task-a-3', 'act-task-a-4'])
    })

    it('after a very long backlog it reloads the tail instead of paging on', async () => {
      let tails = 0
      let forwards = 0
      mockFetch((url) => {
        if (url.includes('after=')) {
          forwards++
          return json(page([row(100 + forwards)], { has_more: forwards < 60 }))
        }
        tails++
        return json(page([row(tails === 1 ? 1 : 999)], { before: '10', has_older: true }))
      })
      const { result, rerender } = mount()
      await settle()
      rerender({ ...base, terminal: true })
      await settle()
      expect(tails).toBe(2)
      expect(forwards).toBe(20)
      expect(ids(result.current.entries)).toEqual(['act-task-a-999'])
    })
  })

  describe('catch-up streak', () => {
    const backlog = (url: string, forwards: number, hasMore: boolean) =>
      url.includes('after=') ? json(page([row(100 + forwards)], { has_more: hasMore })) : json(page([row(1)]))

    it('a short page ends the count: 19 full pages, a short one, then full pages again never reload the tail', async () => {
      let forwards = 0
      let tails = 0
      // Forward reads 1-19 are full, 20 is short, 21 is full, 22 is short.
      mockFetch((url) => {
        if (!url.includes('after=')) tails++
        else forwards++
        return backlog(url, forwards, forwards <= 19 || forwards === 21)
      })
      const { result } = mount()
      await settle()
      act(() => result.current.retry())
      await settle()
      expect(forwards).toBe(20)
      act(() => result.current.retry())
      await settle()
      expect(forwards).toBe(22)
      expect(tails).toBe(1)
    })

    it('a tail reload restarts the count', async () => {
      let forwards = 0
      let tails = 0
      // Forward reads 1-20 are full and end in a tail reload; the reads after it are full up to 39.
      mockFetch((url) => {
        if (!url.includes('after=')) tails++
        else forwards++
        return backlog(url, forwards, forwards < 40)
      })
      const { result } = mount()
      await settle()
      act(() => result.current.retry())
      await settle()
      expect(tails).toBe(2)
      act(() => result.current.retry())
      await settle()
      expect(forwards).toBe(40)
      expect(tails).toBe(2)
    })
  })

  describe('stale responses', () => {
    it('drops a forward page that arrives after the task was switched', async () => {
      const forward = deferred<Response>()
      mockFetch((url) => {
        if (url.includes('after=')) return forward.promise
        return json(page([row(1, url.includes('/task-b/') ? 'task-b' : 'task-a')]))
      })
      const { result, rerender } = mount()
      await settle()
      await tick(3000)
      rerender({ ...base, taskId: 'task-b' })
      await settle()
      forward.resolve(json(page([row(2, 'task-a')])))
      await settle()
      expect(ids(result.current.entries)).toEqual(['act-task-b-1'])
    })
  })

  it('marks a 404 as unavailable and stops periodic polling', async () => {
    mockFetch(() => json({ error: 'not_found' }, 404))
    const { result } = mount()
    await settle()
    expect(result.current.status).toBe('unavailable')
    await tick(30000)
    expect(urls).toHaveLength(1)
  })

  it('keeps loaded entries when a refresh fails and recovers on the next poll', async () => {
    let failing = false
    mockFetch((url) => {
      if (!url.includes('after=')) return json(page([row(1)]))
      return failing ? json({}, 500) : json(page([row(2)]))
    })
    const { result } = mount()
    await settle()
    failing = true
    await tick(3000)
    expect(result.current.status).toBe('ready')
    expect(result.current.refreshFailing).toBe(true)
    expect(ids(result.current.entries)).toEqual(['act-task-a-1'])
    failing = false
    await tick(3000)
    expect(result.current.refreshFailing).toBe(false)
    expect(ids(result.current.entries)).toEqual(['act-task-a-1', 'act-task-a-2'])
  })

  it('retries after a 404 when the socket reconnects', async () => {
    let missing = true
    mockFetch(() => (missing ? json({ error: 'not_found' }, 404) : json(page([row(1)]))))
    const { result, rerender } = mount()
    await settle()
    expect(result.current.status).toBe('unavailable')
    missing = false
    rerender({ ...base, connected: false })
    await settle()
    rerender({ ...base, connected: true })
    await settle()
    expect(result.current.status).toBe('ready')
    expect(result.current.entries).toHaveLength(1)
  })

  it('retry() clears an unavailable state', async () => {
    let missing = true
    mockFetch(() => (missing ? json({ error: 'not_found' }, 404) : json(page([row(1)]))))
    const { result } = mount()
    await settle()
    expect(result.current.status).toBe('unavailable')
    missing = false
    act(() => result.current.retry())
    await settle()
    expect(result.current.status).toBe('ready')
  })

  it('retries after a 404 once the task turns terminal', async () => {
    let missing = true
    mockFetch(() => (missing ? json({ error: 'not_found' }, 404) : json(page([row(1)]))))
    const { result, rerender } = mount()
    await settle()
    missing = false
    rerender({ ...base, terminal: true })
    await settle()
    expect(result.current.status).toBe('ready')
  })

  it('retry() repeats the first load of a finished task whose first page failed', async () => {
    let failing = true
    mockFetch(() => (failing ? json({}, 500) : json(page([row(1)]))))
    const { result } = mount({ terminal: true })
    await settle()
    expect(result.current.status).toBe('error')
    await tick(30000)
    expect(urls).toHaveLength(1) // a finished task is not polled
    failing = false
    act(() => result.current.retry())
    await settle()
    expect(result.current.status).toBe('ready')
    expect(result.current.entries).toHaveLength(1)
  })

  it('keeps the live entries and reports a failing refresh when the first page failed', async () => {
    mockFetch(() => json({}, 500))
    const { result } = mount({ live: [liveEntry(1)], terminal: true })
    await settle()
    expect(result.current.status).toBe('error')
    expect(ids(result.current.entries)).toEqual(['act-task-a-1'])
    expect(result.current.refreshFailing).toBe(true)
  })

  it('does not report a failing refresh while a failed first page leaves nothing on screen', async () => {
    mockFetch(() => json({}, 500))
    const { result } = mount({ terminal: true })
    await settle()
    expect(result.current.status).toBe('error')
    expect(result.current.refreshFailing).toBe(false)
  })

  it('reports an error when the first page fails and retries it', async () => {
    let failing = true
    mockFetch(() => (failing ? json({}, 500) : json(page([row(1)]))))
    const { result } = mount()
    await settle()
    expect(result.current.status).toBe('error')
    failing = false
    await tick(3000)
    expect(result.current.status).toBe('ready')
    expect(result.current.entries).toHaveLength(1)
  })

  it('reloads the tail when the server rejects the saved cursor', async () => {
    let tails = 0
    mockFetch((url) => {
      if (url.includes('after=')) return json({ error: 'invalid_cursor' }, 400)
      tails++
      return json(page([tails === 1 ? row(1) : row(7)]))
    })
    const { result } = mount()
    await settle()
    await tick(3000)
    expect(tails).toBe(2)
    expect(ids(result.current.entries)).toEqual(['act-task-a-7'])
  })

  it('does not overlap requests: a trigger during a running one is replayed once', async () => {
    const slow = deferred<Response>()
    let call = 0
    mockFetch(() => (call++ === 0 ? json(page([row(1)])) : call === 2 ? slow.promise : json(page([row(3)]))))
    mount()
    await settle()
    await tick(3000) // poll starts and hangs
    await tick(3000) // second poll tick and a third one while the first is in flight
    expect(urls).toHaveLength(2)
    slow.resolve(json(page([row(2)])))
    await settle()
    expect(urls).toHaveLength(3)
  })
})

describe('mergeActivity', () => {
  const saved = (n: number) => ({ ...liveEntry(n), cursor: String(n * 10) })
  // Every live entry counts as present when the saved rows were read; `over` sets the rest of the context.
  const merge = (rows: AssistantActivityEntry[], live: AssistantActivityEntry[], over: Partial<MergeContext> = {}) =>
    mergeActivity(rows, live, { atStart: true, unplaced: new Set(), seen: new Map(live.map((e) => [e.eventId, { from: null, to: rows.at(-1)?.eventId ?? null }])), ...over })
  const at = (iso: string, id: string, cursor?: string): AssistantActivityEntry => ({ ...liveEntry(1), eventId: id, occurredAt: iso, cursor, detail: id })

  it('returns the saved entries when nothing is live', () => {
    const rows = [saved(1)]
    expect(merge(rows, [])).toBe(rows)
  })

  it('returns the saved entries when the live ones are all saved too', () => {
    const rows = [saved(1), saved(2)]
    expect(merge(rows, [liveEntry(1), liveEntry(2)])).toBe(rows)
  })

  it('keeps an unsaved entry in front of the next saved one within the same millisecond', () => {
    const F = at('2026-10-27T12:00:00.123456789Z', 'F', '100')
    const E = at('2026-10-27T12:00:00.123000001Z', 'E')
    expect(ids(merge([F], [E, F]))).toEqual(['E', 'F'])
  })

  it('follows the stream order when the wall clock stepped back', () => {
    const D = at('2026-10-27T12:00:10Z', 'D', '50')
    const E = at('2026-10-27T12:00:11Z', 'E')
    const F = at('2026-10-27T12:00:05Z', 'F', '100')
    expect(ids(merge([D, F], [D, E, F]))).toEqual(['D', 'E', 'F'])
  })

  it('puts an unsaved entry right after the saved one it followed, before saved rows the socket never carried', () => {
    const D = at('2026-10-27T12:00:01Z', 'D', '10')
    const X = at('2026-10-27T12:00:02Z', 'X', '20')
    const F = at('2026-10-27T12:00:03Z', 'F', '30')
    const E = at('2026-10-27T12:00:04Z', 'E')
    expect(ids(merge([D, X, F], [D, E, F]))).toEqual(['D', 'E', 'X', 'F'])
  })

  it('keeps several unsaved entries after one anchor in their stream order', () => {
    const D = at('2026-10-27T12:00:01Z', 'D', '10')
    const F = at('2026-10-27T12:00:09Z', 'F', '30')
    const E1 = at('2026-10-27T12:00:08Z', 'E1')
    const E2 = at('2026-10-27T12:00:02Z', 'E2')
    expect(ids(merge([D, F], [D, E1, E2, F]))).toEqual(['D', 'E1', 'E2', 'F'])
  })

  it('hides an unsaved entry that precedes the loaded window until an older page brings its anchor', () => {
    const D = at('2026-10-27T12:00:01Z', 'D', '10')
    const E = at('2026-10-27T12:00:02Z', 'E')
    const F = at('2026-10-27T12:00:03Z', 'F', '30')
    const unplaced = { atStart: false, unplaced: new Set(['D', 'E']) }
    expect(ids(merge([F], [D, E, F], unplaced))).toEqual(['F'])
    expect(ids(merge([D, F], [D, E, F], unplaced))).toEqual(['D', 'E', 'F'])
  })

  it('shows an unsaved entry first when the history starts after it', () => {
    const known = { unplaced: new Set(['act-task-a-1']) }
    expect(ids(merge([saved(5)], [liveEntry(1), saved(5)], known))).toEqual(['act-task-a-1', 'act-task-a-5'])
  })

  it('shows entries that arrived after the window was loaded, in front of the first saved live one', () => {
    const A = at('2026-10-27T12:00:01Z', 'A', '10')
    const B = at('2026-10-27T12:00:02Z', 'B', '20')
    const E = at('2026-10-27T12:00:03Z', 'E')
    const F = at('2026-10-27T12:00:04Z', 'F', '40')
    expect(ids(merge([A, B, F], [E, F], { atStart: false }))).toEqual(['A', 'B', 'E', 'F'])
  })

  it('appends fresh unsaved entries when no live entry is saved yet', () => {
    const A = at('2026-10-27T12:00:01Z', 'A', '10')
    const E = at('2026-10-27T12:00:03Z', 'E')
    expect(ids(merge([A], [E], { atStart: false }))).toEqual(['A', 'E'])
  })

  it('does not append ring entries that were already there when the window loaded and have no anchor', () => {
    const A = at('2026-10-27T12:00:05Z', 'A', '50')
    const stale = at('2026-10-27T12:00:01Z', 'stale')
    expect(ids(merge([A], [stale], { atStart: false, unplaced: new Set(['stale']) }))).toEqual(['A'])
  })

  it('puts a new unsaved entry after the last saved one when the ring holds several saved entries', () => {
    const rows = [saved(1), saved(2), saved(3)]
    const merged = merge(rows, [liveEntry(1), liveEntry(2), liveEntry(3), liveEntry(4)])
    expect(ids(merged)).toEqual(['act-task-a-1', 'act-task-a-2', 'act-task-a-3', 'act-task-a-4'])
  })

  it('puts an unsaved entry that arrived after the read behind every saved row', () => {
    const D = at('2026-10-27T12:00:01Z', 'D', '10')
    const E = at('2026-10-27T12:00:02Z', 'E')
    const F = at('2026-10-27T12:00:03Z', 'F', '30')
    const afterRead = { atStart: true, unplaced: new Set<string>(), seen: new Map([['D', { from: null, to: 'D' }]]) }
    expect(ids(mergeActivity([D, F], [D, E], afterRead))).toEqual(['D', 'F', 'E'])
  })

  it('keeps several entries that arrived after the read in live order, once each', () => {
    const D = at('2026-10-27T12:00:01Z', 'D', '10')
    const E1 = at('2026-10-27T12:00:02Z', 'E1')
    const E2 = at('2026-10-27T12:00:03Z', 'E2')
    const F = at('2026-10-27T12:00:04Z', 'F', '40')
    const afterRead = { atStart: false, unplaced: new Set<string>(), seen: new Map([['D', { from: null, to: 'D' }]]) }
    expect(ids(mergeActivity([D, F], [D, E1, E2, E1], afterRead))).toEqual(['D', 'F', 'E1', 'E2'])
  })

  it('shows entries that arrived after the read even when no saved row is loaded yet', () => {
    const E1 = at('2026-10-27T12:00:02Z', 'E1')
    const E2 = at('2026-10-27T12:00:03Z', 'E2')
    const none = { atStart: false, unplaced: new Set<string>(), seen: new Map() }
    expect(ids(mergeActivity([], [E1, E2], none))).toEqual(['E1', 'E2'])
  })

  it('places read-time ring entries by their anchor and later ones at the end', () => {
    const D = at('2026-10-27T12:00:01Z', 'D', '10')
    const E = at('2026-10-27T12:00:02Z', 'E')
    const F = at('2026-10-27T12:00:03Z', 'F', '30')
    const G = at('2026-10-27T12:00:04Z', 'G')
    const mixed = { atStart: true, unplaced: new Set<string>(), seen: new Map([['D', { from: null, to: 'F' }], ['E', { from: null, to: 'F' }]]) }
    expect(ids(mergeActivity([D, F], [D, E, G], mixed))).toEqual(['D', 'E', 'F', 'G'])
  })

  describe('first read of an unsaved entry', () => {
    const X = at('2026-10-27T12:00:02Z', 'X', '20')
    const Y = at('2026-10-27T12:00:03Z', 'Y', '30')
    const Z = at('2026-10-27T12:00:04Z', 'Z', '40')
    const E = at('2026-10-27T12:00:05Z', 'E')
    const ctx = (seen: [string, { from: string | null; to: string | null }][], over: Partial<MergeContext> = {}) =>
      ({ atStart: true, unplaced: new Set<string>(), seen: new Map(seen), ...over })

    it('puts the entry after the row of its first read when that is later than its saved predecessor', () => {
      const D = at('2026-10-27T12:00:01Z', 'D', '10')
      expect(ids(mergeActivity([D, X, Z], [D, E], ctx([['E', { from: 'X', to: 'Z' }]])))).toEqual(['D', 'X', 'E', 'Z'])
    })

    it('never puts the entry past its saved live successor', () => {
      const D = at('2026-10-27T12:00:01Z', 'D', '10')
      expect(ids(mergeActivity([D, X, Z], [D, E, Z], ctx([['E', { from: 'Z', to: 'Z' }]])))).toEqual(['D', 'X', 'E', 'Z'])
    })

    it('puts an entry first seen by an empty read as late as that read allows', () => {
      expect(ids(mergeActivity([X, Y, Z], [E], ctx([['E', { from: null, to: 'Y' }]])))).toEqual(['X', 'Y', 'E', 'Z'])
    })

    it('keeps an entry first seen by an empty read before a later entry with a first-read row', () => {
      const E2 = at('2026-10-27T12:00:06Z', 'E2')
      const seen: [string, { from: string | null; to: string | null }][] = [['E', { from: null, to: 'Y' }], ['E2', { from: 'Y', to: 'Z' }]]
      expect(ids(mergeActivity([X, Y, Z], [E, E2], ctx(seen)))).toEqual(['X', 'Y', 'E', 'E2', 'Z'])
    })

    it('hides an entry whose first-read row left the window while older rows may exist', () => {
      const rows = [Y]
      const lost = ctx([['E', { from: 'Q', to: 'Y' }]], { atStart: false, unplaced: new Set(['E']) })
      expect(mergeActivity(rows, [E], lost)).toBe(rows)
    })

    it('shows that entry first when the window starts the history', () => {
      expect(ids(mergeActivity([Y], [E], ctx([['E', { from: 'Q', to: 'Y' }]])))).toEqual(['E', 'Y'])
    })
  })

  it('collapses a repeated live event_id', () => {
    expect(ids(merge([], [liveEntry(1), liveEntry(1)]))).toEqual(['act-task-a-1'])
  })
})

// Order of unsaved live entries across reads: seen-by-read records are set once and never move an entry back.
const nums = (entries: AssistantActivityEntry[]) => entries.map((e) => Number(e.eventId.split('-').at(-1)))
const L = (...n: number[]) => n.map((x) => liveEntry(x))

// Tail and forward reads are answered from queues; an empty forward queue answers an empty page.
function serve(tails: PageBody[], forwards: (PageBody | number)[]) {
  return vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const url = String(input)
    if (url.includes('after=')) {
      const next = forwards.shift()
      if (typeof next === 'number') return json({ error: 'x' }, next)
      const cursor = new URL(url, 'http://x').searchParams.get('after')!
      return json(next ?? page([], { after: cursor }))
    }
    return json(tails.shift() ?? page([]))
  })
}

describe('order sequences', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })

  it('S1 verify-2: an empty poll keeps E after F', async () => {
    serve([page([row(4), row(6)])], [])
    const h = mount({ live: L(4) })
    await settle()
    h.rerender({ ...base, live: L(4, 5) })
    expect(nums(h.result.current.entries)).toEqual([4, 6, 5])
    await tick(3000)
    expect(nums(h.result.current.entries)).toEqual([4, 6, 5])
  })

  it('S2 repeated empty polls keep E last', async () => {
    serve([page([row(4), row(6)])], [])
    const h = mount({ live: L(4) })
    await settle()
    h.rerender({ ...base, live: L(4, 5) })
    await tick(3000); await tick(3000); await tick(3000)
    expect(nums(h.result.current.entries)).toEqual([4, 6, 5])
  })

  it('S3 a later poll returns a row the socket dropped (X=7): E stays before X', async () => {
    serve([page([row(4), row(6)])], [page([], { after: '60' }), page([row(7)])])
    const h = mount({ live: L(4) })
    await settle()
    h.rerender({ ...base, live: L(4, 5) })
    await tick(3000)
    await tick(3000)
    expect(nums(h.result.current.entries)).toEqual([4, 6, 5, 7])
  })

  it('S4 a later live row X=7 is saved by a poll: E before X', async () => {
    serve([page([row(4), row(6)])], [page([], { after: '60' }), page([row(7)])])
    const h = mount({ live: L(4) })
    await settle()
    h.rerender({ ...base, live: L(4, 5) })
    await tick(3000)
    h.rerender({ ...base, live: L(4, 5, 7) })
    await tick(3000)
    expect(nums(h.result.current.entries)).toEqual([4, 6, 5, 7])
  })

  it('S5 E saved late keeps file order', async () => {
    serve([page([row(4), row(6)])], [page([], { after: '60' }), page([row(5)])])
    const h = mount({ live: L(4) })
    await settle()
    h.rerender({ ...base, live: L(4, 5) })
    await tick(3000); await tick(3000)
    expect(nums(h.result.current.entries)).toEqual([4, 6, 5])
  })

  it('S6 race: E and its live successor N reach the socket after the tail that already returned N; E precedes N', async () => {
    serve([page([row(1), row(3)])], [])
    const h = mount({ live: [] })
    await settle()
    h.rerender({ ...base, live: L(2, 3) }) // 2 unsaved, 3 saved; the socket order is 2, 3
    expect(nums(h.result.current.entries)).toEqual([1, 2, 3])
    await tick(3000)
    expect(nums(h.result.current.entries)).toEqual([1, 2, 3])
  })

  it('S7 tail reload (400) while E pending keeps E after F when F is still in the window', async () => {
    serve([page([row(4), row(6)]), page([row(4), row(6), row(7)])], [page([], { after: '60' }), 400])
    const h = mount({ live: L(4) })
    await settle()
    h.rerender({ ...base, live: L(4, 5) })
    await tick(3000) // empty poll
    await tick(3000) // 400 -> tail reload
    await settle()
    expect(nums(h.result.current.entries)).toEqual([4, 6, 5, 7])
  })

  it('S8 a poll that returns later rows but not E, many times: E never moves back to the end', async () => {
    serve([page([row(4), row(6)])], [page([], { after: '60' }), page([row(7)]), page([row(8)]), page([row(9)])])
    const h = mount({ live: L(4) })
    await settle()
    h.rerender({ ...base, live: L(4, 5) })
    for (let i = 0; i < 4; i++) await tick(3000)
    expect(nums(h.result.current.entries)).toEqual([4, 6, 5, 7, 8, 9])
  })

  it('S9 page-load: an unsaved ring entry with no saved neighbour is the latest action of a short history', async () => {
    serve([page([row(1), row(2)])], [])
    const h = mount({ live: L(9) })
    await settle()
    expect(nums(h.result.current.entries)).toEqual([1, 2, 9])
  })

  it('S10 trim: E floored on a row that stays in the window stays visible after it', async () => {
    const range = (a: number, b: number) => Array.from({ length: b - a }, (_, i) => row(a + i))
    serve([page(range(0, MAX_ACTIVITY_ENTRIES))], [page([], { after: String((MAX_ACTIVITY_ENTRIES - 1) * 10) }), page(range(MAX_ACTIVITY_ENTRIES, MAX_ACTIVITY_ENTRIES + 100))])
    const h = mount({ live: [] })
    await settle()
    h.rerender({ ...base, live: L(5000) }) // unsaved, after row 1999
    await tick(3000) // empty poll pins it after 1999
    await tick(3000) // trim by 100
    const list = nums(h.result.current.entries)
    expect(list).toContain(5000)
    expect(list.indexOf(5000)).toBe(list.indexOf(1999) + 1)
  })

  it('S11 monotonic: a floorless tail-ring entry stays before a later floored one', async () => {
    serve([page([row(1)])], [page([row(4), row(5)])])
    const h = mount({ live: L(2) }) // 2 in the tail ring, unsaved
    await settle()
    h.rerender({ ...base, live: L(2, 3) }) // 3 unsaved, after the tail
    await tick(3000) // poll (ring [2,3]) returns 4 (dropped) and 5
    h.rerender({ ...base, live: L(2, 3, 5) })
    const list = nums(h.result.current.entries)
    expect(list.indexOf(2)).toBeLessThan(list.indexOf(3))
    expect(list.indexOf(3)).toBeLessThan(list.indexOf(5))
  })
})

describe('more sequences', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })

  it('A1 ambiguous: E and a dropped saved Y between the same two polls -> E before Y (documented choice)', async () => {
    serve([page([row(4), row(6)])], [page([row(7)])])
    const h = mount({ live: L(4) })
    await settle()
    h.rerender({ ...base, live: L(4, 5) })
    await tick(3000)
    expect(nums(h.result.current.entries)).toEqual([4, 6, 5, 7])
  })

  it('A2 E arrives while a forward read is in flight: after that read rows, before the next read rows', async () => {
    let release: (r: Response) => void = () => {}
    let n = 0
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = String(input)
      if (!url.includes('after=')) return json(page([row(4)]))
      n++
      if (n === 1) return new Promise<Response>((r) => { release = r })
      return json(page([row(8)]))
    })
    const h = mount({ live: L(4) })
    await settle()
    await tick(3000)
    h.rerender({ ...base, live: L(4, 5) })
    release(json(page([row(6)])))
    await settle()
    expect(nums(h.result.current.entries)).toEqual([4, 6, 5])
    await tick(3000)
    expect(nums(h.result.current.entries)).toEqual([4, 6, 5, 8])
  })

  it('A3 multi-page catch-up while E pending: E stays after F, before every page', async () => {
    serve([page([row(4), row(6)])], [page([row(7)], { has_more: true }), page([row(8)])])
    const h = mount({ live: L(4) })
    await settle()
    h.rerender({ ...base, live: L(4, 5) })
    await tick(3000)
    await settle()
    expect(nums(h.result.current.entries)).toEqual([4, 6, 5, 7, 8])
  })

  it('A4 trim removes the floor row and the predecessor: E is hidden, not moved to the end', async () => {
    const range = (a: number, b: number) => Array.from({ length: b - a }, (_, i) => row(a + i))
    serve([page(range(0, 10))], [page([], { after: '90' }), page(range(10, MAX_ACTIVITY_ENTRIES + 20))])
    const h = mount({ live: [] })
    await settle()
    h.rerender({ ...base, live: L(5000) })
    await tick(3000)
    await tick(3000)
    expect(nums(h.result.current.entries)).not.toContain(5000)
    expect(nums(h.result.current.entries).at(-1)).toBe(MAX_ACTIVITY_ENTRIES + 19)
  })

  it('A5 Load older while E pending keeps E after F', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = String(input)
      if (url.includes('before=')) return json(page([row(1), row(2)], { has_older: false }))
      if (url.includes('after=')) return json(page([], { after: '60' }))
      return json(page([row(4), row(6)], { before: '30', has_older: true }))
    })
    const h = mount({ live: L(4) })
    await settle()
    h.rerender({ ...base, live: L(4, 5) })
    await tick(3000)
    act(() => h.result.current.loadOlder())
    await settle()
    expect(nums(h.result.current.entries)).toEqual([1, 2, 4, 6, 5])
  })

  it('A6 tail ring entry with a saved predecessor keeps anchor placement (premise)', async () => {
    serve([page([row(4), row(6)])], [])
    const h = mount({ live: L(4, 5) })
    await settle()
    expect(nums(h.result.current.entries)).toEqual([4, 5, 6])
    await tick(3000)
    expect(nums(h.result.current.entries)).toEqual([4, 5, 6])
  })

  it('S12 switch away and back keeps E after F', async () => {
    serve([page([row(4), row(6)]), page([row(1)]), page([row(4), row(6)])], [])
    const h = mount({ live: L(4) })
    await settle()
    h.rerender({ ...base, live: L(4, 5) })
    await tick(3000)
    h.rerender({ ...base, taskId: 'task-b', live: L(4, 5) })
    await settle()
    h.rerender({ ...base, taskId: 'task-a', live: L(4, 5) })
    await settle()
    expect(nums(h.result.current.entries)).toEqual([4, 6, 5])
  })
})

describe('floorless entries', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })

  it('S13 no saved rows yet: E seen by an empty read stays before a row a later read returns', async () => {
    serve([page([])], [page([], { after: '0' }), page([row(7)])])
    const h = mount({ live: [] })
    await settle()
    h.rerender({ ...base, live: L(5) })
    await tick(3000)
    await tick(3000)
    expect(nums(h.result.current.entries)).toEqual([5, 7])
  })

  it('S14 a tail-ring entry with no saved neighbour stays before rows a later read returns', async () => {
    serve([page([row(1), row(2)])], [page([row(3)])])
    const h = mount({ live: L(9) })
    await settle()
    expect(nums(h.result.current.entries)).toEqual([1, 2, 9])
    await tick(3000)
    expect(nums(h.result.current.entries)).toEqual([1, 2, 9, 3])
  })
})

describe('remount', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })

  it('S15 switching away and back before the first tail applied keeps the page-load placement', async () => {
    serve([page([row(1)]), page([row(1)]), page([row(1), row(2)])], [])
    const h = mount({ live: L(9) })
    h.rerender({ ...base, taskId: 'task-b', live: L(9) })
    h.rerender({ ...base, taskId: 'task-a', live: L(9) })
    await settle()
    expect(nums(h.result.current.entries)).toEqual([1, 2, 9])
  })

  it('S16 E arrives while another helper is selected: back on A it follows the rows A already had', async () => {
    serve([page([row(4), row(6)]), page([]), page([row(4), row(6)])], [])
    const h = mount({ live: L(4) })
    await settle()
    h.rerender({ ...base, taskId: 'task-b', live: L(4) })
    await settle()
    h.rerender({ ...base, taskId: 'task-b', live: L(4, 5) })
    h.rerender({ ...base, taskId: 'task-a', live: L(4, 5) })
    await settle()
    expect(nums(h.result.current.entries)).toEqual([4, 6, 5])
  })
})
