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
