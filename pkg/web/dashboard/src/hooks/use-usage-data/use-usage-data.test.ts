import { renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { useUsageData } from './use-usage-data'

describe('useUsageData', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('maps timeBucket/value into UsagePoint and queries metric+stage', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => [
        { timeBucket: '2026-07-10T10:00:00Z', value: 100 },
        { timeBucket: '2026-07-10T10:01:00Z', value: 200 },
      ],
    } as Response)

    const { result } = renderHook(() => useUsageData('tokens', null))

    await waitFor(() => {
      expect(result.current).toHaveLength(2)
    })

    expect(result.current[0]).toEqual({
      timestamp: '2026-07-10T10:00:00Z',
      metric: 'tokens',
      value: 100,
    })

    expect(fetchSpy.mock.calls[0]?.[0]).toBe('/api/usage?metric=tokens&stage=')
  })

  test('empty response is a valid empty series', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => [],
    } as Response)

    const { result } = renderHook(() => useUsageData('cost', null))

    await waitFor(() => {
      expect(result.current).toEqual([])
    })
  })
})
