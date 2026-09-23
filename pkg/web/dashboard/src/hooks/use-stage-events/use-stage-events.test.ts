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
    const { result } = renderHook(() => useStageEvents('early', live))
    await waitFor(() => expect(result.current.length).toBe(2)) // seq1 (history) + seq2 (live), noise excluded
    expect(result.current.every((e) => e.stageId === 'early')).toBe(true)
    expect(fetchMock).toHaveBeenCalledWith('/api/events?stage=early')
  })

  it('returns [] and does not fetch when stageId is null', () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
    const { result } = renderHook(() => useStageEvents(null, []))
    expect(result.current).toEqual([])
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('ignores a stale response after the stage changed', async () => {
    // Первая стадия ('a') резолвится ПОСЛЕ переключения на вторую ('b') —
    // ответ для 'a' должен быть отброшен (generation-ref), история должна
    // остаться той, что уже пришла для 'b'.
    const a = deferred<Response>()
    const b = deferred<Response>()
    vi.spyOn(globalThis, 'fetch').mockImplementation((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/events?stage=a') return a.promise
      if (url === '/api/events?stage=b') return b.promise
      throw new Error(`unexpected fetch url: ${url}`)
    })

    const { result, rerender } = renderHook(({ stageId }) => useStageEvents(stageId, []), {
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

    const { result, rerender } = renderHook(({ globalEvents }: { globalEvents: AfmEvent[] }) => useStageEvents('s1', globalEvents), {
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
    const { result } = renderHook(() => useStageEvents('s1', live))
    expect(result.current.length).toBe(1)
    expect(result.current[0]?.stageId).toBe('s1')
  })

  it('dedupes an event present in BOTH fetched history and the live stream', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify([{ type: 'agent_action', stage_id: 's1', seq: 5, timestamp: '2026-01-01T00:00:00.005Z' }])),
    )
    const live = [ev('s1', 5)]
    const { result } = renderHook(() => useStageEvents('s1', live))
    await waitFor(() => expect(result.current.length).toBe(1))
  })

  it('does not call setHistory after stageId flips to null mid-flight', async () => {
    const pending = deferred<Response>()
    vi.spyOn(globalThis, 'fetch').mockReturnValue(pending.promise)

    const { result, rerender } = renderHook(({ stageId }) => useStageEvents(stageId, []), {
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

  // Review round 1, Important finding: useEffect коммитится ПОСЛЕ рендера,
  // так что рендер СРАЗУ ПОСЛЕ смены stageId ещё видит старое состояние
  // history (принадлежащее ПРЕДЫДУЩЕЙ стадии) — без явной метки-стадии
  // useMemo смёржил бы чужую историю с live-хвостом новой стадии на один
  // кадр. result.current (renderHook) отражает только ФИНАЛЬНОЕ состояние
  // после того, как act() досасывает все синхронные последствия эффекта —
  // поэтому здесь мы трекаем ЛЮБОЙ рендер через обёртку-хук useTracked,
  // которая пушит в captured каждый вызов (включая тот самый промежуточный,
  // ДО того как эффект успел среагировать), а не только последний.
  it('does not stitch the previous stage history onto the new stage during the switch (review round 1, Important)', async () => {
    const captured: AfmEvent[][] = []
    function useTracked(stageId: string | null, globalEvents: AfmEvent[]): AfmEvent[] {
      const value = useStageEvents(stageId, globalEvents)
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

    // Ни один зафиксированный рендер (включая самый первый — ДО того, как
    // эффект для 'b' успел хоть что-то сделать: его фетч зависает навечно)
    // не должен содержать событие стадии 'a'.
    expect(captured.length).toBeGreaterThan(0)
    for (const snapshot of captured) {
      expect(snapshot.some((e) => e.stageId === 'a')).toBe(false)
    }
  })
})
