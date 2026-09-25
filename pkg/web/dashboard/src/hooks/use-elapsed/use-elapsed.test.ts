import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { useElapsed } from './use-elapsed'

describe('useElapsed', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  test('ticks every second from startedAt', () => {
    vi.useFakeTimers({ now: new Date('2026-07-10T10:00:00Z').getTime() })

    const { result } = renderHook(() => useElapsed('2026-07-10T09:59:58Z'))

    expect(result.current).toBe(2000)

    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(result.current).toBe(3000)

    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(result.current).toBe(4000)
  })

  test('stops at endedAt and uses the new boundary after a run resumes', () => {
    vi.useFakeTimers({ now: new Date('2026-07-10T10:00:10Z').getTime() })
    const start = '2026-07-10T10:00:00Z'
    const { result, rerender } = renderHook(({ end }) => useElapsed(start, end), {
      initialProps: { end: '2026-07-10T10:00:04Z' as string | null },
    })
    expect(result.current).toBe(4000)

    act(() => vi.advanceTimersByTime(5000))
    expect(result.current).toBe(4000)

    rerender({ end: null })
    expect(result.current).toBe(15000)
    act(() => vi.advanceTimersByTime(1000))
    expect(result.current).toBe(16000)
  })

  test('clamps future start timestamps to zero', () => {
    vi.useFakeTimers({ now: new Date('2026-07-10T10:00:00Z').getTime() })
    const { result } = renderHook(() => useElapsed('2026-07-10T10:00:05Z'))
    expect(result.current).toBe(0)
  })

  test('freezes when the status endpoint and WebSocket are both unavailable', () => {
    vi.useFakeTimers({ now: new Date('2026-07-10T10:00:02Z').getTime() })
    const { result, rerender } = renderHook(({ live }) => useElapsed('2026-07-10T10:00:00Z', null, live), {
      initialProps: { live: true },
    })
    expect(result.current).toBe(2000)
    rerender({ live: false })
    act(() => vi.advanceTimersByTime(5000))
    expect(result.current).toBe(2000)
    rerender({ live: true })
    expect(result.current).toBe(7000)
  })

  test('continues from accumulated active time after a resume without adding the offline gap', () => {
    vi.useFakeTimers({ now: new Date('2026-07-10T11:00:03Z').getTime() })
    const { result, rerender } = renderHook(
      ({ end, accumulated, since }) => useElapsed('2026-07-10T10:00:00Z', end, true, accumulated, since),
      { initialProps: { end: null as string | null, accumulated: 4000, since: '2026-07-10T11:00:00Z' as string | null } },
    )
    expect(result.current).toBe(7000)
    rerender({ end: '2026-07-10T11:00:05Z', accumulated: 9000, since: null })
    expect(result.current).toBe(9000)
    act(() => vi.advanceTimersByTime(5000))
    expect(result.current).toBe(9000)
  })

  test('does not show the offline gap before a resumed run gets its new anchor', () => {
    vi.useFakeTimers({ now: new Date('2026-07-10T11:00:00Z').getTime() })
    const { result, rerender } = renderHook(({ since }) => useElapsed('2026-07-10T10:00:00Z', null, true, 4000, since), {
      initialProps: { since: null as string | null },
    })
    expect(result.current).toBe(4000)
    rerender({ since: '2026-07-10T11:00:00Z' })
    act(() => vi.advanceTimersByTime(1000))
    expect(result.current).toBe(5000)
  })

  test('returns 0 when startedAt is empty or invalid', () => {
    vi.useFakeTimers()

    const { result } = renderHook(() => useElapsed(''))

    expect(result.current).toBe(0)
  })

  test('clears the interval on unmount and stops updating', () => {
    vi.useFakeTimers({ now: new Date('2026-07-10T10:00:00Z').getTime() })
    const clearIntervalSpy = vi.spyOn(globalThis, 'clearInterval')

    const { result, unmount } = renderHook(() => useElapsed('2026-07-10T09:59:58Z'))

    expect(result.current).toBe(2000)

    unmount()

    expect(clearIntervalSpy).toHaveBeenCalled()

    act(() => {
      vi.advanceTimersByTime(5000)
    })
    // Компонент размонтирован — значение из последнего рендера не меняется.
    expect(result.current).toBe(2000)
  })
})
