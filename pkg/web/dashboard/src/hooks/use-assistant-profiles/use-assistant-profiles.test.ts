import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useAssistantProfiles } from './use-assistant-profiles'

function jsonResponse(body: unknown): Response {
  return { ok: true, json: async () => body } as Response
}

describe('useAssistantProfiles', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  it('does not fetch when disabled', () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch')
    const { result } = renderHook(() => useAssistantProfiles(false))
    expect(result.current).toEqual([])
    expect(fetchSpy).not.toHaveBeenCalled()
  })

  it('fetches and normalizes profiles when enabled', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      jsonResponse({
        profiles: [
          { id: 'frontendDeveloper', title: 'Frontend reviewer', command: 'claude', available: true },
          { id: 'ghost', title: 'Ghost', command: 'claude', available: false, unavailable_reason: 'no policy' },
        ],
        max_depth: 3,
        max_depth_source: 'default',
      }),
    )
    const { result } = renderHook(() => useAssistantProfiles(true))
    await waitFor(() => expect(result.current).toHaveLength(2))
    expect(result.current[0]).toEqual({
      id: 'frontendDeveloper', title: 'Frontend reviewer', command: 'claude', available: true, unavailableReason: '',
    })
    expect(result.current[1]?.unavailableReason).toBe('no policy')
  })

  it('a transient fetch error keeps the last known list instead of clearing it', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch')
    fetchSpy.mockResolvedValueOnce(jsonResponse({ profiles: [{ id: 'x', title: 'X', command: 'claude', available: true }] }))
    const { result } = renderHook(() => useAssistantProfiles(true))
    await waitFor(() => expect(result.current).toHaveLength(1))

    fetchSpy.mockRejectedValueOnce(new Error('network'))
    vi.useFakeTimers()
    await act(async () => {
      vi.advanceTimersByTime(20000)
    })
    expect(result.current).toHaveLength(1)
  })

  it('clears the list when disabled after being enabled', () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse({ profiles: [] }))
    const { result, rerender } = renderHook(({ enabled }) => useAssistantProfiles(enabled), { initialProps: { enabled: true } })
    rerender({ enabled: false })
    expect(result.current).toEqual([])
  })
})
