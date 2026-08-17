import { renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { normalizeStatus, useStatus } from './use-status'

describe('useStatus', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true })
  })

  test('normalizes /api/status (ordered stages array) into Stage[]', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => ({
        flow_name: 'demo',
        started_at: '2026-07-10T10:00:00Z',
        stages: [
          { id: 'propose', name: 'Propose', status: 'done', updated_at: '2026-07-10T10:01:00Z',
            interactive: false, autonomous: false, auto_approve: false, has_dialog: false,
            show_plan: true, show_dialog: false },
          { id: 'plan', name: 'Plan', status: 'running', updated_at: '2026-07-10T10:02:00Z',
            interactive: false, autonomous: false, auto_approve: false, has_dialog: false,
            show_plan: true, show_dialog: false },
        ],
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
      hasDialog: false,
      showPlan: true,
      showDialog: false,
      isScript: false,
      pausedFrom: '',
    })
    expect(result.current.stages[1]?.status).toBe('running')
  })

  test('парсит is_script/paused_from', () => {
    const raw = {
      flow_name: 'demo',
      stages: [
        { id: 's1', status: 'paused', is_script: true, paused_from: 'running' },
        { id: 's2', status: 'running' },
      ],
    }
    const { stages } = normalizeStatus(raw)
    const byId = new Map(stages.map((s) => [s.id, s]))
    expect(byId.get('s1')?.isScript).toBe(true)
    expect(byId.get('s1')?.pausedFrom).toBe('running')
    expect(byId.get('s2')?.isScript).toBe(false)
    expect(byId.get('s2')?.pausedFrom).toBe('')
  })

  test('парсит interactive/autonomous/has_dialog с дефолтом false', () => {
    const raw = {
      flow_name: 'demo',
      stages: [
        { id: 's1', status: 'running', interactive: true },
        { id: 's2', status: 'pending', autonomous: true, has_dialog: true },
        { id: 's3', status: 'pending' },
      ],
    }
    const { stages } = normalizeStatus(raw)
    const byId = new Map(stages.map((s) => [s.id, s]))
    expect(byId.get('s1')?.interactive).toBe(true)
    expect(byId.get('s1')?.autonomous).toBe(false)
    expect(byId.get('s1')?.hasDialog).toBe(false)
    expect(byId.get('s2')?.interactive).toBe(false)
    expect(byId.get('s2')?.autonomous).toBe(true)
    expect(byId.get('s2')?.hasDialog).toBe(true)
    expect(byId.get('s3')?.interactive).toBe(false)
    expect(byId.get('s3')?.autonomous).toBe(false)
    expect(byId.get('s3')?.hasDialog).toBe(false)
  })

  test('preserves array order as sent by the backend (no client-side sort)', () => {
    const raw = {
      flow_name: 'demo',
      stages: [
        { id: 'b', status: 'pending' },
        { id: 'a', status: 'running' },
        { id: 'c', status: 'done' },
      ],
    }
    const { stages } = normalizeStatus(raw)
    expect(stages.map((s) => s.id)).toEqual(['b', 'a', 'c'])
  })

  test('an empty stages array yields no stages', () => {
    const raw = { flow_name: 'demo', stages: [] }
    const { stages } = normalizeStatus(raw)
    expect(stages).toEqual([])
  })

  test('a missing/malformed stages field yields no stages', () => {
    const { stages } = normalizeStatus({ flow_name: 'demo' })
    expect(stages).toEqual([])
  })

  test('normalizes the optional description field when present', () => {
    const raw = {
      flow_name: 'demo',
      stages: [{ id: 's1', status: 'pending' }],
      description: 'Проект X: очистка изображений',
    }
    const status = normalizeStatus(raw)
    expect(status.description).toBe('Проект X: очистка изображений')
  })

  test('leaves description undefined when the backend does not send it', () => {
    const raw = {
      flow_name: 'demo',
      stages: [{ id: 's1', status: 'pending' }],
    }
    const status = normalizeStatus(raw)
    expect(status.description).toBeUndefined()
  })

  test('normalizes an unrecognized stage status to "pending"', () => {
    const raw = {
      flow_name: 'demo',
      stages: [{ id: 's1', status: 'not_a_real_status' }],
    }
    const { stages } = normalizeStatus(raw)
    expect(stages[0]?.status).toBe('pending')
  })

  test('skips a stage entry without a string id', () => {
    const raw = {
      flow_name: 'demo',
      stages: [{ status: 'running' }, { id: 's1', status: 'pending' }],
    }
    const { stages } = normalizeStatus(raw)
    expect(stages.map((s) => s.id)).toEqual(['s1'])
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
      json: async () => ({ flow_name: 'should-not-apply', stages: [] }),
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
    deferred[1]!({ flow_name: 'newer', stages: [] })
    await waitFor(() => expect(result.current.flowName).toBe('newer'))

    // Устаревший запрос отвечает последним — не должен откатить состояние назад.
    deferred[0]!({ flow_name: 'stale', stages: [] })
    await new Promise((r) => setTimeout(r, 10))
    expect(result.current.flowName).toBe('newer')
  })

  test('refresh() triggers a fresh fetch on demand (WS refresh channel)', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => ({ flow_name: 'v1', stages: [] }),
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

  test('normalizeStatus parses idle/backoff fields', () => {
    const result = normalizeStatus({
      flow_name: 'demo',
      stages: [{ id: 's1', status: 'running', updated_at: '' }],
      idle_accumulated_ms: 5000,
      idle_since: '2026-08-07T10:00:00Z',
      backoff_accumulated_ms: 3000,
      backoff_open_since: ['2026-08-07T10:05:00Z', '2026-08-07T10:06:00Z'],
    })

    expect(result.idleAccumulatedMs).toBe(5000)
    expect(result.idleSince).toBe('2026-08-07T10:00:00Z')
    expect(result.backoffAccumulatedMs).toBe(3000)
    expect(result.backoffOpenSince).toEqual(['2026-08-07T10:05:00Z', '2026-08-07T10:06:00Z'])
  })

  test('normalizeStatus defaults idle/backoff fields when absent', () => {
    const result = normalizeStatus({ flow_name: 'demo', stages: [] })

    expect(result.idleAccumulatedMs).toBe(0)
    expect(result.idleSince).toBeNull()
    expect(result.backoffAccumulatedMs).toBe(0)
    expect(result.backoffOpenSince).toEqual([])
  })

  test('normalizeStatus: reads the ordered stages array directly (no per-id maps)', () => {
    const raw = {
      flow_name: 'demo',
      started_at: '2026-08-10T00:00:00Z',
      stages: [
        { id: 'b', name: 'Stage B', status: 'running', updated_at: '2026-08-10T00:01:00Z',
          interactive: false, autonomous: true, auto_approve: false, has_dialog: false,
          show_plan: false, show_dialog: true },
        { id: 'a', name: '', status: 'pending', updated_at: '',
          interactive: true, autonomous: false, auto_approve: true, has_dialog: false,
          show_plan: true, show_dialog: true },
      ],
      idle_accumulated_ms: 0,
      backoff_accumulated_ms: 0,
    }

    const status = normalizeStatus(raw)

    expect(status.stages.map((s) => s.id)).toEqual(['b', 'a']) // order preserved, not sorted
    expect(status.stages[0]).toMatchObject({
      id: 'b', name: 'Stage B', status: 'running', autonomous: true, showPlan: false, showDialog: true,
    })
    expect(status.stages[1]).toMatchObject({
      id: 'a', interactive: true, autoApprove: true, showPlan: true, showDialog: true,
    })
  })

  test('refetches status when the tab regains visibility (background-tab poll throttling)', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => ({ flow_name: 'demo', stages: [] }),
    } as Response)

    renderHook(() => useStatus())
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))

    Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true })
    document.dispatchEvent(new Event('visibilitychange'))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
  })

  test('ignores visibilitychange while the tab is still hidden', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => ({ flow_name: 'demo', stages: [] }),
    } as Response)

    renderHook(() => useStatus())
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))

    Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true })
    document.dispatchEvent(new Event('visibilitychange'))

    await new Promise((resolve) => setTimeout(resolve, 10))
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  test('refetches status on window focus', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => ({ flow_name: 'demo', stages: [] }),
    } as Response)

    renderHook(() => useStatus())
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))

    window.dispatchEvent(new Event('focus'))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
  })
})
