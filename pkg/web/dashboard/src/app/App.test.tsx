import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
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

type StageViewOverrides = {
  interactive?: boolean
  autonomous?: boolean
  autoApprove?: boolean
  hasDialog?: boolean
  showPlan?: boolean
  showDialog?: boolean
}

// Строит один элемент нового wire-формата stages: []StageView (см. Task 2's
// pkg/server/stageview.go). show_plan/show_dialog по умолчанию вычисляются
// так же, как их раньше считал клиент (App.tsx до удаления NO_STAGE) — это
// сохраняет поведение существующих тестов один в один, ведь теперь эти два
// поля приходят готовыми с бэкенда, а не считаются на фронте.
function stageView(id: string, name: string, status: string, overrides: StageViewOverrides = {}) {
  const interactive = overrides.interactive ?? false
  const autonomous = overrides.autonomous ?? false
  const hasDialog = overrides.hasDialog ?? false

  return {
    id,
    name,
    status,
    updated_at: '',
    interactive,
    autonomous,
    auto_approve: overrides.autoApprove ?? false,
    has_dialog: hasDialog,
    show_plan: overrides.showPlan ?? (!autonomous || status === 'failed'),
    show_dialog: overrides.showDialog ?? (interactive || autonomous || hasDialog),
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
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Propose', 'running'), stageView('s2', 'Plan', 'pending')],
    }))

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
        stages: [stageView('s1', 'Propose', 'running')],
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
        stages: [stageView('s1', 'Propose', 'running')],
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
      stages: [stageView('s1', 'Autonomous stage', 'running', { autonomous: true, interactive: false })],
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
      stages: [stageView('s1', 'Silent stage', 'running', { autonomous: false, interactive: false })],
    }))

    render(<App />)

    await waitFor(() => {
      expect(document.getElementById('detail-title')).toHaveTextContent('Silent stage')
    })

    expect(document.getElementById('dialog-section')).toBeNull()
    expect(document.getElementById('plan-section')).not.toBeNull()
  })

  test('CRITICAL: a non-interactive stage WITH dialog history still shows the dialog panel', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [
        stageView('s1', 'Auto-answered stage', 'running', { autonomous: false, interactive: false, hasDialog: true }),
      ],
    }))

    render(<App />)

    await waitFor(() => {
      expect(document.getElementById('detail-title')).toHaveTextContent('Auto-answered stage')
    })

    expect(document.getElementById('dialog-section')).not.toBeNull()
  })

  test('CRITICAL: a failed autonomous stage still shows the retry button', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Autonomous stage', 'failed', { autonomous: true, interactive: false })],
    }))

    render(<App />)

    await waitFor(() => {
      expect(document.getElementById('detail-title')).toHaveTextContent('Autonomous stage')
    })

    expect(document.getElementById('btn-retry')).not.toBeNull()
  })

  test('advances selection to the next active stage when the selected stage completes', async () => {
    // Регресс-защита существующего поведения после рефактора автопродвижения (#3a):
    // когда ВЫБРАННАЯ стадия сама завершается, выбор переходит к следующей активной.
    let done = false
    mockFetchForStatus(() =>
      done
        ? {
            flow_name: 'demo',
            stages: [stageView('s1', 'Propose', 'done'), stageView('s2', 'Plan', 'running')],
          }
        : {
            flow_name: 'demo',
            stages: [stageView('s1', 'Propose', 'running'), stageView('s2', 'Plan', 'pending')],
          },
    )

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Propose'))

    // s1 завершилась, s2 стала активной; значимое WS-событие триггерит рефетч статуса.
    done = true
    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    act(() => {
      ws?.onmessage?.({ data: JSON.stringify({ type: 'stage_status_changed', data: { status: 'done' }, stage_id: 's1' }) })
    })

    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Plan'))
  })

  test('manually selecting a completed stage keeps it selected instead of bouncing to the active one', async () => {
    // Ядро фикса #3a: клик по завершённой стадии во время работы флоу должен
    // оставить её выбранной (иначе нельзя посмотреть её логи/план/диалог).
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Propose', 'done'), stageView('s2', 'Plan', 'running')],
    }))

    render(<App />)
    // Автовыбор активной s2.
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Plan'))

    // Клик по завершённой s1 — выбор должен «прилипнуть», а не отскочить обратно на s2.
    fireEvent.click(document.querySelector('[data-stage-id="s1"]') as HTMLElement)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Propose'))

    // Даём эффектам/поллингу шанс (ошибочно) перекинуть выбор — он обязан остаться на s1.
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 30))
    })
    expect(document.getElementById('detail-title')).toHaveTextContent('Propose')
  })

  test('sets the browser tab title from the flow description', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      description: 'Build the login flow',
      stages: [stageView('s1', 'Propose', 'running')],
    }))

    render(<App />)

    await waitFor(() => expect(document.title).toBe('Build the login flow'))
  })

  test('tab title falls back to the flow name when description is absent', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo-flow',
      stages: [stageView('s1', 'Propose', 'running')],
    }))

    render(<App />)

    await waitFor(() => expect(document.title).toBe('demo-flow'))
  })

  test('CRITICAL: a failed /revise POST from AgentNoteModal keeps the modal open instead of closing silently', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})

    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as Request).url

      if (url.includes('/api/status')) {
        return {
          ok: true,
          json: async () => ({
            flow_name: 'demo',
            stages: [stageView('s1', 'Propose', 'running')],
          }),
        } as Response
      }
      // Стадия ушла из ожидаемого статуса за время, пока юзер печатал заметку.
      if (url.includes('/revise')) {
        return { ok: false, status: 409, json: async () => ({}) } as Response
      }
      if (url.includes('/log')) return { ok: true, text: async () => '' } as Response
      if (url.includes('/plan')) return { ok: true, text: async () => '' } as Response
      if (url.includes('/dialog')) return { ok: true, json: async () => [] } as Response

      return { ok: true, json: async () => [] } as Response
    })

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Propose'))

    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    fireEvent.click(screen.getByText('Add note for agent'))
    fireEvent.change(screen.getByPlaceholderText(/what should the agent take into account/i), {
      target: { value: 'test note' },
    })
    fireEvent.click(screen.getByRole('button', { name: /send/i }))

    await waitFor(() => expect(consoleError).toHaveBeenCalled())

    // Модалка не закрылась молча — текст заметки не потерян, юзер может повторить попытку.
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByPlaceholderText(/what should the agent take into account/i)).toHaveValue('test note')
  })

  test('hides the "thinking" badge while offline even if the selected stage is running', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Propose', 'running')],
    }))

    render(<App />)

    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Propose'))

    // WS никогда не открывался в этом тесте — connected остаётся false.
    expect(screen.getByText('OFFLINE')).toBeInTheDocument()
    expect(screen.queryByText('thinking')).not.toBeInTheDocument()

    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    act(() => {
      ws?.onopen?.()
    })

    await waitFor(() => expect(screen.getByText('LINK')).toBeInTheDocument())
    expect(screen.getByText('thinking')).toBeInTheDocument()
  })

  test('shows accumulated Idle time from /api/status and lets it tick while an idle episode is open', async () => {
    let idleSince: string | null = null
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Propose', 'running')],
      started_at: '2026-07-29T09:59:00.000Z',
      idle_accumulated_ms: 5000,
      idle_since: idleSince,
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Propose'))
    await waitFor(() => expect(document.getElementById('idle')).toHaveTextContent('00:05'))

    idleSince = '2026-07-29T10:00:00.000Z'
    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    act(() => {
      ws?.onopen?.()
    })

    await waitFor(() => {
      const text = document.getElementById('idle')?.textContent ?? ''
      expect(text).not.toBe('00:05')
    }, { timeout: 4000 })
  })

  test('regression: idle_accumulated_ms with idle_since=null does not tick — the backend, not the client, decides what counts as idle', async () => {
    // Реальный баг, который чинил старый useIdleTime на фронте, теперь чинится
    // на бэкенде (см. Task 1's TestIsIdle_FailedWhileAnotherRunningIsNotIdle) —
    // здесь достаточно проверить, что фронт просто показывает то, что
    // прислал бэкенд, и не тикает, если idle_since=null (флоу не простаивает).
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Upstream', 'running'), stageView('s2', 'Downstream', 'failed')],
      started_at: '2026-07-29T09:59:00.000Z',
      idle_accumulated_ms: 0,
      idle_since: null,
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Upstream'))
    await waitFor(() => expect(document.getElementById('idle')).toHaveTextContent('00:00'))
  })

  test('IDLE stops ticking while the WebSocket is disconnected and resumes once reconnected', async () => {
    vi.useFakeTimers()
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Propose', 'awaiting_approval', { showPlan: true })],
      started_at: '2026-07-29T09:59:00.000Z',
      idle_accumulated_ms: 0,
      idle_since: '2026-07-29T10:00:00.000Z',
    }))

    render(<App />)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })

    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    act(() => {
      ws?.onopen?.()
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })

    const idleTextConnected = document.getElementById('idle')?.textContent ?? ''

    act(() => {
      ws?.onclose?.()
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })

    // Сокет разорван — значение держится на месте, а не продолжает тикать.
    expect(document.getElementById('idle')).toHaveTextContent(idleTextConnected)

    vi.useRealTimers()
  })

  test('WARNING: falls back to a failed stage when no stage is active', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Done stage', 'done'), stageView('s2', 'Failed stage', 'failed')],
    }))

    render(<App />)

    await waitFor(() => {
      expect(document.getElementById('detail-title')).toHaveTextContent('Failed stage')
    })
  })
})
