import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { useIdleMs } from './use-idle-ms'

describe('useIdleMs', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  test('returns just the accumulated value when not currently idle (since=null)', () => {
    vi.useFakeTimers({ now: new Date('2026-08-07T10:00:00Z').getTime() })

    const { result } = renderHook(() => useIdleMs(5000, null, true))

    expect(result.current).toBe(5000)
  })

  test('adds live delta since the anchor while idle and connected, ticking every second', () => {
    vi.useFakeTimers({ now: new Date('2026-08-07T10:00:02Z').getTime() })

    const { result } = renderHook(() => useIdleMs(5000, '2026-08-07T10:00:00Z', true))

    expect(result.current).toBe(7000) // 5000 accumulated + 2000 live

    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(result.current).toBe(8000)
  })

  test('freezes the displayed value while disconnected', () => {
    vi.useFakeTimers({ now: new Date('2026-08-07T10:00:02Z').getTime() })

    const { result, rerender } = renderHook(({ connected }) => useIdleMs(5000, '2026-08-07T10:00:00Z', connected), {
      initialProps: { connected: true },
    })
    expect(result.current).toBe(7000)

    rerender({ connected: false })
    act(() => {
      vi.advanceTimersByTime(5000)
    })
    // Отключено — тик не идёт, значение держится на последнем вычисленном.
    expect(result.current).toBe(7000)

    rerender({ connected: true })
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    // Переподключились — тик продолжается от той же точки (в реальном
    // приложении к этому моменту accumulatedMs/since уже обновились свежим
    // /api/status; здесь параметры не менялись, поэтому просто +1s).
    expect(result.current).toBe(13000)
  })
})
