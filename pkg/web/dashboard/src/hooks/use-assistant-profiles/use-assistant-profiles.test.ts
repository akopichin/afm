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
      id: 'frontendDeveloper', title: 'Frontend reviewer', command: 'claude', commandSource: '', runtimeContext: '',
      available: true, unavailableReason: '',
    })
    expect(result.current[1]?.unavailableReason).toBe('no policy')
  })

  it('requests run-level profiles without stage_id', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse({ profiles: [] }))
    renderHook(() => useAssistantProfiles(true))
    await waitFor(() => expect(fetchSpy).toHaveBeenCalled())
    expect(fetchSpy).toHaveBeenCalledWith('/api/assistant-profiles')
  })

  it('requests the stage context with stage_id and keeps source and runtime context', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      jsonResponse({
        profiles: [{ id: 'qa', title: 'QA', command: 'codex', command_source: 'stage', runtime_context: 'wrapper', available: true }],
      }),
    )
    const { result } = renderHook(() => useAssistantProfiles(true, 'impl 1'))
    await waitFor(() => expect(result.current).toHaveLength(1))
    expect(fetchSpy).toHaveBeenCalledWith('/api/assistant-profiles?stage_id=impl%201')
    expect(result.current[0]).toMatchObject({ command: 'codex', commandSource: 'stage', runtimeContext: 'wrapper' })
  })

  it('does not show one stage\'s profiles for another stage while the new request is in flight', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch')
    fetchSpy.mockResolvedValueOnce(jsonResponse({ profiles: [{ id: 'a', title: 'A', command: 'claude', available: true }] }))
    const { result, rerender } = renderHook(({ stageId }) => useAssistantProfiles(true, stageId), { initialProps: { stageId: 'one' } })
    await waitFor(() => expect(result.current).toHaveLength(1))

    fetchSpy.mockReturnValueOnce(new Promise<Response>(() => {}))
    rerender({ stageId: 'two' })
    expect(result.current).toEqual([])
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
