import { renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { normalizeStatus, useStatus } from './use-status'

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
      interactive: false,
      autonomous: false,
      autoApprove: false,
    })
    expect(result.current.stages[1]?.status).toBe('running')
  })

  test('парсит stage_interactive и stage_autonomous с дефолтом false', () => {
    const raw = {
      flow_name: 'demo',
      stage_order: ['s1', 's2', 's3'],
      stages: { s1: { status: 'running' }, s2: { status: 'pending' }, s3: { status: 'pending' } },
      stage_interactive: { s1: true },
      stage_autonomous: { s2: true },
    }
    const { stages } = normalizeStatus(raw)
    const byId = new Map(stages.map((s) => [s.id, s]))
    expect(byId.get('s1')?.interactive).toBe(true)
    expect(byId.get('s1')?.autonomous).toBe(false)
    expect(byId.get('s2')?.interactive).toBe(false)
    expect(byId.get('s2')?.autonomous).toBe(true)
    expect(byId.get('s3')?.interactive).toBe(false)
    expect(byId.get('s3')?.autonomous).toBe(false)
  })

  test('falls back to Object.keys(...).sort() when stage_order is missing', () => {
    const raw = {
      flow_name: 'demo',
      stages: { b: { status: 'pending' }, a: { status: 'running' }, c: { status: 'done' } },
    }
    const { stages } = normalizeStatus(raw)
    expect(stages.map((s) => s.id)).toEqual(['a', 'b', 'c'])
  })

  test('falls back to Object.keys(...).sort() when stage_order is an empty array', () => {
    const raw = {
      flow_name: 'demo',
      stage_order: [],
      stages: { b: { status: 'pending' }, a: { status: 'running' } },
    }
    const { stages } = normalizeStatus(raw)
    expect(stages.map((s) => s.id)).toEqual(['a', 'b'])
  })

  test('normalizes the optional description field when present', () => {
    const raw = {
      flow_name: 'demo',
      stage_order: ['s1'],
      stages: { s1: { status: 'pending' } },
      description: 'Проект X: очистка изображений',
    }
    const status = normalizeStatus(raw)
    expect(status.description).toBe('Проект X: очистка изображений')
  })

  test('leaves description undefined when the backend does not send it', () => {
    const raw = {
      flow_name: 'demo',
      stage_order: ['s1'],
      stages: { s1: { status: 'pending' } },
    }
    const status = normalizeStatus(raw)
    expect(status.description).toBeUndefined()
  })

  test('normalizes an unrecognized stage status to "pending"', () => {
    const raw = {
      flow_name: 'demo',
      stage_order: ['s1'],
      stages: { s1: { status: 'not_a_real_status' } },
    }
    const { stages } = normalizeStatus(raw)
    expect(stages[0]?.status).toBe('pending')
  })

  test('fetch rejection leaves status at the empty initial state', async () => {
    vi.spyOn(globalThis, 'fetch').mockRejectedValue(new Error('network down'))

    const { result } = renderHook(() => useStatus())

    await waitFor(() => {
      expect(globalThis.fetch).toHaveBeenCalled()
    })
    expect(result.current.flowName).toBe('')
    expect(result.current.stages).toEqual([])
    expect(result.current.startedAt).toBe('')
  })

  test('response.ok === false leaves status at the empty initial state', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: false,
      json: async () => ({ flow_name: 'should-not-apply', stages: {} }),
    } as Response)

    const { result } = renderHook(() => useStatus())

    await waitFor(() => {
      expect(globalThis.fetch).toHaveBeenCalled()
    })
    expect(result.current.flowName).toBe('')
    expect(result.current.stages).toEqual([])
  })

  test('CRITICAL: discards a stale response that resolves after a newer request was issued', async () => {
    // Живой баг: несколько значимых WS-событий подряд (напр. стадия A завершилась
    // И стадия B тут же стала running) issue несколько независимых fetch('/api/status').
    // Сетевые ответы могут прийти НЕ в порядке отправки — если более старый (issued
    // раньше) запрос резолвится ПОСЛЕ более нового, он откатывает состояние назад
    // (напр. стадия B снова выглядит pending) и одноразовый auto-advance в App
    // навсегда теряет свой шанс сработать.
    let callIndex = 0
    const deferred: Array<(data: unknown) => void> = []
    vi.spyOn(globalThis, 'fetch').mockImplementation(() => {
      const idx = callIndex++
      return new Promise<Response>((resolve) => {
        deferred[idx] = (data) => resolve({ ok: true, json: async () => data } as Response)
      })
    })

    const { result } = renderHook(() => useStatus())
    await waitFor(() => expect(deferred[0]).toBeDefined())

    // Второй (более новый) запрос issued до того, как первый успел резолвиться.
    result.current.refresh()
    await waitFor(() => expect(deferred[1]).toBeDefined())

    // Резолвим НЕ по порядку: новый запрос отвечает первым.
    deferred[1]!({ flow_name: 'newer', stages: {} })
    await waitFor(() => expect(result.current.flowName).toBe('newer'))

    // Устаревший запрос отвечает последним — не должен откатить состояние назад.
    deferred[0]!({ flow_name: 'stale', stages: {} })
    await new Promise((r) => setTimeout(r, 10))
    expect(result.current.flowName).toBe('newer')
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
