import { act, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { App } from './App'

// Минимальный stub WebSocket — App открывает соединение при монтировании.
// instances хранит все созданные сокеты, чтобы тесты могли достать актуальный
// и вручную вызвать onmessage, эмулируя событие сервера.
class StubWebSocket {
  static instances: StubWebSocket[] = []

  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null

  constructor() {
    StubWebSocket.instances.push(this)
  }

  close() {
    /* no-op */
  }
}

// Мокирует fetch для App: /api/status отдаёт statusPayload() (и считает вызовы через
// onStatusCall), остальные эндпоинты (log/plan/dialog) отвечают пустыми заглушками.
function mockFetchForStatus(statusPayload: () => unknown, onStatusCall?: () => void) {
  return vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const url = typeof input === 'string' ? input : (input as Request).url

    if (url.includes('/api/status')) {
      onStatusCall?.()
      return { ok: true, json: async () => statusPayload() } as Response
    }

    if (url.includes('/log')) return { ok: true, text: async () => '' } as Response
    if (url.includes('/plan')) return { ok: true, text: async () => '' } as Response
    if (url.includes('/dialog')) return { ok: true, json: async () => [] } as Response

    return { ok: true, json: async () => [] } as Response
  })
}

describe('App', () => {
  beforeEach(() => {
    StubWebSocket.instances = []
    vi.stubGlobal('WebSocket', StubWebSocket)
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  test('renders the flow name and auto-selects an active stage', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as Request).url

      if (url.includes('/api/status')) {
        return {
          ok: true,
          json: async () => ({
            flow_name: 'demo',
            stage_order: ['s1', 's2'],
            stage_names: { s1: 'Propose', s2: 'Plan' },
            stages: {
              s1: { status: 'running', updated_at: '' },
              s2: { status: 'pending', updated_at: '' },
            },
          }),
        } as Response
      }

      if (url.includes('/log')) return { ok: true, text: async () => '' } as Response
      if (url.includes('/plan')) return { ok: true, text: async () => '' } as Response
      if (url.includes('/dialog')) return { ok: true, json: async () => [] } as Response

      return { ok: true, json: async () => [] } as Response
    })

    render(<App />)

    await waitFor(() => {
      expect(screen.getByText('demo')).toBeInTheDocument()
    })

    // Автовыбор: s1 (running) выбран автоматически → заголовок панели деталей показывает 'Propose'.
    // 'Propose' также есть в списке стадий, поэтому проверяем именно заголовок деталей по id.
    await waitFor(() => {
      expect(document.getElementById('detail-title')).toHaveTextContent('Propose')
    })

    expect(screen.getByText('OFFLINE')).toBeInTheDocument()
  })

  test('a significant WS event triggers a re-fetch of /api/status', async () => {
    let statusCalls = 0
    mockFetchForStatus(
      () => ({
        flow_name: 'demo',
        stage_order: ['s1'],
        stage_names: { s1: 'Propose' },
        stages: { s1: { status: 'running', updated_at: '' } },
      }),
      () => {
        statusCalls += 1
      },
    )

    render(<App />)

    await waitFor(() => expect(statusCalls).toBe(1))

    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    act(() => {
      ws?.onmessage?.({ data: JSON.stringify({ type: 'stage_status_changed', data: { status: 'done' }, stage_id: 's1' }) })
    })

    await waitFor(() => expect(statusCalls).toBe(2))
  })

  test('CRITICAL: a batch of several significant events triggers only one refresh (throttled)', async () => {
    let statusCalls = 0
    mockFetchForStatus(
      () => ({
        flow_name: 'demo',
        stage_order: ['s1'],
        stage_names: { s1: 'Propose' },
        stages: { s1: { status: 'running', updated_at: '' } },
      }),
      () => {
        statusCalls += 1
      },
    )

    render(<App />)

    await waitFor(() => expect(statusCalls).toBe(1))

    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    // Три значимых события, отправленные синхронно в одном батче (одном act) — React 18
    // объединяет соответствующие setState в один коммит, и refresh() эффект видит только
    // одно изменение events (по последнему событию батча), т.е. срабатывает один раз.
    act(() => {
      ws?.onmessage?.({ data: JSON.stringify({ type: 'stage_status_changed', data: { status: 'running' }, stage_id: 's1' }) })
      ws?.onmessage?.({ data: JSON.stringify({ type: 'approved', data: {}, stage_id: 's1' }) })
      ws?.onmessage?.({ data: JSON.stringify({ type: 'revised', data: {}, stage_id: 's1' }) })
    })

    await waitFor(() => expect(statusCalls).toBe(2))

    // POLL_INTERVAL_MS для /api/status — 3с, так что короткая пауза не даёт лишних вызовов
    // просочиться и подтверждает, что троттлинг сработал, а не совпадение с поллингом.
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 50))
    })
    expect(statusCalls).toBe(2)
  })

  test('CRITICAL: an autonomous stage hides the plan panel', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stage_order: ['s1'],
      stage_names: { s1: 'Autonomous stage' },
      stages: { s1: { status: 'running', updated_at: '' } },
      stage_autonomous: { s1: true },
      stage_interactive: { s1: false },
    }))

    render(<App />)

    await waitFor(() => {
      expect(document.getElementById('detail-title')).toHaveTextContent('Autonomous stage')
    })

    expect(document.getElementById('plan-section')).toBeNull()
  })

  test('CRITICAL: a non-interactive, non-autonomous stage hides the dialog panel', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stage_order: ['s1'],
      stage_names: { s1: 'Silent stage' },
      stages: { s1: { status: 'running', updated_at: '' } },
      stage_autonomous: { s1: false },
      stage_interactive: { s1: false },
    }))

    render(<App />)

    await waitFor(() => {
      expect(document.getElementById('detail-title')).toHaveTextContent('Silent stage')
    })

    expect(document.getElementById('dialog-section')).toBeNull()
    expect(document.getElementById('plan-section')).not.toBeNull()
  })

  test('CRITICAL: a failed autonomous stage still shows the retry button', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stage_order: ['s1'],
      stage_names: { s1: 'Autonomous stage' },
      stages: { s1: { status: 'failed', updated_at: '' } },
      stage_autonomous: { s1: true },
      stage_interactive: { s1: false },
    }))

    render(<App />)

    await waitFor(() => {
      expect(document.getElementById('detail-title')).toHaveTextContent('Autonomous stage')
    })

    expect(document.getElementById('btn-retry')).not.toBeNull()
  })

  test('WARNING: falls back to a failed stage when no stage is active', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stage_order: ['s1', 's2'],
      stage_names: { s1: 'Done stage', s2: 'Failed stage' },
      stages: {
        s1: { status: 'done', updated_at: '' },
        s2: { status: 'failed', updated_at: '' },
      },
    }))

    render(<App />)

    await waitFor(() => {
      expect(document.getElementById('detail-title')).toHaveTextContent('Failed stage')
    })
  })
})
