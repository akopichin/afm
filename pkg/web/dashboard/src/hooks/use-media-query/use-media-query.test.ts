import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useMediaQuery } from './use-media-query'

// A controllable matchMedia: flip() changes the answer and notifies the subscribed listeners.
function stubMatchMedia(initial: boolean) {
  let matches = initial
  const listeners = new Set<() => void>()
  const removed = vi.fn()
  vi.stubGlobal('matchMedia', () => ({
    get matches() { return matches },
    addEventListener: (_: string, listener: () => void) => listeners.add(listener),
    removeEventListener: (_: string, listener: () => void) => { removed(); listeners.delete(listener) },
  }))
  return {
    listeners, removed,
    flip(next: boolean) { act(() => { matches = next; listeners.forEach((notify) => notify()) }) },
  }
}

describe('useMediaQuery', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('starts with the current match', () => {
    stubMatchMedia(true)
    expect(renderHook(() => useMediaQuery('(max-width: 1180px)')).result.current).toBe(true)
  })

  it('follows change events', () => {
    const media = stubMatchMedia(false)
    const { result } = renderHook(() => useMediaQuery('(max-width: 1180px)'))
    expect(result.current).toBe(false)
    media.flip(true)
    expect(result.current).toBe(true)
    media.flip(false)
    expect(result.current).toBe(false)
  })

  it('unsubscribes on unmount', () => {
    const media = stubMatchMedia(false)
    const { unmount } = renderHook(() => useMediaQuery('(max-width: 1180px)'))
    expect(media.listeners.size).toBe(1)
    unmount()
    expect(media.removed).toHaveBeenCalledTimes(1)
    expect(media.listeners.size).toBe(0)
  })

  it('reports false when matchMedia is unavailable', () => {
    vi.stubGlobal('matchMedia', undefined)
    expect(renderHook(() => useMediaQuery('(max-width: 1180px)')).result.current).toBe(false)
  })
})
