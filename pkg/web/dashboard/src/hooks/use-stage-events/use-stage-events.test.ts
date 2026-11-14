import { act, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { useStageEvents } from './use-stage-events'
import type { AfmEvent } from '../../types'

const ev = (stage: string, seq: number): AfmEvent => ({
  type: 'agent_action',
  stageId: stage,
  payload: {},
  timestamp: new Date(seq).toISOString(),
  seq,
})

// Отложенный промис — для тестов гонки, где нужно управлять моментом резолва
// fetch() руками (как resolveFetch в use-event-feed.test.ts, но именованно).
function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve: (value: T) => void = () => {}
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

describe('useStageEvents', () => {
  it('returns the fetched per-stage history merged with live global events', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify([{ type: 'agent_action', stage_id: 'early', seq: 1, timestamp: '1970-01-01T00:00:00.001Z' }])),
    )
    const live = [ev('early', 2), ev('noise', 9)]
    const { result } = renderHook(() => useStageEvents('early', live, 'run-a'))
    await waitFor(() => expect(result.current.length).toBe(2)) // seq1 (history) + seq2 (live), noise excluded
    expect(result.current.every((e) => e.stageId === 'early')).toBe(true)
    expect(fetchMock).toHaveBeenCalledWith('/api/events?stage=early')
  })

  it('returns [] and does not fetch when stageId is null', () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
    const { result } = renderHook(() => useStageEvents(null, [], 'run-a'))
    expect(result.current).toEqual([])
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('ignores a stale response after the stage changed', async () => {
    // Первая стадия ('a') резолвится ПОСЛЕ переключения на вторую ('b') — ответ для 'a' должен быть
    // отброшен (generation-ref), история должна остаться той, что уже пришла для 'b'.
    const a = deferred<Response>()
    const b = deferred<Response>()
    vi.spyOn(globalThis, 'fetch').mockImplementation((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/events?stage=a') return a.promise
      if (url === '/api/events?stage=b') return b.promise
      throw new Error(`unexpected fetch url: ${url}`)
    })

    const { result, rerender } = renderHook(({ stageId }) => useStageEvents(stageId, [], 'run-a'), {
      initialProps: { stageId: 'a' as string | null },
    })

    rerender({ stageId: 'b' })

    await act(async () => {
      b.resolve(new Response(JSON.stringify([{ type: 'agent_action', stage_id: 'b', seq: 10, timestamp: '2026-01-01T00:00:00.000Z' }])))
      await b.promise
    })
    await waitFor(() => expect(result.current.length).toBe(1))
    expect(result.current[0]?.stageId).toBe('b')

    await act(async () => {
      a.resolve(new Response(JSON.stringify([{ type: 'agent_action', stage_id: 'a', seq: 1, timestamp: '2026-01-01T00:00:00.000Z' }])))
      await a.promise
    })
    // Стейл-ответ для 'a' не должен был подмешаться — всё ещё только запись 'b'.
    expect(result.current.length).toBe(1)
    expect(result.current[0]?.stageId).toBe('b')
  })

  it('appends live global events for the stage AFTER the history fetch resolves', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify([{ type: 'agent_action', stage_id: 's1', seq: 1, timestamp: '2026-01-01T00:00:00.001Z' }])),
    )

    const { result, rerender } = renderHook(({ globalEvents }: { globalEvents: AfmEvent[] }) => useStageEvents('s1', globalEvents, 'run-a'), {
      initialProps: { globalEvents: [] as AfmEvent[] },
    })

    await waitFor(() => expect(result.current.length).toBe(1))

    rerender({ globalEvents: [ev('s1', 1), ev('s1', 2)] })

    await waitFor(() => expect(result.current.length).toBe(2))
    expect(result.current.map((e) => e.seq)).toEqual([1, 2])
  })

  it('shows the live-filtered tail while the fetch is still pending (no blank flash)', () => {
    vi.spyOn(globalThis, 'fetch').mockReturnValue(new Promise<Response>(() => {})) // никогда не резолвится
    const live = [ev('s1', 1)]
    const { result } = renderHook(() => useStageEvents('s1', live, 'run-a'))
    expect(result.current.length).toBe(1)
    expect(result.current[0]?.stageId).toBe('s1')
  })

  it('dedupes an event present in BOTH fetched history and the live stream', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify([{ type: 'agent_action', stage_id: 's1', seq: 5, timestamp: '2026-01-01T00:00:00.005Z' }])),
    )
    const live = [ev('s1', 5)]
    const { result } = renderHook(() => useStageEvents('s1', live, 'run-a'))
    await waitFor(() => expect(result.current.length).toBe(1))
  })

  it('does not call setHistory after stageId flips to null mid-flight', async () => {
    const pending = deferred<Response>()
    vi.spyOn(globalThis, 'fetch').mockReturnValue(pending.promise)

    const { result, rerender } = renderHook(({ stageId }) => useStageEvents(stageId, [], 'run-a'), {
      initialProps: { stageId: 's1' as string | null },
    })

    rerender({ stageId: null })
    expect(result.current).toEqual([])

    await act(async () => {
      pending.resolve(
        new Response(JSON.stringify([{ type: 'agent_action', stage_id: 's1', seq: 1, timestamp: '2026-01-01T00:00:00.001Z' }])),
      )
      await pending.promise
    })
    // gen бампнут при переключении на null — поздний резолв должен быть отброшен.
    expect(result.current).toEqual([])
  })

  // Записываем каждый render, включая кадр до effect: история прежней стадии и чужие события не должны
  // попасть в новую.
  it('filters out events for a different stage from a mixed fetch response', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(
        JSON.stringify([
          { type: 'agent_action', stage_id: 'early', seq: 1, timestamp: '2026-01-01T00:00:00.001Z' },
          { type: 'agent_action', stage_id: 'other', seq: 2, timestamp: '2026-01-01T00:00:00.002Z' },
        ]),
      ),
    )
    const { result } = renderHook(() => useStageEvents('early', [], 'run-a'))
    await waitFor(() => expect(result.current.length).toBe(1))
    expect(result.current.every((e) => e.stageId === 'early')).toBe(true)
  })

  it('caps a single stage feed at 2000 events', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify([])))
    // Больше кэпа (MAX_STAGE_EVENTS=2000) live-событий ОДНОЙ стадии — лента должна
    // обрезаться до последних 2000, самые старые отбрасываются.
    const live = Array.from({ length: 2200 }, (_, i) => ev('s1', i))
    const { result } = renderHook(() => useStageEvents('s1', live, 'run-a'))
    await waitFor(() => expect(result.current.length).toBe(2000))
    expect(result.current[0]?.seq).toBe(200)
    expect(result.current[1999]?.seq).toBe(2199)
  })

  it('does not stitch the previous stage history onto the new stage during the switch (review round 1, Important)', async () => {
    const captured: AfmEvent[][] = []
    function useTracked(stageId: string | null, globalEvents: AfmEvent[]): AfmEvent[] {
      const value = useStageEvents(stageId, globalEvents, 'run-a')
      captured.push(value)
      return value
    }

    vi.spyOn(globalThis, 'fetch').mockImplementation((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/events?stage=a') {
        return Promise.resolve(
          new Response(JSON.stringify([{ type: 'agent_action', stage_id: 'a', seq: 1, timestamp: '2026-01-01T00:00:00.001Z' }])),
        )
      }
      if (url === '/api/events?stage=b') return new Promise<Response>(() => {}) // никогда не резолвится в этом тесте
      throw new Error(`unexpected fetch url: ${url}`)
    })

    const { rerender } = renderHook(({ stageId, globalEvents }) => useTracked(stageId, globalEvents), {
      initialProps: { stageId: 'a' as string | null, globalEvents: [] as AfmEvent[] },
    })

    await waitFor(() => expect(captured[captured.length - 1]?.length).toBe(1))
    expect(captured[captured.length - 1]?.[0]?.stageId).toBe('a')

    captured.length = 0 // нас интересуют только рендеры ПОСЛЕ переключения на 'b'

    rerender({ stageId: 'b', globalEvents: [ev('b', 5)] })

    // Ни один зафиксированный рендер (включая самый первый — ДО того, как эффект для 'b' успел хоть что-то
    // сделать: его фетч зависает навечно) не должен содержать событие стадии 'a'.
    expect(captured.length).toBeGreaterThan(0)
    for (const snapshot of captured) {
      expect(snapshot.some((e) => e.stageId === 'a')).toBe(false)
    }
  })

  it('a new run refetches the stage history and shows only its own events, even for the same stage and seq', async () => {
    let served = 'old'
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () =>
      new Response(JSON.stringify([{ type: served === 'old' ? 'agent_action' : 'approved', stage_id: 's1', seq: 1, timestamp: '2026-01-01T00:00:00.000Z' }])),
    )
    const { result, rerender } = renderHook(({ runId, globalEvents }) => useStageEvents('s1', globalEvents, runId), {
      initialProps: { runId: 'run-a', globalEvents: [] as AfmEvent[] },
    })
    await waitFor(() => expect(result.current.map((e) => e.type)).toEqual(['agent_action']))

    served = 'new'
    rerender({ runId: 'run-b', globalEvents: [] })
    expect(result.current).toEqual([]) // run A's history is not shown for run B while B's loads
    await waitFor(() => expect(result.current.map((e) => e.type)).toEqual(['approved']))
  })

  it('ignores a late history response of the previous run for the same stage', async () => {
    const a = deferred<Response>()
    const b = deferred<Response>()
    let calls = 0
    vi.spyOn(globalThis, 'fetch').mockImplementation(() => (++calls === 1 ? a.promise : b.promise))
    const { result, rerender } = renderHook(({ runId }) => useStageEvents('s1', [], runId), { initialProps: { runId: 'run-a' } })
    rerender({ runId: 'run-b' })

    await act(async () => {
      b.resolve(new Response(JSON.stringify([{ type: 'approved', stage_id: 's1', seq: 1, timestamp: '2026-01-01T00:00:00.000Z' }])))
      await b.promise
    })
    await waitFor(() => expect(result.current).toHaveLength(1))

    await act(async () => {
      a.resolve(new Response(JSON.stringify([{ type: 'agent_action', stage_id: 's1', seq: 1, timestamp: '2026-01-01T00:00:00.000Z' }])))
      await a.promise
    })
    expect(result.current.map((e) => e.type)).toEqual(['approved'])
  })

  it("the first run id ('' → run) does not refetch or drop the loaded history", async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify([{ type: 'agent_action', stage_id: 's1', seq: 1, timestamp: '2026-01-01T00:00:00.000Z' }])),
    )
    const { result, rerender } = renderHook(({ runId }) => useStageEvents('s1', [], runId), { initialProps: { runId: '' } })
    await waitFor(() => expect(result.current).toHaveLength(1))

    rerender({ runId: 'run-a' })

    expect(result.current).toHaveLength(1)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('keeps only the last 2000 events of the stage from history, in order', async () => {
    const raw = Array.from({ length: 2100 }, (_, i) => ({ type: 'agent_action', stage_id: 's1', seq: i + 1, timestamp: '2026-01-01T00:00:00.000Z' }))
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify([...raw, { type: 'agent_action', stage_id: 'other', seq: 5000 }])))
    const { result } = renderHook(() => useStageEvents('s1', [], 'run-a'))

    await waitFor(() => expect(result.current).toHaveLength(2000))

    expect(result.current[0]?.seq).toBe(101)
    expect(result.current[1999]?.seq).toBe(2100)
  })

  it('does not re-append live events that were truncated out of the stage history', async () => {
    const raw = Array.from({ length: 2100 }, (_, i) => ({ type: 'agent_action', stage_id: 's1', seq: i + 1, timestamp: '2026-01-01T00:00:00.000Z' }))
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify(raw)))
    const live = Array.from({ length: 2100 }, (_, i) => ev('s1', i + 1))
    const { result } = renderHook(() => useStageEvents('s1', live, 'run-a'))

    await waitFor(() => expect(result.current[0]?.seq).toBe(101))

    expect(result.current).toHaveLength(2000)
    expect(result.current[1999]?.seq).toBe(2100)
  })

  it('returns the same array when only other stages gain events', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify([])))
    const own = ev('s1', 1)
    const { result, rerender } = renderHook(({ events }) => useStageEvents('s1', events, 'run-a'), {
      initialProps: { events: [own, ev('noise', 2)] },
    })
    await act(async () => {})
    const before = result.current

    rerender({ events: [own, ev('noise', 2), ev('noise', 3)] })

    expect(result.current).toBe(before)

    rerender({ events: [own, ev('noise', 2), ev('noise', 3), ev('s1', 4)] })

    expect(result.current).not.toBe(before)
    expect(result.current.map((e) => e.seq)).toEqual([1, 4])
  })
  describe('positional merge of a truncated history with the live window', () => {
    const rawSeq = (from: number, to: number) =>
      Array.from({ length: to - from + 1 }, (_, i) => ({ type: 'agent_action', stage_id: 's1', seq: from + i, timestamp: '2026-01-01T00:00:00.000Z' }))
    const rawNoSeq = (from: number, to: number) =>
      Array.from({ length: to - from + 1 }, (_, i) => ({ type: 'agent_action', stage_id: 's1', data: { n: from + i }, timestamp: '2026-01-01T00:00:00.000Z' }))
    const liveSeq = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => ev('s1', from + i))
    const liveNoSeq = (from: number, to: number): AfmEvent[] =>
      Array.from({ length: to - from + 1 }, (_, i) => ({ type: 'agent_action', stageId: 's1', payload: { n: from + i }, timestamp: '2026-01-01T00:00:00.000Z' }))
    const nOf = (e: AfmEvent | undefined) => (e?.payload as { n: number } | undefined)?.n

    it('T1: history of the newest 2000 plus the full live window yields exactly those 2000, in order', async () => {
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify(rawSeq(1001, 3000))))
      const live = liveSeq(1, 3000)
      const { result } = renderHook(() => useStageEvents('s1', live, 'run-a'))
      await waitFor(() => expect(result.current[0]?.seq).toBe(1001))
      expect(result.current).toHaveLength(2000)
      expect(result.current[1999]?.seq).toBe(3000)
      const seqs = result.current.map((e) => e.seq as number)
      expect(seqs.every((s, i) => i === 0 || s > (seqs[i - 1] as number))).toBe(true)
    })

    it('T1: same for events without seq', async () => {
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify(rawNoSeq(1001, 3000))))
      const live = liveNoSeq(1, 3000)
      const { result } = renderHook(() => useStageEvents('s1', live, 'run-a'))
      await waitFor(() => expect(nOf(result.current[0])).toBe(1001))
      expect(result.current).toHaveLength(2000)
      expect(nOf(result.current[1999])).toBe(3000)
    })

    it('T2: converts only the retained tail of a huge seq-less history', async () => {
      let converted = 0
      const raw = Array.from({ length: 30000 }, (_, n) => ({
        type: 'agent_action',
        stage_id: 's1',
        timestamp: '2026-01-01T00:00:00.000Z',
        get data() {
          converted++
          return { n }
        },
      }))
      vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: true, json: async () => raw } as unknown as Response)
      const { result } = renderHook(() => useStageEvents('s1', [], 'run-a'))
      await waitFor(() => expect(result.current).toHaveLength(2000))
      expect(converted).toBeLessThanOrEqual(2000)
      expect(nOf(result.current[1999])).toBe(29999)
    })

    it('T3: a lagging live window never re-appends events older than the first covered one', async () => {
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify(rawSeq(1001, 3000))))
      const { result, rerender } = renderHook(({ globalEvents }) => useStageEvents('s1', globalEvents, 'run-a'), {
        initialProps: { globalEvents: liveSeq(1, 2950) },
      })
      await waitFor(() => expect(result.current[0]?.seq).toBe(1001))
      expect(result.current).toHaveLength(2000)
      expect(result.current[1999]?.seq).toBe(3000)

      rerender({ globalEvents: liveSeq(1, 3005) })
      expect(result.current).toHaveLength(2000)
      expect(result.current[0]?.seq).toBe(1006)
      expect(result.current[1999]?.seq).toBe(3005)
    })

    it('F15: an out-of-order seq-less status keeps history order and live-only runs after history', async () => {
      const t = '2026-01-01T00:00:00.000Z'
      const rawStatus = { type: 'stage_status_changed', stage_id: 's1', data: { status: 'running' }, timestamp: t }
      const liveStatus: AfmEvent = { type: 'stage_status_changed', stageId: 's1', payload: { status: 'running' }, timestamp: t }
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify([...rawNoSeq(1004, 2964), rawStatus, ...rawNoSeq(2965, 3000)])))
      const live = [...liveNoSeq(1304, 3193), liveStatus, ...liveNoSeq(3194, 3300)]
      const { result } = renderHook(() => useStageEvents('s1', live, 'run-a'))
      await waitFor(() => expect(nOf(result.current[result.current.length - 1])).toBe(3300))
      const order = result.current.map((e) => (e.type === 'stage_status_changed' ? 'S' : nOf(e)))
      const pos = order.indexOf('S')
      expect(order.slice(pos + 1)).toEqual(Array.from({ length: 3300 - 2965 + 1 }, (_, i) => 2965 + i))
    })

    it('F16: a seq-less status high in history but first in live keeps older live actions before history', async () => {
      const t = '2026-01-01T00:00:00.000Z'
      const rawStatus = { type: 'stage_status_changed', stage_id: 's1', data: { status: 'running' }, timestamp: t }
      const liveStatus: AfmEvent = { type: 'stage_status_changed', stageId: 's1', payload: { status: 'running' }, timestamp: t }
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify([...rawNoSeq(3001, 3212), rawStatus, ...rawNoSeq(3213, 3300)])))
      const live = [liveStatus, ...liveNoSeq(2832, 3300)]
      const { result } = renderHook(() => useStageEvents('s1', live, 'run-a'))
      await waitFor(() => expect(nOf(result.current[result.current.length - 1])).toBe(3300))
      const order = result.current.map((e) => (e.type === 'stage_status_changed' ? 'S' : nOf(e)))
      const pos = order.indexOf('S')
      expect(order.slice(pos)).toEqual(['S', ...Array.from({ length: 3300 - 3213 + 1 }, (_, i) => 3213 + i)])
      const nums = order.filter((x): x is number => x !== 'S')
      expect(nums[0]).toBe(2832)
      expect(nums.every((n, i) => i === 0 || n > (nums[i - 1] as number))).toBe(true)
    })

    it('F17: a single outlier anchor keeps live-only events in the tail after history', async () => {
      const t = '2026-01-01T00:00:00.000Z'
      const rawStatus = { type: 'stage_status_changed', stage_id: 's1', data: { status: 'running' }, timestamp: t }
      const liveStatus: AfmEvent = { type: 'stage_status_changed', stageId: 's1', payload: { status: 'running' }, timestamp: t }
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify([...rawNoSeq(1004, 2964), rawStatus, ...rawNoSeq(2965, 3000)])))
      const live = [...liveNoSeq(3002, 3193), liveStatus, ...liveNoSeq(3194, 5000)]
      const { result } = renderHook(() => useStageEvents('s1', live, 'run-a'))
      // Live-only events are present before history arrives; wait for the merged result.
      await waitFor(() => {
        const order = result.current.map((e) => (e.type === 'stage_status_changed' ? 'S' : nOf(e)))
        expect(order).toEqual([3000, ...Array.from({ length: 5000 - 3002 + 1 }, (_, i) => 3002 + i)])
      })
      expect(result.current).toHaveLength(2000)
    })

    it('F18: two shared status events do not displace live-only events from the tail', async () => {
      const t = '2026-01-01T00:00:00.000Z'
      const raw = (status: string) => ({ type: 'stage_status_changed', stage_id: 's1', data: { status }, timestamp: t })
      const live_ = (status: string): AfmEvent => ({ type: 'stage_status_changed', stageId: 's1', payload: { status }, timestamp: t })
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(
        new Response(JSON.stringify([...rawNoSeq(1003, 2964), raw('paused'), raw('running'), ...rawNoSeq(2965, 3000)])),
      )
      const live = [...liveNoSeq(3003, 3193), live_('paused'), live_('running'), ...liveNoSeq(3194, 5000)]
      const { result } = renderHook(() => useStageEvents('s1', live, 'run-a'))
      // Live-only events are present before history arrives; wait for the merged result.
      await waitFor(() => {
        const order = result.current.map((e) => (e.type === 'stage_status_changed' ? 'S' : nOf(e)))
        expect(order).toEqual([2999, 3000, ...Array.from({ length: 5000 - 3003 + 1 }, (_, i) => 3003 + i)])
      })
      expect(result.current).toHaveLength(2000)
    })

    it('F19: a single genuine action anchor keeps older live actions before history', async () => {
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify(rawNoSeq(1001, 3000))))
      const live = liveNoSeq(1, 1001)
      const { result } = renderHook(() => useStageEvents('s1', live, 'run-a'))
      await waitFor(() => expect(nOf(result.current[result.current.length - 1])).toBe(3000))
      expect(result.current.map(nOf)).toEqual(Array.from({ length: 2000 }, (_, i) => 1001 + i))
    })

    it('F13: an old shared status event does not move older live actions after history', async () => {
      const rawStatus = { type: 'stage_status_changed', stage_id: 's1', seq: 1000000, data: { status: 'running' }, timestamp: '2026-01-01T00:00:00.000Z' }
      const liveStatus: AfmEvent = { type: 'stage_status_changed', stageId: 's1', payload: { status: 'running' }, timestamp: '2026-01-01T00:00:00.000Z', seq: 1000000 }
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify([rawStatus, ...rawSeq(1301, 3300)])))
      const live = [liveStatus, ...liveSeq(1, 3300)]
      const { result } = renderHook(() => useStageEvents('s1', live, 'run-a'))
      await waitFor(() => expect(result.current.some((e) => e.seq === 1301)).toBe(true))
      const seqs = result.current.filter((e) => e.type === 'agent_action').map((e) => e.seq as number)
      expect(seqs[0]).toBe(1301)
      expect(seqs).toHaveLength(2000 - 1 + (result.current.some((e) => e.type === 'stage_status_changed') ? 0 : 1))
      expect(seqs[seqs.length - 1]).toBe(3300)
      expect(seqs.every((s, i) => i === 0 || s > (seqs[i - 1] as number))).toBe(true)
    })
  })

  it('adds run-level budget notices to every stage feed, but not stage-less run agent notices', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('[]'))
    const notice = (scope: string, stageId: string, receipt: number): AfmEvent => ({
      type: 'budget_warning', stageId, payload: { scope }, timestamp: new Date(receipt).toISOString(), receipt,
    })
    const live = [ev('a', 1), notice('run', '', 2), notice('task:t1', '', 3), notice('stage:b', 'b', 4)]
    const { result } = renderHook(() => useStageEvents('a', live, 'run-a'))
    await waitFor(() => expect(result.current.map((e) => e.receipt ?? e.seq)).toEqual([1, 2]))
  })
})
