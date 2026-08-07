import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { useBackoffMs } from './use-backoff-ms'

describe('useBackoffMs', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  test('returns just the accumulated value when no stage is currently retrying', () => {
    vi.useFakeTimers({ now: new Date('2026-08-07T10:00:00Z').getTime() })

    const { result } = renderHook(() => useBackoffMs(3000, [], true))

    expect(result.current).toBe(3000)
  })

  test('sums live deltas for every currently-open episode', () => {
    vi.useFakeTimers({ now: new Date('2026-08-07T10:00:04Z').getTime() })

    const { result } = renderHook(() => useBackoffMs(1000, ['2026-08-07T10:00:00Z', '2026-08-07T10:00:02Z'], true))

    // 1000 accumulated + 4000 (first episode) + 2000 (second episode)
    expect(result.current).toBe(7000)

    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(result.current).toBe(9000)
  })

  test('freezes the displayed value while disconnected', () => {
    vi.useFakeTimers({ now: new Date('2026-08-07T10:00:04Z').getTime() })

    const { result, rerender } = renderHook(({ connected }) => useBackoffMs(1000, ['2026-08-07T10:00:00Z'], connected), {
      initialProps: { connected: true },
    })
    expect(result.current).toBe(5000)

    rerender({ connected: false })
    act(() => {
      vi.advanceTimersByTime(5000)
    })
    expect(result.current).toBe(5000)
  })
})
