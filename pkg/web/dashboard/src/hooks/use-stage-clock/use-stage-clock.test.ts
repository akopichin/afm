import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { useStageClock } from './use-stage-clock'

const START = new Date('2026-10-30T20:00:00Z').getTime()

describe('useStageClock', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  test('ticks once a second while active', () => {
    vi.useFakeTimers({ now: START })
    const { result } = renderHook(() => useStageClock(true))
    expect(result.current).toBe(START)

    act(() => vi.advanceTimersByTime(1000))
    expect(result.current).toBe(START + 1000)
    act(() => vi.advanceTimersByTime(2000))
    expect(result.current).toBe(START + 3000)
  })

  test('creates no interval while inactive and keeps the last value', () => {
    vi.useFakeTimers({ now: START })
    const { result } = renderHook(() => useStageClock(false))
    expect(vi.getTimerCount()).toBe(0)

    act(() => vi.advanceTimersByTime(5000))
    expect(result.current).toBe(START)
  })

  test('freezes on deactivation and jumps to the current time on return', () => {
    vi.useFakeTimers({ now: START })
    const { result, rerender } = renderHook(({ active }) => useStageClock(active), { initialProps: { active: true } })
    act(() => vi.advanceTimersByTime(2000))
    expect(result.current).toBe(START + 2000)

    rerender({ active: false })
    expect(vi.getTimerCount()).toBe(0)
    act(() => vi.advanceTimersByTime(10_000))
    expect(result.current).toBe(START + 2000)

    rerender({ active: true })
    expect(result.current).toBe(START + 12_000)
    act(() => vi.advanceTimersByTime(1000))
    expect(result.current).toBe(START + 13_000)
  })

  test('removes the interval on unmount', () => {
    vi.useFakeTimers({ now: START })
    const { unmount } = renderHook(() => useStageClock(true))
    expect(vi.getTimerCount()).toBe(1)
    unmount()
    expect(vi.getTimerCount()).toBe(0)
  })
})
