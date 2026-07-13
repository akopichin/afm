import { renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { useStatus } from './use-status'

describe('useStatus', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('normalizes /api/status (object + stage_order + stage_names) into Stage[]', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => ({
        flow_name: 'demo',
        started_at: '2026-07-10T10:00:00Z',
        stage_order: ['propose', 'plan'],
        stage_names: { propose: 'Propose', plan: 'Plan' },
        stages: {
          propose: { status: 'done', updated_at: '2026-07-10T10:01:00Z' },
          plan: { status: 'running', updated_at: '2026-07-10T10:02:00Z' },
        },
      }),
    } as Response)

    const { result } = renderHook(() => useStatus())

    await waitFor(() => {
      expect(result.current.stages).toHaveLength(2)
    })

    expect(result.current.flowName).toBe('demo')
    expect(result.current.startedAt).toBe('2026-07-10T10:00:00Z')
    expect(result.current.stages[0]).toEqual({
      id: 'propose',
      name: 'Propose',
      status: 'done',
      updatedAt: '2026-07-10T10:01:00Z',
    })
    expect(result.current.stages[1]?.status).toBe('running')
  })

  test('refresh() triggers a fresh fetch on demand (WS refresh channel)', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => ({ flow_name: 'v1', stages: {} }),
    } as Response)

    const { result } = renderHook(() => useStatus())

    await waitFor(() => {
      expect(result.current.flowName).toBe('v1')
    })

    const callsBefore = fetchSpy.mock.calls.length
    result.current.refresh()

    await waitFor(() => {
      expect(fetchSpy.mock.calls.length).toBeGreaterThan(callsBefore)
    })
  })
})
