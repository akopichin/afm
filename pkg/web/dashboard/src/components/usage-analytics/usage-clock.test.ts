import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { useSinceAsOf } from './usage-clock'

const AS_OF = '2026-10-31T12:00:00Z'
const NEXT_AS_OF = '2026-10-31T12:00:30Z'

function renderSince(asOf: string, active: boolean) {
  return renderHook(({ asOf, active }) => useSinceAsOf(asOf, active), { initialProps: { asOf, active } })
}

describe('useSinceAsOf', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  test('freezes at the last value when ticking stops and catches up on return', () => {
    vi.useFakeTimers({ now: Date.parse(AS_OF) })
    const { result, rerender } = renderSince(AS_OF, true)
    act(() => vi.advanceTimersByTime(5_000))
    expect(result.current).toBe(5_000)
    const before = result.current

    rerender({ asOf: AS_OF, active: false })
    expect(result.current).toBe(before)
    act(() => vi.advanceTimersByTime(5_000))
    expect(result.current).toBe(before)

    rerender({ asOf: AS_OF, active: true })
    expect(result.current).toBe(10_000)
  })

  test('is zero when it never ticked', () => {
    vi.useFakeTimers({ now: Date.parse(AS_OF) })
    const { result } = renderSince(AS_OF, false)
    act(() => vi.advanceTimersByTime(5_000))
    expect(result.current).toBe(0)
  })

  test('a new as_of while not ticking is shown as served', () => {
    vi.useFakeTimers({ now: Date.parse(AS_OF) })
    const { result, rerender } = renderSince(AS_OF, true)
    act(() => vi.advanceTimersByTime(5_000))
    rerender({ asOf: AS_OF, active: false })
    expect(result.current).toBe(5_000)

    rerender({ asOf: NEXT_AS_OF, active: false })
    expect(result.current).toBe(0)
  })
})
