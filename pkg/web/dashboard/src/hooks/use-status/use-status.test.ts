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
      preNote: '',
      buttons: [],
    })
    expect(result.current.stages[1]?.status).toBe('running')
  })

  test('парсит buttons (массив строк), дефолт [] при отсутствии/мусоре', () => {
    const raw = {
      flow_name: 'demo',
      stages: [
        { id: 's1', status: 'running', buttons: ['Run linter', 'Rebuild'] },
        { id: 's2', status: 'running' },
        { id: 's3', status: 'running', buttons: 'not-an-array' },
        { id: 's4', status: 'running', buttons: ['ok', 42, null] },
      ],
    }
    const { stages } = normalizeStatus(raw)
    const byId = new Map(stages.map((s) => [s.id, s]))
    expect(byId.get('s1')?.buttons).toEqual(['Run linter', 'Rebuild'])
    expect(byId.get('s2')?.buttons).toEqual([])
    expect(byId.get('s3')?.buttons).toEqual([])
    expect(byId.get('s4')?.buttons).toEqual(['ok'])
  })

  test('парсит verify (Task 7): валидный объект → StageVerify, отсутствие/битая фаза/мусор → undefined', () => {
    const raw = {
      flow_name: 'demo',
      stages: [
        { id: 's1', status: 'running', verify: { step: 2, command: 'codex', phase: 'needs_changes' } },
        { id: 's2', status: 'running' },
        { id: 's3', status: 'running', verify: { step: 1, command: 'codex', phase: 'not-a-real-phase' } },
        { id: 's4', status: 'running', verify: 'not-an-object' },
        { id: 's5', status: 'running', verify: { step: 'not-a-number', command: 42, phase: 'running' } },
      ],
    }
    const { stages } = normalizeStatus(raw)
    const byId = new Map(stages.map((s) => [s.id, s]))
    expect(byId.get('s1')?.verify).toEqual({ step: 2, command: 'codex', phase: 'needs_changes' })
    expect(byId.get('s2')?.verify).toBeUndefined()
    expect(byId.get('s3')?.verify).toBeUndefined()
    expect(byId.get('s4')?.verify).toBeUndefined()
    // step/command защитно коэрсятся (0/''), но валидная phase всё равно даёт запись.
    expect(byId.get('s5')?.verify).toEqual({ step: 0, command: '', phase: 'running' })
  })

  test('парсит is_script/paused_from/pre_note', () => {
    const raw = {
      flow_name: 'demo',
      stages: [
        { id: 's1', status: 'paused', is_script: true, paused_from: 'running', pre_note: 'учти лимиты' },
        { id: 's2', status: 'running' },
      ],
    }
    const { stages } = normalizeStatus(raw)
    const byId = new Map(stages.map((s) => [s.id, s]))
    expect(byId.get('s1')?.isScript).toBe(true)
    expect(byId.get('s1')?.pausedFrom).toBe('running')
    expect(byId.get('s1')?.preNote).toBe('учти лимиты')
    expect(byId.get('s2')?.isScript).toBe(false)
    expect(byId.get('s2')?.pausedFrom).toBe('')
    expect(byId.get('s2')?.preNote).toBe('')
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

  test('normalizeStatus maps capabilities.file_browser to capabilities.fileBrowser', () => {
    const result = normalizeStatus({
      flow_name: 'demo',
      stages: [],
      capabilities: { file_browser: true },
    })

    expect(result.capabilities.fileBrowser).toBe(true)
  })

  test('normalizeStatus defaults capabilities.fileBrowser to false when absent', () => {
    const result = normalizeStatus({ flow_name: 'demo', stages: [] })

    expect(result.capabilities.fileBrowser).toBe(false)
  })

  test('maps flow_pause_state and flow_paused_stages', () => {
    const s = normalizeStatus({ flow_name: 'demo', stages: [], flow_pause_state: 'paused', flow_paused_stages: ['s1'] })
    expect(s.flowPauseState).toBe('paused')
    expect(s.flowPausedStages).toEqual(['s1'])
  })

  test('normalizeStatus maps flow_pause_state "resuming"', () => {
    const s = normalizeStatus({ flow_name: 'demo', stages: [], flow_pause_state: 'resuming' })
    expect(s.flowPauseState).toBe('resuming')
  })

  test('normalizeStatus defaults flow_pause_state to "none" and flow_paused_stages to [] when absent/malformed', () => {
    const s = normalizeStatus({ flow_name: 'demo', stages: [] })
    expect(s.flowPauseState).toBe('none')
    expect(s.flowPausedStages).toEqual([])

    const s2 = normalizeStatus({ flow_name: 'demo', stages: [], flow_pause_state: 'bogus', flow_paused_stages: 'not-an-array' })
    expect(s2.flowPauseState).toBe('none')
    expect(s2.flowPausedStages).toEqual([])
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

  test('normalizeStatus: fully old JSON shape (no accounting, no cost, no coverage_issues)', () => {
    const raw = {
      flow_name: 'old-backend',
      started_at: '2026-09-15T10:00:00Z',
      stages: [
        { id: 's1', status: 'running', name: 'Stage 1', updated_at: '2026-09-15T10:01:00Z' },
        { id: 's2', status: 'pending', name: 'Stage 2', updated_at: '' },
      ],
      idle_accumulated_ms: 0,
      backoff_accumulated_ms: 0,
      // NO accounting, NO coverage_issues, NO cost on stages, NO run_cost/run_overhead_cost
    }

    const status = normalizeStatus(raw)

    // accounting должен быть {supported: false} для старого бэкенда
    expect(status.accounting).toEqual({ supported: false })
    // coverageIssues должен быть пустым массивом
    expect(status.coverageIssues).toEqual([])
    // runCost и runOverheadCost отсутствуют (undefined)
    expect(status.runCost).toBeUndefined()
    expect(status.runOverheadCost).toBeUndefined()
    // Стадии не имеют cost
    expect(status.stages[0]?.cost).toBeUndefined()
    expect(status.stages[1]?.cost).toBeUndefined()
  })

  test('normalizeStatus: new shape with full accounting, cost, and coverage_issues', () => {
    const raw = {
      flow_name: 'new-backend',
      started_at: '2026-09-15T10:00:00Z',
      stages: [
        {
          id: 's1',
          status: 'done',
          name: 'Stage 1',
          updated_at: '2026-09-15T10:01:00Z',
          cost: {
            display_cost: '$0.42',
            coverage: 'full',
            estimated_cost_usd: 0.42,
            metered: 100,
            priced_invocations: 1,
            unpriced: 0,
            unmetered: 0,
            models: ['claude-3-5-sonnet-20241022'],
            uncached_input: 500,
            cache_read: 100,
            cache_write_5m: 50,
            cache_write_1h: 0,
            cache_write_other: 0,
            output: 200,
            reasoning_output: 0,
            total_tokens: 850,
            cache_write_total: 50,
            cache_hit_ratio: 0.15,
            phases: { planning: 850 },
          },
        },
      ],
      run_cost: {
        display_cost: '$0.50',
        coverage: 'partial',
        estimated_cost_usd: 0.50,
        metered: 120,
        priced_invocations: 1,
        unpriced: 10,
        unmetered: 0,
        models: ['claude-3-5-sonnet-20241022'],
        uncached_input: 600,
        cache_read: 150,
        cache_write_5m: 60,
        cache_write_1h: 0,
        cache_write_other: 0,
        output: 250,
        reasoning_output: 0,
        total_tokens: 1000,
        cache_write_total: 60,
        cache_hit_ratio: 0.2,
        phases: { planning: 1000 },
      },
      run_overhead_cost: {
        display_cost: '$0.08',
        coverage: 'none',
        estimated_cost_usd: 0.08,
        metered: 20,
        priced_invocations: 0,
        unpriced: 20,
        unmetered: 0,
        models: [],
        uncached_input: 100,
        cache_read: 0,
        cache_write_5m: 0,
        cache_write_1h: 0,
        cache_write_other: 0,
        output: 50,
        reasoning_output: 0,
        total_tokens: 150,
        cache_write_total: 0,
        cache_hit_ratio: null,
        phases: { overhead: 150 },
      },
      coverage_issues: [
        {
          kind: 'unpriced',
          attribution: { kind: 'run_overhead' },
          phase: 'planning',
          channel: 'stdout',
          model: 'claude-3-5-sonnet-20241022',
          reason: 'Model not in pricing database',
          count: 10,
        },
        {
          kind: 'unmetered',
          attribution: { kind: 'stage', stage_id: 's1' },
          phase: 'implementation',
          channel: 'stderr',
          model: 'claude-3-5-sonnet-20241022',
          reason: 'Could not detect token count',
          count: 5,
        },
      ],
      accounting: {
        health: 'ok',
        has_data: true,
      },
      idle_accumulated_ms: 0,
      backoff_accumulated_ms: 0,
    }

    const status = normalizeStatus(raw)

    // accounting должен быть {supported: true, health: 'ok', hasData: true}
    expect(status.accounting).toEqual({ supported: true, health: 'ok', hasData: true })

    // runCost должен быть маппирован
    expect(status.runCost).toBeDefined()
    expect(status.runCost?.displayCost).toBe('$0.50')
    expect(status.runCost?.coverage).toBe('partial')
    expect(status.runCost?.estimatedCostUsd).toBe(0.50)
    expect(status.runCost?.cacheHitRatio).toBe(0.2)

    // runOverheadCost должен быть маппирован
    expect(status.runOverheadCost).toBeDefined()
    expect(status.runOverheadCost?.displayCost).toBe('$0.08')
    expect(status.runOverheadCost?.coverage).toBe('none')
    expect(status.runOverheadCost?.cacheHitRatio).toBeNull()

    // coverageIssues должны быть маппированы
    expect(status.coverageIssues).toHaveLength(2)
    expect(status.coverageIssues[0]).toMatchObject({
      kind: 'unpriced',
      attribution: { kind: 'run_overhead' },
      phase: 'planning',
    })
    expect(status.coverageIssues[1]).toMatchObject({
      kind: 'unmetered',
      attribution: { kind: 'stage', stageId: 's1' },
      phase: 'implementation',
    })

    // Stage cost должен быть маппирован
    expect(status.stages[0]?.cost).toBeDefined()
    expect(status.stages[0]?.cost?.displayCost).toBe('$0.42')
    expect(status.stages[0]?.cost?.coverage).toBe('full')
    expect(status.stages[0]?.cost?.pricedInvocations).toBe(1)
    expect(status.stages[0]?.cost?.cacheHitRatio).toBe(0.15)
    expect(status.stages[0]?.cost?.phases).toEqual({ planning: 850 })
  })
})
