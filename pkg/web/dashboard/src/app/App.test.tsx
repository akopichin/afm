import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { App } from './App'

// Минимальный stub WebSocket — App открывает соединение при монтировании.
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
  isScript?: boolean
  showPlan?: boolean
  showDialog?: boolean
  // updatedAt параметризует episode-подпись attention. По умолчанию '' — так же, как раньше, чтобы
  // существующие тесты не менялись; тесты про повторные эпизоды одной стадии задают разные t1/t2.
  updatedAt?: string
}

// Строит один элемент нового wire-формата stages: []StageView.
function stageView(id: string, name: string, status: string, overrides: StageViewOverrides = {}) {
  const interactive = overrides.interactive ?? false
  const autonomous = overrides.autonomous ?? false
  const hasDialog = overrides.hasDialog ?? false
  const isScript = overrides.isScript ?? false

  return {
    id,
    name,
    status,
    updated_at: overrides.updatedAt ?? '',
    interactive,
    autonomous,
    auto_approve: overrides.autoApprove ?? false,
    has_dialog: hasDialog,
    is_script: isScript,
    show_plan: overrides.showPlan ?? ((status !== 'pending' && !isScript && !autonomous) || status === 'failed' || status === 'paused'),
    show_dialog: overrides.showDialog ?? (hasDialog || status === 'awaiting_user_input'),
  }
}

// Мокирует fetch для App: /api/status отдаёт statusPayload() (и считает вызовы через
// onStatusCall), остальные эндпоинты (plan/dialog) отвечают пустыми заглушками.
function mockFetchForStatus(statusPayload: () => unknown, onStatusCall?: () => void) {
  return vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const url = typeof input === 'string' ? input : (input as Request).url

    if (url.includes('/api/status')) {
      onStatusCall?.()
      // The real accounting-enabled backend always sends an `accounting` object; the Cost tab / Est. cost
      // tile are gated on its presence.
      const payload = statusPayload()
      if (payload !== null && typeof payload === 'object' && !('accounting' in payload)) {
        ;(payload as Record<string, unknown>).accounting = { health: 'ok', has_data: false, show_money: true }
      }
      return { ok: true, json: async () => payload } as Response
    }

    if (url.includes('/plan')) return { ok: true, text: async () => '' } as Response
    if (url.includes('/dialog')) return { ok: true, json: async () => [] } as Response
    // /api/mesh/* — capability off by default in this harness (no test here stands up a mesh-enabled
    // server), same nil-Service 404 convention pkg/server/mesh.go itself uses. Without this, the generic
    // catch-all below (ok:true for anything unmatched) would make useMeshRoster's own capability probe
    // misread every App test as mesh-enabled and leak a "Team" tab into exact tab-label assertions.
    if (url.includes('/api/mesh/')) return { ok: false, status: 404, json: async () => ({}) } as Response

    return { ok: true, json: async () => [] } as Response
  })
}

// Переключает воркспейс на вкладку деталей выбранной стадии. Нужна там, где тест проверяет
// наличие/отсутствие самой панели плана/диалога.
function openDetail(): void {
  const tabs = screen.getAllByRole('tab')
  const detail = tabs.find((t) => t.textContent !== 'Feed' && t.textContent !== 'Cost')
  if (detail) fireEvent.click(detail)
}

describe('App', () => {
  beforeEach(() => {
    StubWebSocket.instances = []
    vi.stubGlobal('WebSocket', StubWebSocket)
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    localStorage.clear()
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

    expect(screen.getByText('Offline')).toBeInTheDocument()
  })

  test('renders the afm version footer (rail-foot) from /api/status version', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      version: 'v1.1.28',
      stages: [stageView('s1', 'Propose', 'running')],
    }))

    render(<App />)

    // Ловит забытый проп App→StagesList: отдельные unit-тесты обоих компонентов
    // прошли бы, а строка версии всё равно не появилась бы.
    await waitFor(() => {
      expect(document.querySelector('.rail-foot')).toHaveTextContent('afm v1.1.28')
    })
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
    // React батчит три синхронных события в один commit; refresh срабатывает один раз.
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

  test('CRITICAL: WS refresh survives past the 10000-event feed cap (Finding #2)', async () => {
    let statusCalls = 0
    mockFetchForStatus(
      () => ({ flow_name: 'demo', stages: [stageView('s1', 'Propose', 'running')] }),
      () => {
        statusCalls += 1
      },
    )

    render(<App />)
    await waitFor(() => expect(statusCalls).toBe(1))

    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]

    // Забиваем ленту ВЫШЕ кэпа (MAX_EVENTS=10000, use-event-feed) незначимыми agent_action — сами по себе
    // refresh они не вызывают, но насыщают длину массива до кэпа.
    act(() => {
      for (let i = 0; i < 10005; i += 1) {
        ws?.onmessage?.({ data: JSON.stringify({ type: 'agent_action', data: { n: i }, stage_id: 's1' }) })
      }
    })
    expect(statusCalls).toBe(1)

    // Значимое событие ПОСЛЕ насыщения кэпа обязано снова триггерить refresh.
    act(() => {
      ws?.onmessage?.({ data: JSON.stringify({ type: 'stage_status_changed', data: { status: 'done' }, stage_id: 's1' }) })
    })
    await waitFor(() => expect(statusCalls).toBe(2))
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

    openDetail()
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

    openDetail()
    expect(document.getElementById('dialog-section')).not.toBeNull()
  })

  test('R2 #1 / R3 #7: a pending attention stays as a separate glowing beacon tab after returning to Feed', async () => {
    // B ждёт аппрув. Воркспейс авто-открывает его (вкладка Approval).
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Alpha', 'running'), stageView('s2', 'Beta', 'awaiting_approval')],
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Beta'))

    const feedTab = screen.getAllByRole('tab').find((t) => t.textContent === 'Feed')!
    fireEvent.click(feedTab)

    // Маяк Approval присутствует как отдельная вкладка (не Feed, не имя стадии).
    const beaconTab = screen.getAllByRole('tab').find((t) => /Approval/.test(t.textContent ?? ''))!
    expect(beaconTab).toBeDefined()
    // Клик по маяку возвращает к плану ожидающей стадии.
    fireEvent.click(beaconTab)
    await waitFor(() => expect(document.getElementById('plan-section')).not.toBeNull())
  })

  test('own attention in Feed: the plain detail tab is hidden, only the glow beacon remains (no duplicate)', async () => {
    // Одна стадия ждёт. В feed-виде средний блёклый detail-таб (имя стадии) вёл бы в ТО ЖЕ ожидание, что и
    // маяк — дубликат.
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Gamma', 'awaiting_approval')],
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Gamma'))

    // Уходим в Feed.
    const feedTab = screen.getAllByRole('tab').find((t) => t.textContent === 'Feed')!
    fireEvent.click(feedTab)

    await waitFor(() => {
      const labels = screen.getAllByRole('tab').map((t) => t.textContent ?? '')
      expect(labels.some((l) => /Approval/.test(l))).toBe(true) // маяк остался
      expect(labels.some((l) => l === 'Gamma')).toBe(false) // блёклый дубль скрыт Feed + постоянная Cost-вкладка (Task 11) + маяк + постоянная Full feed (Task
      // 6) — никакого блёклого дубля-detail сверх этих четырёх.
      expect(labels.length).toBe(4)
    })
  })

  test('done stage without plan/dialog: no dud detail tab in Feed (nothing to open)', async () => {
    // Завершённая автономная стадия без плана и диалога. В feed-виде detail-таб
    // вёл бы в ту же ленту — пустышка. Показываем только Feed.
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Alpha', 'done', { showPlan: false, showDialog: false })],
    }))

    render(<App />)
    await waitFor(() => expect(screen.getAllByRole('tab').some((t) => t.textContent === 'Feed')).toBe(true))
    // Выбираем завершённую стадию.
    fireEvent.click(document.querySelector('[data-stage-id="s1"] .stage-row') as HTMLElement)

    await waitFor(() => {
      const labels = screen.getAllByRole('tab').map((t) => t.textContent ?? '')
      // Feed + постоянная Cost-вкладка (Task 11) + постоянная Full feed
      // (Task 6); ни detail-таба, ни маяка.
      expect(labels).toEqual(['Feed', 'Cost', 'Full feed'])
    })
  })

  test('clicking an unstarted pending stage stays on Feed alongside completed stages', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [
        stageView('s1', 'Finished with plan', 'done', { showPlan: true }),
        stageView('s2', 'Finished with dialog', 'done', { hasDialog: true, showPlan: false, showDialog: true }),
        stageView('s3', 'Current', 'running'),
        stageView('s4', 'Not started', 'pending', { showPlan: false, showDialog: false }),
        stageView('s5', 'Waiting for answer', 'awaiting_user_input', { showPlan: false, showDialog: true }),
      ],
    }))

    render(<App />)

    // The waiting stage is initially auto-opened as Attention. Return to Feed first so the rail click is
    // tested in the global workspace, like the live dashboard scenario.
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Waiting for answer'))
    fireEvent.click(screen.getByRole('tab', { name: 'Feed' }))

    fireEvent.click(document.querySelector('[data-stage-id="s4"] .stage-row') as HTMLElement)

    await waitFor(() => {
      expect(screen.getByRole('tab', { name: 'Feed' })).toHaveAttribute('aria-selected', 'true')
      expect(document.getElementById('plan-section')).toBeNull()
      expect(screen.queryByText('No plan yet')).not.toBeInTheDocument()
      expect(screen.getAllByRole('tab').some((tab) => /Question/.test(tab.textContent ?? ''))).toBe(true)
    })
  })

  test('another stage awaits in Feed: a completed stage exposes a detail tab but stays on Feed', async () => {
    // Ждёт s2, а выбрана в рейле завершённая s1. Маяк указывает на чужое ожидание (s2).
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [
        stageView('s1', 'Alpha', 'done', { showPlan: true, showDialog: false }),
        stageView('s2', 'Beta', 'awaiting_approval'),
      ],
    }))

    render(<App />)
    // Воркспейс авто-открывает ожидание s2.
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Beta'))

    // Выбираем s1 — завершённая стадия не открывает историю автоматически.
    fireEvent.click(document.querySelector('[data-stage-id="s1"] .stage-row') as HTMLElement)

    await waitFor(() => {
      const labels = screen.getAllByRole('tab').map((t) => t.textContent ?? '')
      expect(screen.getByRole('tab', { name: 'Feed' })).toHaveAttribute('aria-selected', 'true')
      expect(labels.some((l) => l === 'Alpha')).toBe(true) // detail-вкладка завершённой s1
      expect(labels.some((l) => /Approval/.test(l))).toBe(true) // и маяк чужого ожидания
    })
  })

  test('R3 #7: clicking a completed stage while another stage awaits stays on Feed', async () => {
    // s1 done с планом, s2 ждёт аппрув. Смотрим s1 → остаёмся на Feed, а
    // ожидание s2 остаётся отдельной glow-вкладкой Approval.
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [
        stageView('s1', 'Done stage', 'done', { showPlan: true, showDialog: false }),
        stageView('s2', 'Waiter', 'awaiting_approval'),
      ],
    }))

    render(<App />)
    // Воркспейс авто-открывает ожидание s2.
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Waiter'))

    // Кликаем по s1 — история завершённой стадии не открывается автоматически,
    // но detail-вкладка «Done stage» становится доступной как точка входа.
    fireEvent.click(document.querySelector('[data-stage-id="s1"] .stage-row') as HTMLElement)

    const labels = screen.getAllByRole('tab').map((t) => t.textContent ?? '')
    expect(screen.getByRole('tab', { name: 'Feed' })).toHaveAttribute('aria-selected', 'true')
    expect(labels.some((l) => /Done stage/.test(l))).toBe(true)
    expect(labels.some((l) => /Approval/.test(l))).toBe(true)
  })

  test('a finished stage with both plan and dialog stays on Feed', async () => {
    // Стадия завершена и имеет и план, и диалог. Клик по строке не открывает
    // историю автоматически — Feed остаётся единой рабочей вкладкой.
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Both', 'done', { hasDialog: true, showPlan: true, showDialog: true })],
    }))

    render(<App />)
    await waitFor(() => expect(screen.getByText('Both')).toBeInTheDocument())

    // Выбираем завершённую стадию кликом по её строке.
    fireEvent.click(document.querySelector('[data-stage-id="s1"] .stage-row') as HTMLElement)

    await waitFor(() => {
      expect(screen.getByRole('tab', { name: 'Feed' })).toHaveAttribute('aria-selected', 'true')
      // detail-вкладка появилась (точка входа в историю), но история не открыта.
      expect(screen.getAllByRole('tab').some((t) => t.textContent === 'Both')).toBe(true)
      expect(screen.queryByRole('group', { name: /history view/i })).not.toBeInTheDocument()
      expect(document.getElementById('dialog-section')).toBeNull()
      expect(document.getElementById('plan-section')).toBeNull()
    })
  })

  test('done stage with plan: clicking its detail tab opens plan history', async () => {
    // Завершённая обычная стадия с планом. Клик по строке рейла оставляет Feed,
    // но detail-вкладка «Alpha» ведёт в историю плана.
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Alpha', 'done', { showPlan: true, showDialog: false })],
    }))

    render(<App />)
    await waitFor(() => expect(screen.getByText('Alpha')).toBeInTheDocument())
    fireEvent.click(document.querySelector('[data-stage-id="s1"] .stage-row') as HTMLElement)

    const detailTab = await screen.findByRole('tab', { name: 'Alpha' })
    fireEvent.click(detailTab)

    await waitFor(() => {
      expect(detailTab).toHaveAttribute('aria-selected', 'true')
      expect(document.getElementById('plan-section')).not.toBeNull()
      expect(screen.getByRole('tab', { name: 'Feed' })).toHaveAttribute('aria-selected', 'false')
    })
  })

  test('done stage with dialog only: clicking its detail tab opens dialog history', async () => {
    // Non-interactive/авто-отвеченная завершённая стадия: только диалог, без плана.
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Alpha', 'done', { hasDialog: true, showPlan: false, showDialog: true })],
    }))

    render(<App />)
    await waitFor(() => expect(screen.getByText('Alpha')).toBeInTheDocument())
    fireEvent.click(document.querySelector('[data-stage-id="s1"] .stage-row') as HTMLElement)

    const detailTab = await screen.findByRole('tab', { name: 'Alpha' })
    fireEvent.click(detailTab)

    await waitFor(() => {
      expect(document.getElementById('dialog-section')).not.toBeNull()
      expect(document.getElementById('plan-section')).toBeNull()
    })
  })

  test('done stage with plan and dialog: detail tab opens dialog history, Plan switch shows plan', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Both', 'done', { hasDialog: true, showPlan: true, showDialog: true })],
    }))

    render(<App />)
    await waitFor(() => expect(screen.getByText('Both')).toBeInTheDocument())
    fireEvent.click(document.querySelector('[data-stage-id="s1"] .stage-row') as HTMLElement)

    const detailTab = await screen.findByRole('tab', { name: 'Both' })
    fireEvent.click(detailTab)

    // По умолчанию открывается диалог (showDialog → dialog-history), а рядом есть
    // переключатель Plan|Dialog.
    await waitFor(() => {
      expect(document.getElementById('dialog-section')).not.toBeNull()
      expect(screen.getByRole('group', { name: /history view/i })).toBeInTheDocument()
    })

    fireEvent.click(screen.getByRole('button', { name: /^Plan$/ }))
    await waitFor(() => expect(document.getElementById('plan-section')).not.toBeNull())
  })

  test('auto_approve done stage: detail tab exposes the plan history', async () => {
    // auto_approve влияет только на awaiting_approval; завершённая auto_approve стадия семантически
    // идентична обычной done с планом — detail-вкладка ведёт в план.
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Auto', 'done', { autoApprove: true, showPlan: true, showDialog: false })],
    }))

    render(<App />)
    await waitFor(() => expect(screen.getByText('Auto')).toBeInTheDocument())
    fireEvent.click(document.querySelector('[data-stage-id="s1"] .stage-row') as HTMLElement)

    const detailTab = await screen.findByRole('tab', { name: 'Auto' })
    fireEvent.click(detailTab)

    await waitFor(() => expect(document.getElementById('plan-section')).not.toBeNull())
  })

  test('auto_approve done stage with plan and dialog: detail tab reaches both histories', async () => {
    // Замыкает матрицу: auto_approve + и план, и диалог. По умолчанию открывается
    // диалог, переключатель Plan даёт план — как у обычной done с обоими.
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'AutoBoth', 'done', { autoApprove: true, hasDialog: true, showPlan: true, showDialog: true })],
    }))

    render(<App />)
    await waitFor(() => expect(screen.getByText('AutoBoth')).toBeInTheDocument())
    fireEvent.click(document.querySelector('[data-stage-id="s1"] .stage-row') as HTMLElement)

    const detailTab = await screen.findByRole('tab', { name: 'AutoBoth' })
    fireEvent.click(detailTab)

    await waitFor(() => {
      expect(document.getElementById('dialog-section')).not.toBeNull()
      expect(screen.getByRole('group', { name: /history view/i })).toBeInTheDocument()
    })

    fireEvent.click(screen.getByRole('button', { name: /^Plan$/ }))
    await waitFor(() => expect(document.getElementById('plan-section')).not.toBeNull())
  })

  test('selecting a RUNNING stage with dialog history while on Feed stays on Feed', async () => {
    // Репорт: пользователь ответил на вопрос, кликнул другую стадию, потом вернулся к ней — воркспейс
    // прыгал в dialog-history вместо того чтобы остаться в Feed.
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [
        stageView('s1', 'Worker', 'running', { showPlan: false, showDialog: false }),
        stageView('s2', 'Asker', 'running', { hasDialog: true, showDialog: true }),
      ],
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Worker'))

    fireEvent.click(screen.getByRole('tab', { name: 'Feed' }))
    fireEvent.click(document.querySelector('[data-stage-id="s1"] .stage-row') as HTMLElement)
    fireEvent.click(document.querySelector('[data-stage-id="s2"] .stage-row') as HTMLElement)

    await waitFor(() => {
      expect(screen.getByRole('tab', { name: 'Feed' })).toHaveAttribute('aria-selected', 'true')
      expect(document.getElementById('dialog-section')).toBeNull()
    })
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

    openDetail()
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

  test('CRITICAL: a pending action on a NON-selected stage surfaces itself (Finding #1)', async () => {
    // Стадия B входит в awaiting_approval, пока выбрана и работает стадия A. Воркспейс обязан сам открыть
    // ожидание B (его нельзя «потерять»), а не остаться на A.
    let awaiting = false
    mockFetchForStatus(() =>
      awaiting
        ? {
            flow_name: 'demo',
            stages: [stageView('s1', 'Alpha', 'running'), stageView('s2', 'Beta', 'awaiting_approval')],
          }
        : {
            flow_name: 'demo',
            stages: [stageView('s1', 'Alpha', 'running'), stageView('s2', 'Beta', 'running')],
          },
    )

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Alpha'))

    awaiting = true
    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    act(() => {
      ws?.onmessage?.({ data: JSON.stringify({ type: 'stage_status_changed', data: { status: 'awaiting_approval' }, stage_id: 's2' }) })
    })

    // Воркспейс переключился на ожидание B и показал баннер аппрува.
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Beta'))
    expect(screen.getByText('Plan needs your approval')).not.toBeNull()
  })

  test('CRITICAL: an interactive question fills the workspace alone — no plan panel beside it (Finding #2)', async () => {
    // interactive-стадия в awaiting_user_input: сервер отдаёт и show_plan, и show_dialog=true. Теперь
    // показываем ровно одну панель — диалог — на всю высоту, без плана рядом.
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Ask', 'awaiting_user_input', { interactive: true, hasDialog: true, showPlan: true, showDialog: true })],
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Ask'))
    await waitFor(() => expect(document.getElementById('dialog-section')).not.toBeNull())
    // Исторической панели плана рядом с вопросом быть НЕ должно.
    expect(document.getElementById('plan-section')).toBeNull()
    expect(screen.getByText('Agent needs your input')).not.toBeNull()
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
    // Кликаем по строке-кнопке (её текст), а не по <li>: интерактивен теперь .stage-row.
    fireEvent.click(document.querySelector('[data-stage-id="s1"] .stage-row') as HTMLElement)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Propose'))

    // Даём эффектам/поллингу шанс (ошибочно) перекинуть выбор — он обязан остаться на s1.
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 30))
    })
    expect(document.getElementById('detail-title')).toHaveTextContent('Propose')
  })

  test('CRITICAL: keeps retrying auto-advance across polls instead of sticking forever (fast back-to-back script stages)', async () => {
    // Root cause of the reported bug: the old check fired only in the exact tick the selected stage
    // transitioned to done, searching forward for an already-active stage right then.
    let phase: 'running' | 'gap' | 'caught-up' = 'running'
    mockFetchForStatus(() => {
      if (phase === 'running') {
        return {
          flow_name: 'demo',
          stages: [stageView('s1', 'Script 1', 'running'), stageView('s2', 'Script 2', 'pending'), stageView('s3', 'Script 3', 'pending')],
        }
      }
      if (phase === 'gap') {
        // s1 just finished; s2 hasn't started yet in this exact snapshot — nothing is active.
        return {
          flow_name: 'demo',
          stages: [stageView('s1', 'Script 1', 'done'), stageView('s2', 'Script 2', 'pending'), stageView('s3', 'Script 3', 'pending')],
        }
      }
      // s2 raced through to done too while nobody was watching; s3 is now active.
      return {
        flow_name: 'demo',
        stages: [stageView('s1', 'Script 1', 'done'), stageView('s2', 'Script 2', 'done'), stageView('s3', 'Script 3', 'running')],
      }
    })

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Script 1'))

    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]

    phase = 'gap'
    act(() => {
      ws?.onmessage?.({ data: JSON.stringify({ type: 'stage_status_changed', data: { status: 'done' }, stage_id: 's1' }) })
    })
    // Nothing downstream is active yet — selection correctly stays on s1 (not an error state).
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Script 1'))

    phase = 'caught-up'
    act(() => {
      ws?.onmessage?.({ data: JSON.stringify({ type: 'stage_status_changed', data: { status: 'running' }, stage_id: 's3' }) })
    })
    // The old one-shot check would stay stuck on s1 forever here — this is the fix under test.
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Script 3'))
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

  test('CRITICAL: a failed /revise from the feed composer keeps the typed note instead of clearing it', async () => {
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
      // Стадия ушла из running за время, пока юзер печатал заметку → 409.
      if (url.includes('/revise')) {
        return { ok: false, status: 409, json: async () => ({}) } as Response
      }
        if (url.includes('/plan')) return { ok: true, text: async () => '' } as Response
      if (url.includes('/dialog')) return { ok: true, json: async () => [] } as Response
      // /api/mesh/* — capability off by default (see mockFetchForStatus's own comment): without this the
      // generic catch-all below would make useMeshRoster misread this test as mesh-enabled and swap the
      // Revise composer's "Send note to agent" button for the mesh-mode "Apply Revise" label.
      if (url.includes('/api/mesh/')) return { ok: false, status: 404, json: async () => ({}) } as Response

      return { ok: true, json: async () => [] } as Response
    })

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Propose'))

    // Композер живёт только в ленте — открываем её.
    const feedTab = screen.getAllByRole('tab').find((t) => t.textContent === 'Feed')!
    fireEvent.click(feedTab)

    const composer = screen.getByPlaceholderText(/note to agent/i)
    fireEvent.change(composer, { target: { value: 'test note' } })
    fireEvent.click(screen.getByRole('button', { name: /send note to agent/i }))

    // /revise вернул 409 — reviseStage отклонился, FeedComposer НЕ очистил поле:
    // текст сохранён, юзер может повторить попытку.
    await waitFor(() => expect(composer).toHaveValue('test note'))
  })

  test('a failed /pause POST is logged, not left as an unhandled rejection', async () => {
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
      // Стадия успела уйти из pausable-статуса за время между открытием кебаба и кликом.
      if (url.includes('/pause')) {
        return { ok: false, status: 409, json: async () => ({}) } as Response
      }
        if (url.includes('/plan')) return { ok: true, text: async () => '' } as Response
      if (url.includes('/dialog')) return { ok: true, json: async () => [] } as Response

      return { ok: true, json: async () => [] } as Response
    })

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Propose'))

    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    fireEvent.click(screen.getByText('Pause'))

    await waitFor(() => expect(consoleError).toHaveBeenCalledWith('Failed to pause stage:', expect.anything()))
  })

  test('hides the "thinking" badge while offline even if the selected stage is running', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Propose', 'running')],
    }))

    render(<App />)

    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Propose'))

    // WS никогда не открывался в этом тесте — connected остаётся false.
    expect(screen.getByText('Offline')).toBeInTheDocument()
    expect(screen.queryByText('thinking')).not.toBeInTheDocument()

    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    act(() => {
      ws?.onopen?.()
    })

    await waitFor(() => expect(screen.getByText('Agent online')).toBeInTheDocument())
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

  test('Elapsed stays at the persisted end time while the completed dashboard is open', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-09-25T10:00:10Z'))
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Done', 'done')],
      started_at: '2026-09-25T10:00:00Z',
      run_status: 'finished',
      ended_at: '2026-09-25T10:00:04Z',
      elapsed_accumulated_ms: 4000,
    }))
    render(<App />)
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(document.getElementById('elapsed')).toHaveTextContent('00:04')
    await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
    expect(document.getElementById('elapsed')).toHaveTextContent('00:04')
  })

  test('regression: idle_accumulated_ms with idle_since=null does not tick — the backend, not the client, decides what counts as idle', async () => {
    // Показываем backend Idle без тика при idle_since=null.
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Upstream', 'running'), stageView('s2', 'Downstream', 'failed')],
      started_at: '2026-07-29T09:59:00.000Z',
      idle_accumulated_ms: 0,
      idle_since: null,
    }))

    render(<App />)
    // Воркспейс авто-открывает стадию, ждущую действия: 'Downstream' — это attention-элемент и всплывает
    // сам, а не 'Upstream'.
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Downstream'))
    await waitFor(() => expect(document.getElementById('idle')).toHaveTextContent('00:00'))
  })

  test('IDLE keeps ticking while HTTP status polling works after a WebSocket disconnect', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-07-29T10:00:00.000Z'))
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Propose', 'awaiting_approval')],
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

    expect(document.getElementById('idle')).toHaveTextContent('00:00')

    act(() => {
      ws?.onclose?.()
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })

    // /api/status продолжает отвечать, поэтому отсутствие WS не замораживает Idle.
    expect(document.getElementById('idle')).toHaveTextContent('00:05')

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

  test('renders a Full feed tab as the last tab', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Alpha', 'running')],
      accounting: { health: 'ok', has_data: false },
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Alpha'))

    const tabs = screen.getAllByRole('tab')
    expect(tabs[tabs.length - 1]).toHaveTextContent('Full feed')
  })

  test('Feed tab shows only the selected stage; Full feed shows all', async () => {
    // Feed больше не фильтрует общий поток через тумблер — она читает уже отфильтрованную стадией ленту.
    // Full feed — отдельная постоянная вкладка, читающая весь поток событий флоу.
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Alpha', 'running'), stageView('s2', 'Beta', 'pending')],
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Alpha'))

    // agent_action не значим (не триггерит рефетч и смену выбора) — набиваем ленту.
    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    act(() => {
      ws?.onmessage?.({ data: JSON.stringify({ type: 'agent_action', data: { tool: 'read_file', detail: 'a.ts' }, stage_id: 's1' }) })
      ws?.onmessage?.({ data: JSON.stringify({ type: 'agent_action', data: { tool: 'read_file', detail: 'b.ts' }, stage_id: 's2' }) })
    })

    // Default view — Feed для выбранной s1: только её событие видно.
    await waitFor(() => expect(document.getElementById('feed-content')?.textContent).toContain('read_file: a.ts'))
    expect(document.getElementById('feed-content')?.textContent).not.toContain('read_file: b.ts')

    // Full feed → видны события ВСЕХ стадий.
    fireEvent.click(screen.getByRole('tab', { name: 'Full feed' }))
    await waitFor(() => expect(document.getElementById('feed-content')?.textContent).toContain('read_file: b.ts'))
    expect(document.getElementById('feed-content')?.textContent).toContain('read_file: a.ts')
  })

  test('no This stage / All toggle exists', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Alpha', 'running'), stageView('s2', 'Beta', 'pending')],
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Alpha'))

    expect(screen.queryByRole('button', { name: 'All' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'This stage' })).not.toBeInTheDocument()

    // Тумблер не появляется и в Full feed — там его тоже никогда не было.
    fireEvent.click(screen.getByRole('tab', { name: 'Full feed' }))
    expect(screen.queryByRole('button', { name: 'All' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'This stage' })).not.toBeInTheDocument()
  })

  test('a stale selection outside current stages falls back to the global Feed empty hint', async () => {
    // Если выбранная стадия исчезла и активных нет, workspace показывает нейтральный empty hint.
    let swapped = false
    mockFetchForStatus(() =>
      swapped
        ? { flow_name: 'demo', stages: [stageView('s9', 'Later', 'pending')] }
        : { flow_name: 'demo', stages: [stageView('s1', 'Alpha', 'running')] },
    )

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Alpha'))

    swapped = true
    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    act(() => {
      ws?.onmessage?.({ data: JSON.stringify({ type: 'stage_status_changed', data: { status: 'done' }, stage_id: 's1' }) })
    })

    await waitFor(() => {
      expect(document.getElementById('feed-content')?.textContent).toContain('Select a stage to see its feed')
    })
  })

  test('Cost tab: opening it hides the stage-scoped WorkspaceHeader and shows the cost report', async () => {
    // Task 11: Cost — глобальный отчёт, не привязанный к выбранной стадии.
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Propose', 'running')],
      accounting: { health: 'ok', has_data: false },
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Propose'))

    fireEvent.click(screen.getByRole('tab', { name: 'Cost' }))

    await waitFor(() => expect(screen.getByText('No usage data')).toBeInTheDocument())
    expect(document.getElementById('detail-title')).not.toBeInTheDocument()
  })

  test('Cost tab is absent when accounting is unsupported (display switch off)', async () => {
    // Disabled accounting hides all cost UI, including the permanent Cost tab.
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Propose', 'running')],
      accounting: null,
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Propose'))

    const labels = screen.getAllByRole('tab').map((t) => t.textContent ?? '')
    expect(labels).not.toContain('Cost')
    expect(screen.queryByRole('button', { name: /est\. cost/i })).toBeNull()
  })

  test('Cost tab is always right before the trailing Full feed tab, after the contextual detail/beacon tabs', async () => {
    // Живой ревью-баг: Cost был второй вкладкой сразу после Feed, поэтому detail-таб выбранной стадии и
    // attention-маяк оказывались ПОСЛЕ Cost — Cost «застревала» посередине списка.
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [
        stageView('s1', 'Greet', 'done', { showPlan: true, showDialog: false }),
        stageView('s2', 'Approve', 'awaiting_approval'),
      ],
    }))

    render(<App />)
    // Воркспейс авто-открывает ожидание s2.
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Approve'))

    // Выбираем s1 — одновременно остаются Feed и маяк чужого ожидания.
    fireEvent.click(document.querySelector('[data-stage-id="s1"] .stage-row') as HTMLElement)

    await waitFor(() => {
      const labels = screen.getAllByRole('tab').map((t) => t.textContent ?? '')
      expect(labels[0]).toBe('Feed')
      expect(labels[labels.length - 1]).toBe('Full feed')
      expect(labels[labels.length - 2]).toBe('Cost')
      expect(labels).toContain('Greet') // detail-вкладка завершённой s1
      expect(labels.some((l) => /Approval/.test(l))).toBe(true)
    })

    // Клик по Cost по-прежнему активирует вкладку отчёта, даже будучи предпоследней.
    fireEvent.click(screen.getByRole('tab', { name: 'Cost' }))
    await waitFor(() => expect(screen.getByRole('tab', { name: 'Cost' })).toHaveAttribute('aria-selected', 'true'))
  })

  test('FIX 1: Cost view never shows a redundant stage-detail tab alongside the attention beacon', async () => {
    // Global Cost must not duplicate stage detail tabs; expected order is Feed, Approval, Cost, Full feed.
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Propose', 'awaiting_approval')],
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Propose'))

    fireEvent.click(screen.getByRole('tab', { name: 'Cost' }))

    await waitFor(() => {
      const labels = screen.getAllByRole('tab').map((t) => t.textContent ?? '')
      expect(labels).toEqual(['Feed', expect.stringMatching(/Approval/), 'Cost', 'Full feed'])
    })
  })

  test('FIX 3: activating Cost from the RunMetrics "…" popover moves focus onto the Cost tab', async () => {
    // Round-5 #6 спеки: after the popover closes and Cost opens, focus must land on the destination (the
    // Cost tab button), not linger on the now out-of-context "…" disclosure button.
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Propose', 'running')],
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Propose'))

    fireEvent.click(screen.getByRole('button', { name: /show all run metrics/i }))
    const popover = screen.getByRole('group', { name: /all run metrics/i })
    fireEvent.click(within(popover).getByRole('button', { name: /est\. cost/i }))

    await waitFor(() => {
      const costTab = screen.getByRole('tab', { name: 'Cost' })
      expect(costTab).toHaveAttribute('aria-selected', 'true')
      expect(document.activeElement).toBe(costTab)
    })
  })

  test('Cost panel: a long stage list with several expanded rows keeps Total reachable via its own scroll container', async () => {
    const manyStages = Array.from({ length: 20 }, (_, i) => ({
      ...stageView(`s${i}`, `Stage ${i}`, 'done'),
      cost: {
        display_cost: '$1.00',
        coverage: 'full',
        estimated_cost_usd: 1,
        metered: 1,
        priced_invocations: 1,
        unpriced: 0,
        unmetered: 0,
        models: ['claude'],
        uncached_input: 10,
        cache_read: 10,
        cache_write_5m: 0,
        cache_write_1h: 0,
        cache_write_other: 0,
        output: 10,
        reasoning_output: 0,
        total_tokens: 30,
        cache_write_total: 0,
        cache_hit_ratio: 0.5,
        phases: { implementation: 1 },
      },
    }))

    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: manyStages,
      run_cost: {
        display_cost: '$20.00',
        coverage: 'full',
        estimated_cost_usd: 20,
        metered: 20,
        priced_invocations: 20,
        unpriced: 0,
        unmetered: 0,
        models: ['claude'],
        uncached_input: 200,
        cache_read: 200,
        cache_write_5m: 0,
        cache_write_1h: 0,
        cache_write_other: 0,
        output: 200,
        reasoning_output: 0,
        total_tokens: 600,
        cache_write_total: 0,
        cache_hit_ratio: 0.5,
        phases: {},
      },
      accounting: { health: 'ok', has_data: true, show_money: true },
    }))

    render(<App />)
    await waitFor(() => expect(screen.getByText('demo')).toBeInTheDocument())

    fireEvent.click(screen.getByRole('tab', { name: 'Cost' }))
    const table = await screen.findByRole('table')

    // Раскрываем несколько строк ВНУТРИ таблицы, а не любую кнопку с именем "Stage N" — в рейле слева тоже
    // есть кнопки-строки с такими же именами.
    const expandButtons = within(table).getAllByRole('button', { name: /Stage \d+/ })
    fireEvent.click(expandButtons[0] as HTMLElement)
    fireEvent.click(expandButtons[5] as HTMLElement)
    fireEvent.click(expandButtons[10] as HTMLElement)

    const totalRow = within(table).getByText('Total').closest('tr')
    expect(totalRow).not.toBeNull()
    expect(totalRow).toHaveTextContent('$20.00')
    expect(document.querySelector('.cost-panel-scroll')).not.toBeNull()
  })

  // Task 6: клик по dialog_question/dialog_answer элементу ленты должен выбрать стадию вопроса и открыть
  // её диалог — live-вопрос, если стадия сейчас awaiting_user_input, иначе read-only историю.
  test('клик по dialog_question в ленте для awaiting_user_input стадии выбирает её и показывает live-вопрос', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [
        stageView('s1', 'Alpha', 'running'),
        stageView('s2', 'Ask', 'awaiting_user_input', { interactive: true, hasDialog: true, showDialog: true }),
      ],
    }))

    render(<App />)
    // s2 — единственная стадия с attention-статусом → авто-открытие на маунте
    // (rule 9) сразу фокусирует воркспейс на её live-вопросе.
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Ask'))
    expect(document.getElementById('dialog-section')).not.toBeNull()

    // Возвращаемся в Feed вручную — сигнатура авто-открытия для s2 уже потрачена (rule 9: ровно один раз
    // на прибытие), так что редьюсер не перебросит нас назад сам.
    fireEvent.click(screen.getByRole('tab', { name: 'Feed' }))
    await waitFor(() => expect(document.getElementById('dialog-section')).toBeNull())

    // Клик по Feed вернул выбор в attention-эпизод s2 — Feed выбранной стадии уже показывает события s2.
    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    act(() => {
      ws?.onmessage?.({
        data: JSON.stringify({
          type: 'dialog_question',
          data: { phase: 'planning', id: 'q1', title: 'what next?' },
          stage_id: 's2',
        }),
      })
    })

    const feedButton = await screen.findByRole('button', { name: /what next\?/ })
    fireEvent.click(feedButton)

    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Ask'))
    expect(document.getElementById('dialog-section')).not.toBeNull()
    expect(screen.getByText('Agent needs your input')).not.toBeNull()
  })

  test('клик по dialog_answer в ленте для завершённой стадии открывает историю диалога, а не live-вопрос', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [
        stageView('s1', 'Alpha', 'running'),
        stageView('s2', 'Done', 'done', { hasDialog: true, showDialog: true }),
      ],
    }))

    render(<App />)
    // s2 не в attention-статусе → авто-выбор оставляет активную s1, Feed открыт.
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Alpha'))
    expect(document.getElementById('dialog-section')).toBeNull()

    // s2 не выбрана → per-stage Feed (s1) не покажет её событие; переключаемся
    // на Full feed, чтобы увидеть событие s2 в ленте.
    fireEvent.click(screen.getByRole('tab', { name: 'Full feed' }))

    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    act(() => {
      ws?.onmessage?.({
        data: JSON.stringify({
          type: 'dialog_answer',
          data: { phase: 'implementation', id: 'q9', title: 'reply text' },
          stage_id: 's2',
        }),
      })
    })

    const feedButton = await screen.findByRole('button', { name: /reply text/ })
    fireEvent.click(feedButton)

    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Done'))
    expect(document.getElementById('dialog-section')).not.toBeNull()
    // История, не live-вопрос — баннер ожидания ответа не должен показываться.
    expect(screen.queryByText('Agent needs your input')).not.toBeInTheDocument()
  })

  // Non-interactive стадия с auto_approve авто-ответила на вопрос агента.
  test('авто-ответ и вопрос завершённой auto_approve стадии навигируемы и переживают reload', async () => {
    const feedEvents = [
      {
        type: 'dialog_question',
        stage_id: 's1',
        data: { phase: 'implementation', id: 'q1', title: 'Which backend?' },
        timestamp: '2026-09-29T10:00:00.000Z',
      },
      {
        type: 'auto_answered',
        stage_id: 's1',
        data: {
          phase: 'implementation',
          id: 'q1',
          answer: 'Use Postgres',
          from_options: true,
          question_title: 'Which backend?',
          dialog: true,
        },
        timestamp: '2026-09-29T10:00:01.000Z',
      },
    ]

    // fetch маршрутизируется по URL: /api/status → payload; любой /api/events → feedEvents из истории;
    // /dialog → полная запись вопроса и авто-ответа. Порядок веток важен (dialog до events).
    function mockFetchForFeed() {
      return vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
        const url = typeof input === 'string' ? input : (input as Request).url
        if (url.includes('/api/status')) {
          return {
            ok: true,
            json: async () => ({
              flow_name: 'demo',
              accounting: { health: 'ok', has_data: false, show_money: true },
              stages: [
                stageView('s1', 'Archive', 'done', { autoApprove: true, hasDialog: true, showDialog: true }),
                stageView('s2', 'Build', 'running'),
                stageView('s3', 'Ship', 'pending'),
              ],
            }),
          } as Response
        }
        if (url.includes('/dialog')) {
          return {
            ok: true,
            json: async () => [
              { phase: 'implementation', id: 'q1', question: 'Which backend?', options: ['Use Postgres', 'Use SQLite'] },
              { phase: 'implementation', id: 'q1', answer: 'Use Postgres', auto_answered: true },
            ],
          } as Response
        }
        if (url.includes('/api/events')) return { ok: true, json: async () => feedEvents } as Response
        if (url.includes('/plan')) return { ok: true, text: async () => '' } as Response
        return { ok: true, json: async () => [] } as Response
      })
    }

    // Общие проверки, одинаковые до и после reload: выбрать завершённую s1,
    // увидеть в ленте обе строки, кликнуть по каждой и попасть в один диалог.
    async function assertBothNavigableToDialog() {
      // Авто-выбор берёт активную s2 (running). Выбираем завершённую s1 в рейле.
      await waitFor(() => expect(document.querySelector('[data-stage-id="s1"] .stage-row')).not.toBeNull())
      fireEvent.click(document.querySelector('[data-stage-id="s1"] .stage-row') as HTMLElement)

      // Лента показывает ОБЕ строки как кликабельные кнопки.
      const questionBtn = await screen.findByRole('button', { name: /Which backend\?/ })
      expect(await screen.findByRole('button', { name: /Use Postgres/ })).toBeInTheDocument()

      // Клик по вопросу открывает диалог стадии s1.
      fireEvent.click(questionBtn)
      await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Archive'))
      expect(document.getElementById('dialog-section')).not.toBeNull()

      // Возврат в Feed и клик по авто-ответу открывает ТОТ ЖЕ диалог.
      fireEvent.click(screen.getByRole('tab', { name: 'Feed' }))
      const answerBtnAgain = await screen.findByRole('button', { name: /Use Postgres/ })
      fireEvent.click(answerBtnAgain)
      await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Archive'))
      expect(document.getElementById('dialog-section')).not.toBeNull()
    }

    // До reload.
    mockFetchForFeed()
    const first = render(<App />)
    await waitFor(() => expect(screen.getByText('demo')).toBeInTheDocument())
    await assertBothNavigableToDialog()

    // Reload: полностью размонтируем и монтируем заново. Никаких WS-сообщений
    // не эмитим — контент обязан восстановиться ТОЛЬКО из HTTP-истории.
    first.unmount()
    render(<App />)
    await waitFor(() => expect(screen.getByText('demo')).toBeInTheDocument())
    await assertBothNavigableToDialog()
  })

  test('pause on focus: focusing the composer pauses; sending continues before revise', async () => {
    // Toggle-предпочтение читается из localStorage при монтировании.
    localStorage.setItem('afm.pauseOnFocus', 'true')

    // Собираем порядок мутирующих POST-ов (pause/continue/revise) в один журнал.
    const posts: string[] = []
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      const method = (init?.method ?? 'GET').toUpperCase()

      if (url.includes('/api/status')) {
        return {
          ok: true,
          json: async () => ({
            flow_name: 'demo',
            stages: [stageView('s1', 'Propose', 'running')],
            accounting: { health: 'ok', has_data: false, show_money: true },
          }),
        } as Response
      }
      if (method === 'POST' && url.includes('/pause')) {
        posts.push('pause')
        return { ok: true, json: async () => ({}) } as Response
      }
      if (method === 'POST' && url.includes('/continue')) {
        posts.push('continue')
        return { ok: true, json: async () => ({}) } as Response
      }
      if (method === 'POST' && url.includes('/revise')) {
        posts.push('revise')
        return { ok: true, json: async () => ({}) } as Response
      }
      if (url.includes('/plan')) return { ok: true, text: async () => '' } as Response
      if (url.includes('/dialog')) return { ok: true, json: async () => [] } as Response
      // /api/mesh/* — capability off by default (see mockFetchForStatus's own comment).
      if (url.includes('/api/mesh/')) return { ok: false, status: 404, json: async () => ({}) } as Response
      return { ok: true, json: async () => [] } as Response
    })

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Propose'))

    // Композер живёт в ленте — открываем её.
    const feedTab = screen.getAllByRole('tab').find((t) => t.textContent === 'Feed')!
    fireEvent.click(feedTab)

    const composer = screen.getByPlaceholderText(/note to agent/i)

    // Фокус → стадия ставится на паузу (POST /pause), владение отмечается.
    fireEvent.focus(composer)
    await waitFor(() => expect(posts).toContain('pause'))

    // Отправка при владении: continue ДО revise.
    fireEvent.change(composer, { target: { value: 'note while paused' } })
    fireEvent.click(screen.getByRole('button', { name: /send note to agent/i }))

    await waitFor(() => expect(posts).toContain('revise'))
    expect(posts.indexOf('continue')).toBeLessThan(posts.indexOf('revise'))
  })

  test('pause on focus: a self-owned paused stage keeps the feed composer (draft) and does not auto-open the paused attention view', async () => {
    // Наша pause при непустом draft сохраняет composer и подавляет auto-attention, чтобы не потерять ввод.
    localStorage.setItem('afm.pauseOnFocus', 'true')

    let paused = false
    const posts: string[] = []
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      const method = (init?.method ?? 'GET').toUpperCase()

      if (url.includes('/api/status')) {
        return {
          ok: true,
          json: async () => ({
            flow_name: 'demo',
            stages: [stageView('s1', 'Propose', paused ? 'paused' : 'running')],
            accounting: { health: 'ok', has_data: false, show_money: true },
          }),
        } as Response
      }
      if (method === 'POST' && url.includes('/pause')) {
        posts.push('pause')
        paused = true // следующий /api/status отдаст стадию уже на паузе
        return { ok: true, json: async () => ({}) } as Response
      }
      if (method === 'POST' && url.includes('/continue')) {
        posts.push('continue')
        return { ok: true, json: async () => ({}) } as Response
      }
      if (url.includes('/plan')) return { ok: true, text: async () => '' } as Response
      if (url.includes('/dialog')) return { ok: true, json: async () => [] } as Response
      return { ok: true, json: async () => [] } as Response
    })

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Propose'))

    const feedTab = screen.getAllByRole('tab').find((t) => t.textContent === 'Feed')!
    fireEvent.click(feedTab)

    const composer = screen.getByPlaceholderText(/note to agent/i)
    fireEvent.focus(composer)
    fireEvent.change(composer, { target: { value: 'draft note' } })
    // Дожидаемся, что pause применился (владение подтверждено).
    await waitFor(() => expect(posts).toContain('pause'))

    // Blur с НЕпустым черновиком не возобновляет стадию (владение сохраняется).
    fireEvent.blur(composer)

    // Сервер теперь отдаёт стадию на паузе; значимое WS-событие триггерит рефетч.
    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    act(() => {
      ws?.onmessage?.({ data: JSON.stringify({ type: 'stage_status_changed', data: { status: 'paused' }, stage_id: 's1' }) })
    })

    // Композер остался, черновик цел; воркспейс не ушёл в attention (Feed активна),
    // и стадию никто не возобновил самопроизвольно.
    await waitFor(() => expect(screen.getByPlaceholderText(/note to agent/i)).toHaveValue('draft note'))
    expect(screen.getByRole('tab', { name: 'Feed' })).toHaveAttribute('aria-selected', 'true')
    expect(posts).not.toContain('continue')
  })

  // Используем настоящие focus()/blur(): fireEvent не меняет document.activeElement и не проверяет
  // suppression.

  // Хелпер: значимое WS-событие → App ре-запрашивает /api/status (мутируемый
  // payload уже обновлён вызывающим ДО этого вызова).
  function emitStageStatus(stageId: string, status: string): void {
    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    act(() => {
      ws?.onmessage?.({ data: JSON.stringify({ type: 'stage_status_changed', data: { status }, stage_id: stageId }) })
    })
  }

  const QUESTION_FLAGS = { interactive: true, hasDialog: true, showDialog: true } as const

  test('Question while Feed textarea is focused: Feed stays, beacon shows, draft preserved; blur auto-opens', async () => {
    let asking = false
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [
        stageView('s1', 'Alpha', 'running'),
        stageView('s2', 'Beta', asking ? 'awaiting_user_input' : 'running', asking ? QUESTION_FLAGS : {}),
      ],
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Alpha'))
    fireEvent.click(screen.getByRole('tab', { name: 'Feed' }))

    // Фокус composer'а через РЕАЛЬНЫЙ .focus() → useIsEditing=true (suppression).
    const composer = screen.getByPlaceholderText(/note to agent/i)
    act(() => composer.focus())
    expect(document.activeElement).toBe(composer)
    fireEvent.change(composer, { target: { value: 'my draft' } })

    // Пока идёт ввод — s2 задаёт вопрос: он ОТКЛАДЫВАЕТСЯ (маяк), фокус не крадётся.
    asking = true
    emitStageStatus('s2', 'awaiting_user_input')

    await waitFor(() => expect(screen.getAllByRole('tab').some((t) => /Question/.test(t.textContent ?? ''))).toBe(true))
    expect(screen.getByRole('tab', { name: 'Feed' })).toHaveAttribute('aria-selected', 'true')
    // Черновик цел — воркспейс не размонтировался под suppression.
    expect(screen.getByPlaceholderText(/note to agent/i)).toHaveValue('my draft')

    // Снятие фокуса — всё ещё актуальный вопрос авто-открывается.
    act(() => composer.blur())
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Beta'))
    expect(document.getElementById('dialog-section')).not.toBeNull()
  })

  test('Question resolves before blur: no late auto-open, no transient navigation', async () => {
    let asking = false
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [
        stageView('s1', 'Alpha', 'running'),
        stageView('s2', 'Beta', asking ? 'awaiting_user_input' : 'running', asking ? QUESTION_FLAGS : {}),
      ],
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Alpha'))
    fireEvent.click(screen.getByRole('tab', { name: 'Feed' }))

    const composer = screen.getByPlaceholderText(/note to agent/i)
    act(() => composer.focus())
    fireEvent.change(composer, { target: { value: 'my draft' } })

    // Вопрос пришёл (отложен под фокусом), маяк засветился.
    asking = true
    emitStageStatus('s2', 'awaiting_user_input')
    await waitFor(() => expect(screen.getAllByRole('tab').some((t) => /Question/.test(t.textContent ?? ''))).toBe(true))

    // Вопрос РАЗРЕШИЛСЯ до blur (ответ пришёл в другой вкладке/через API) —
    // дожидаемся исчезновения маяка (снимок running применён).
    asking = false
    emitStageStatus('s2', 'running')
    await waitFor(() => expect(screen.getAllByRole('tab').some((t) => /Question/.test(t.textContent ?? ''))).toBe(false))

    // Только теперь blur: release нечего открывать — ни позднего auto-open, ни
    // мелькнувшей навигации в Question (иначе detail-title стал бы 'Beta').
    act(() => composer.blur())
    await act(async () => { await new Promise((r) => setTimeout(r, 20)) })
    expect(screen.getByRole('tab', { name: 'Feed' })).toHaveAttribute('aria-selected', 'true')
    expect(document.getElementById('detail-title')).toHaveTextContent('Alpha')
    expect(document.getElementById('dialog-section')).toBeNull()
  })

  test('Overlay defers, close releases: question stays a beacon under a modal, auto-opens on close', async () => {
    let asking = false
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [
        stageView('s1', 'Alpha', 'running'),
        stageView('s2', 'Beta', asking ? 'awaiting_user_input' : 'running', asking ? QUESTION_FLAGS : {}),
        stageView('s3', 'Gamma', 'pending', { showPlan: false, showDialog: false }),
      ],
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Alpha'))
    fireEvent.click(screen.getByRole('tab', { name: 'Feed' }))

    // Открываем модалку pre-note на pending s3 — anyModalOpen=true. Проводка App→hook, которую
    // reducer-тест с абстрактным suppressed не доказывает.
    const s3Kebab = within(document.querySelector('[data-stage-id="s3"]') as HTMLElement).getByRole('button', { name: /more actions/i })
    fireEvent.click(s3Kebab)
    fireEvent.click(screen.getByText('Add note (before start)'))
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument())

    // Вопрос s2 приходит, пока открыт overlay: только светится, Feed под ним цел.
    asking = true
    emitStageStatus('s2', 'awaiting_user_input')
    await waitFor(() => expect(screen.getAllByRole('tab').some((t) => /Question/.test(t.textContent ?? ''))).toBe(true))
    expect(screen.getByRole('tab', { name: 'Feed' })).toHaveAttribute('aria-selected', 'true')
    // Шорткат к ждущему действию виден в шапке модалки (Finding #4).
    expect(screen.getByRole('button', { name: /Question waiting/ })).toBeInTheDocument()

    // Закрываем overlay — актуальный вопрос авто-открывается.
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Beta'))
    expect(document.getElementById('dialog-section')).not.toBeNull()
  })

  test('Pause-on-focus op does not consume another stage\'s question; finishing the op releases it', async () => {
    localStorage.setItem('afm.pauseOnFocus', 'true')

    let s1Status = 'running'
    let s2Status = 'running'
    const posts: string[] = []
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      const method = (init?.method ?? 'GET').toUpperCase()
      if (url.includes('/api/status')) {
        return {
          ok: true,
          json: async () => ({
            flow_name: 'demo',
            accounting: { health: 'ok', has_data: false, show_money: true },
            stages: [
              stageView('s1', 'Alpha', s1Status),
              stageView('s2', 'Beta', s2Status, s2Status === 'awaiting_user_input' ? QUESTION_FLAGS : {}),
            ],
          }),
        } as Response
      }
      if (method === 'POST' && url.includes('/pause')) { posts.push('pause'); s1Status = 'paused'; return { ok: true, json: async () => ({}) } as Response }
      if (method === 'POST' && url.includes('/continue')) { posts.push('continue'); s1Status = 'running'; return { ok: true, json: async () => ({}) } as Response }
      if (method === 'POST' && url.includes('/revise')) { posts.push('revise'); return { ok: true, json: async () => ({}) } as Response }
      if (url.includes('/plan')) return { ok: true, text: async () => '' } as Response
      if (url.includes('/dialog')) return { ok: true, json: async () => [] } as Response
      // /api/mesh/* — capability off by default (see mockFetchForStatus's own comment).
      if (url.includes('/api/mesh/')) return { ok: false, status: 404, json: async () => ({}) } as Response
      return { ok: true, json: async () => [] } as Response
    })

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Alpha'))
    fireEvent.click(screen.getByRole('tab', { name: 'Feed' }))

    // Фокус composer'а на A ставит НАШУ паузу (self-owned) и заносит черновик.
    const composer = screen.getByPlaceholderText(/note to agent/i)
    act(() => composer.focus())
    fireEvent.change(composer, { target: { value: 'draft note' } })
    await waitFor(() => expect(posts).toContain('pause'))

    // Статус отдаёт A на паузе → владение подтверждается (баннер паузы).
    emitStageStatus('s1', 'paused')
    await waitFor(() => expect(screen.getByText(/Stage paused/)).toBeInTheDocument())

    // Blur с НЕпустым черновиком: владение сохраняется, а textarea-фокус снят —
    // единственный источник suppression теперь selfOwnedActive, не useIsEditing.
    act(() => composer.blur())
    expect(document.activeElement).not.toBe(composer)

    // Пока держим owned-паузу — B задаёт вопрос: он ОТКЛАДЫВАЕТСЯ.
    s2Status = 'awaiting_user_input'
    emitStageStatus('s2', 'awaiting_user_input')
    // Ждём именно ПРИМЕНЕНИЯ snapshot с вопросом B, а не маяка Paused самой A — иначе тест ложно-зелёный:
    // условие уже выполнено паузой A ещё до того, как вопрос B вообще пришёл.
    await waitFor(() =>
      expect(document.querySelector('[data-stage-id="s2"][data-status="awaiting_user_input"]')).not.toBeNull(),
    )
    expect(screen.getByRole('tab', { name: 'Feed' })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByPlaceholderText(/note to agent/i)).toHaveValue('draft note')

    // Завершаем op отправкой (continue → revise): владение снимается.
    fireEvent.click(screen.getByRole('button', { name: /send note to agent/i }))
    await waitFor(() => expect(posts).toContain('revise'))

    // Авторитетный статус: A снова running (владения нет), B всё ещё ждёт — отдельное событие (дедуп ленты
    // схлопнул бы повтор того же status s2) → B авто-открывается.
    emitStageStatus('s1', 'running')
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Beta'))
    expect(document.getElementById('dialog-section')).not.toBeNull()
  })

  test('self-owned paused does not flash a false Paused panel after Send before /api/status catches up', async () => {
    // Регрессия: handleSend снимает владение СРАЗУ после revise — до того как /api/status заменит
    // локальный paused на running.
    localStorage.setItem('afm.pauseOnFocus', 'true')
    let s1Status = 'running'
    const posts: string[] = []
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      const method = (init?.method ?? 'GET').toUpperCase()
      if (url.includes('/api/status')) {
        return { ok: true, json: async () => ({ flow_name: 'demo', stages: [stageView('s1', 'Alpha', s1Status)] }) } as Response
      }
      if (method === 'POST' && url.includes('/pause')) { posts.push('pause'); s1Status = 'paused'; return { ok: true, json: async () => ({}) } as Response }
      // continue НАМЕРЕННО не трогает s1Status — статус ещё не догнал (лаг).
      if (method === 'POST' && url.includes('/continue')) { posts.push('continue'); return { ok: true, json: async () => ({}) } as Response }
      if (method === 'POST' && url.includes('/revise')) { posts.push('revise'); return { ok: true, json: async () => ({}) } as Response }
      if (url.includes('/plan')) return { ok: true, text: async () => '' } as Response
      if (url.includes('/dialog')) return { ok: true, json: async () => [] } as Response
      // /api/mesh/* — capability off by default (see mockFetchForStatus's own comment).
      if (url.includes('/api/mesh/')) return { ok: false, status: 404, json: async () => ({}) } as Response
      return { ok: true, json: async () => [] } as Response
    })

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Alpha'))
    fireEvent.click(screen.getByRole('tab', { name: 'Feed' }))

    const composer = screen.getByPlaceholderText(/note to agent/i)
    act(() => composer.focus())
    fireEvent.change(composer, { target: { value: 'draft note' } })
    await waitFor(() => expect(posts).toContain('pause'))
    emitStageStatus('s1', 'paused') // A/paused приходит под нашим владением → consumed.
    await waitFor(() => expect(screen.getByText(/Stage paused/)).toBeInTheDocument())

    // Send: continue → revise, затем clearOp (владение снято) — А ещё paused.
    fireEvent.click(screen.getByRole('button', { name: /send note to agent/i }))
    await waitFor(() => expect(posts).toContain('revise'))

    // Зазор: владение снято, suppression отпущен, A всё ещё paused. Триггерим refresh (тот же
    // paused-статус).
    emitStageStatus('s1', 'paused')
    await act(async () => { await new Promise((r) => setTimeout(r, 20)) })
    expect(screen.getByRole('tab', { name: 'Feed' })).toHaveAttribute('aria-selected', 'true')
  })

  test('Pause checkbox focus is not editing: a question auto-opens immediately', async () => {
    let asking = false
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [
        stageView('s1', 'Alpha', 'running'),
        stageView('s2', 'Beta', asking ? 'awaiting_user_input' : 'running', asking ? QUESTION_FLAGS : {}),
      ],
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Alpha'))
    fireEvent.click(screen.getByRole('tab', { name: 'Feed' }))

    // Фокус на checkbox тумблера паузы — клик сам по себе фокус в jsdom не
    // гарантирует, поэтому .focus() + assert activeElement (denylist → не editing).
    const checkbox = screen.getByRole('checkbox')
    act(() => checkbox.focus())
    expect(document.activeElement).toBe(checkbox)

    // Вопрос приходит — suppression'а нет, авто-открытие сразу.
    asking = true
    emitStageStatus('s2', 'awaiting_user_input')
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Beta'))
    expect(document.getElementById('dialog-section')).not.toBeNull()
  })

  test('Manual beacon open during suppression: episode does not auto-open again after release', async () => {
    let asking = false
    let s3done = false
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [
        stageView('s1', 'Alpha', 'running'),
        stageView('s2', 'Beta', asking ? 'awaiting_user_input' : 'running', asking ? QUESTION_FLAGS : {}),
        stageView('s3', 'Gamma', s3done ? 'done' : 'running'),
      ],
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Alpha'))
    fireEvent.click(screen.getByRole('tab', { name: 'Feed' }))

    const composer = screen.getByPlaceholderText(/note to agent/i)
    act(() => composer.focus())
    fireEvent.change(composer, { target: { value: 'my draft' } })

    asking = true
    emitStageStatus('s2', 'awaiting_user_input')
    const beacon = await waitFor(() => {
      const t = screen.getAllByRole('tab').find((x) => /Question/.test(x.textContent ?? ''))
      expect(t).toBeDefined()
      return t as HTMLElement
    })

    // Открываем маяк вручную (это ПОМЕЧАЕТ эпизод handled). Клик уводит фокус с
    // composer'а — suppression от ввода снимается.
    fireEvent.click(beacon)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Beta'))
    expect(document.getElementById('dialog-section')).not.toBeNull()

    // Возвращаемся в Feed вручную.
    fireEvent.click(screen.getByRole('tab', { name: 'Feed' }))
    await waitFor(() => expect(document.getElementById('dialog-section')).toBeNull())

    // Свежий суппрешн-free sync: эпизод s2 уже handled, поэтому фокус не крадётся — остаёмся на Feed.
    s3done = true
    emitStageStatus('s3', 'done')
    // Ждём s3=done в DOM, доказывая применение refresh без suppression.
    await waitFor(() =>
      expect(document.querySelector('[data-stage-id="s3"][data-status="done"]')).not.toBeNull(),
    )
    expect(screen.getByRole('tab', { name: 'Feed' })).toHaveAttribute('aria-selected', 'true')
    expect(document.getElementById('dialog-section')).toBeNull()
  })

  test('Same-stage question after typing surfaces without a manual blur (reconcile-after-unmount)', async () => {
    // Реальный репорт: одна стадия B; фокус в её composer'е, затем B сама уходит в awaiting_user_input →
    // её composer размонтируется.
    let asking = false
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Bravo', asking ? 'awaiting_user_input' : 'running', asking ? QUESTION_FLAGS : {})],
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Bravo'))
    fireEvent.click(screen.getByRole('tab', { name: 'Feed' }))

    const composer = screen.getByPlaceholderText(/note to agent/i)
    act(() => composer.focus())
    expect(document.activeElement).toBe(composer)
    fireEvent.change(composer, { target: { value: 'half-typed' } })

    // B → awaiting_user_input: composer B размонтируется, .blur() не вызываем.
    asking = true
    emitStageStatus('s1', 'awaiting_user_input')

    // Reconcile-эффект схлопывает editing → вопрос B авто-открывается (не виснет).
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Bravo'))
    expect(document.getElementById('dialog-section')).not.toBeNull()
    expect(screen.getByText('Agent needs your input')).not.toBeNull()
  })

  test('New episode through the real pipeline: t2 under suppression defers, then auto-opens on blur', async () => {
    // Suppression держится на ОТДЕЛЬНОЙ running-стадии A. Проверяет через реальный App→hook пайплайн, что
    // updated_at-параметризация и episode-identity живут в проде.
    const t1 = '2026-09-30T10:00:00.000Z'
    const t2 = '2026-09-30T10:05:00.000Z'
    let bStatus = 'awaiting_user_input'
    let bUpdated = t1
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [
        stageView('sA', 'Alpha', 'running'),
        stageView('sB', 'Bravo', bStatus, bStatus === 'awaiting_user_input' ? { ...QUESTION_FLAGS, updatedAt: bUpdated } : { updatedAt: bUpdated }),
      ],
    }))

    render(<App />)
    // (1) B входит awaiting t1 БЕЗ фокуса → авто-открытие attention на B/t1.
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Bravo'))
    expect(document.getElementById('dialog-section')).not.toBeNull()

    // (2) Выбираем A, открываем Feed, фокусируем её composer (suppression).
    fireEvent.click(document.querySelector('[data-stage-id="sA"] .stage-row') as HTMLElement)
    fireEvent.click(screen.getByRole('tab', { name: 'Feed' }))
    const composer = screen.getByPlaceholderText(/note to agent/i)
    act(() => composer.focus())
    expect(document.activeElement).toBe(composer)

    // (3) B возвращается в running, затем снова awaiting с НОВЫМ эпизодом t2 —
    // на снимке t2 composer A сфокусирован → t2 откладывается, Feed/A цел.
    bStatus = 'running'
    emitStageStatus('sB', 'running')
    await waitFor(() => expect(screen.getAllByRole('tab').some((t) => /Question/.test(t.textContent ?? ''))).toBe(false))

    bStatus = 'awaiting_user_input'
    bUpdated = t2
    emitStageStatus('sB', 'awaiting_user_input')
    await waitFor(() => expect(screen.getAllByRole('tab').some((t) => /Question/.test(t.textContent ?? ''))).toBe(true))
    expect(screen.getByRole('tab', { name: 'Feed' })).toHaveAttribute('aria-selected', 'true')
    expect(document.getElementById('detail-title')).toHaveTextContent('Alpha')

    // (4) Снятие фокуса — новый эпизод t2 авто-открывается.
    act(() => composer.blur())
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Bravo'))
    expect(document.getElementById('dialog-section')).not.toBeNull()
  })

  // SideAgentProvider оборачивает FileBrowserProvider (не наоборот) — проверяем, что оба провайдера
  // смонтированы и видны их кнопки в шапке, когда обе capability включены.
  test('renders both the Files and Side agent header buttons when both capabilities are on', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Propose', 'running')],
      capabilities: { file_browser: true, side_agent: true },
    }))

    render(<App />)

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /open project files/i })).toBeInTheDocument()
    })
    expect(screen.getByRole('button', { name: /open side agent/i })).toBeInTheDocument()
  })

  // Agent Mesh, T7.3 — there is no status.capabilities.mesh field (unlike file_browser/side_agent above):
  // useMeshRoster's own first-fetch probe IS the capability signal the Team tab gates on. These two tests
  // are the end-to-end proof both directions of that probe actually drive the tab.
  test('Agent Mesh T7.3: Team tab appears once the mesh probe succeeds, and opens the Team view', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      if (url.includes('/api/status')) {
        return {
          ok: true,
          json: async () => ({
            flow_name: 'demo',
            stages: [stageView('s1', 'Propose', 'running')],
            accounting: { health: 'ok', has_data: false, show_money: true },
          }),
        } as Response
      }
      if (url.includes('/api/mesh/participants')) {
        return {
          ok: true,
          json: async () => ({ participants: [{ id: 'run:qa', kind: 'run_agent', scope: 'run', display_name: 'QA reviewer', reachable: true }] }),
        } as Response
      }
      if (url.includes('/api/mesh/threads')) return { ok: true, json: async () => ({ threads: [] }) } as Response
      if (url.includes('/api/mesh/board')) return { ok: true, json: async () => ({ entries: [] }) } as Response
      if (url.includes('/plan')) return { ok: true, text: async () => '' } as Response
      if (url.includes('/dialog')) return { ok: true, json: async () => [] } as Response
      return { ok: true, json: async () => [] } as Response
    })

    render(<App />)

    await waitFor(() => expect(screen.getByRole('tab', { name: 'Team' })).toBeInTheDocument())
    fireEvent.click(screen.getByRole('tab', { name: 'Team' }))

    await waitFor(() => expect(screen.getByText('This run')).toBeInTheDocument())
    expect(screen.getByText('QA reviewer')).toBeInTheDocument()
  })

  // Stage 5: the stage Feed's people panel. A click on a person puts their @handle into the Feed composer
  // (Message mode) and addresses them; the Team tab keeps its own roster.
  test('people panel: a click on a person addresses them in the current Feed composer', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      if (url.includes('/api/status')) {
        return {
          ok: true,
          json: async () => ({
            flow_name: 'demo',
            stages: [stageView('s1', 'Propose', 'running'), stageView('s2', 'Review', 'pending')],
            accounting: { health: 'ok', has_data: false, show_money: true },
          }),
        } as Response
      }
      if (url.includes('/api/mesh/participants')) {
        return {
          ok: true,
          json: async () => ({
            participants: [
              { id: 'user', kind: 'user', scope: 'run', reachable: true },
              { id: 'stage:s1', kind: 'stage_agent', scope: 'stage', stage_id: 's1', alias: 's1', display_name: 'Propose', reachable: true, command: 'claude' },
              { id: 'stage:s2', kind: 'stage_agent', scope: 'stage', stage_id: 's2', alias: 's2', display_name: 'Review', reachable: true, command: 'claude' },
              { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA reviewer', reachable: true, command: 'codex', readonly_enforcement: 'os' },
            ],
          }),
        } as Response
      }
      if (url.includes('/api/mesh/threads')) return { ok: true, json: async () => ({ threads: [] }) } as Response
      if (url.includes('/plan')) return { ok: true, text: async () => '' } as Response
      if (url.includes('/dialog')) return { ok: true, json: async () => [] } as Response
      return { ok: true, json: async () => [] } as Response
    })

    render(<App />)

    const people = await screen.findByRole('navigation', { name: 'People' })
    await waitFor(() => expect(within(people).getByText('QA reviewer')).toBeInTheDocument())
    expect(within(people).getByText('Not started — reads it when it starts')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Show people' })).toHaveAttribute('aria-controls', 'stage-people')

    fireEvent.click(within(people).getByText('QA reviewer'))

    const composer = (await screen.findByRole('combobox', { name: /message the mesh/i })) as HTMLTextAreaElement
    expect(composer.value).toBe('@qa ')
    expect(screen.getByRole('tab', { name: 'Message' })).toHaveAttribute('aria-selected', 'true')
    expect(screen.queryByText('Pick someone with @ or press Reply')).toBeNull()
  })

  test('people panel: an agent just added can be picked before the next roster poll', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      if (url.includes('/api/status')) {
        return {
          ok: true,
          json: async () => ({
            flow_name: 'demo',
            stages: [stageView('s1', 'Propose', 'running')],
            capabilities: { collaboration: true },
            accounting: { health: 'ok', has_data: false, show_money: true },
          }),
        } as Response
      }
      if (url.includes('/api/assistant-profiles')) {
        return { ok: true, json: async () => ({ profiles: [{ id: 'qa', title: 'QA role', command: 'claude', available: true }] }) } as Response
      }
      if (url.includes('/api/mesh/attach-and-send')) {
        return { ok: true, json: async () => ({ task_id: 't-new', participant_id: 'run:t-new', thread_id: 'mt-new' }) } as Response
      }
      // The roster poll never learns about the new agent during this test.
      if (url.includes('/api/mesh/participants')) {
        return {
          ok: true,
          json: async () => ({
            participants: [
              { id: 'user', kind: 'user', scope: 'run', reachable: true },
              { id: 'stage:s1', kind: 'stage_agent', scope: 'stage', stage_id: 's1', alias: 's1', display_name: 'Propose', reachable: true },
            ],
          }),
        } as Response
      }
      if (url.includes('/api/mesh/threads')) return { ok: true, json: async () => ({ threads: [] }) } as Response
      if (url.includes('/plan')) return { ok: true, text: async () => '' } as Response
      if (url.includes('/dialog')) return { ok: true, json: async () => [] } as Response
      return { ok: true, json: async () => [] } as Response
    })

    render(<App />)

    const people = await screen.findByRole('navigation', { name: 'People' })
    fireEvent.click(within(people).getByRole('button', { name: 'Add agent' }))
    fireEvent.change(await screen.findByPlaceholderText('What should this run agent do?'), { target: { value: 'review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add run agent' }))

    const added = await within(people).findByText('QA role')
    fireEvent.click(added)

    const composer = (await screen.findByRole('combobox', { name: /message the mesh/i })) as HTMLTextAreaElement
    expect(composer.value).toBe('@run:t-new ')
  })

  test('Agent Mesh T7.3: no Team tab when the mesh probe 404s (capability off)', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stages: [stageView('s1', 'Propose', 'running')],
    }))

    render(<App />)

    await waitFor(() => expect(screen.getByText('demo')).toBeInTheDocument())
    await waitFor(() => expect(screen.queryByRole('tab', { name: 'Team' })).toBeNull())
  })
})
