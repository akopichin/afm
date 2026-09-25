import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
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
  isScript?: boolean
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
  const isScript = overrides.isScript ?? false

  return {
    id,
    name,
    status,
    updated_at: '',
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
      // The real accounting-enabled backend (the default) always sends an
      // `accounting` object; the Cost tab / Est. cost tile are gated on its
      // presence. Default it in when a payload omits it, so tests model the
      // enabled backend; a test exercising the disabled switch sets
      // `accounting` explicitly (e.g. null → supported:false, no Cost chrome).
      // show_money:true keeps the money-visible default these tests were written
      // against; the money-hidden default is covered in RunMetrics/CostPanel/
      // StagesList unit tests.
      const payload = statusPayload()
      if (payload !== null && typeof payload === 'object' && !('accounting' in payload)) {
        ;(payload as Record<string, unknown>).accounting = { health: 'ok', has_data: false, show_money: true }
      }
      return { ok: true, json: async () => payload } as Response
    }

    if (url.includes('/plan')) return { ok: true, text: async () => '' } as Response
    if (url.includes('/dialog')) return { ok: true, json: async () => [] } as Response

    return { ok: true, json: async () => [] } as Response
  })
}

// Переключает воркспейс на вкладку деталей выбранной стадии (Feed — дефолтная
// вкладка; план/диалог живут на второй, контекстной вкладке). Нужна там, где
// тест проверяет наличие/отсутствие самой панели плана/диалога.
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

  test('CRITICAL: WS refresh survives past the 1000-event feed cap (Finding #2)', async () => {
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

    // Забиваем ленту ВЫШЕ кэпа (MAX_EVENTS=1000, use-event-feed) незначимыми
    // agent_action — сами по себе refresh они не вызывают, но насыщают длину
    // массива до кэпа.
    act(() => {
      for (let i = 0; i < 1005; i += 1) {
        ws?.onmessage?.({ data: JSON.stringify({ type: 'agent_action', data: { n: i }, stage_id: 's1' }) })
      }
    })
    expect(statusCalls).toBe(1)

    // Значимое событие ПОСЛЕ насыщения кэпа обязано снова триггерить refresh.
    // При старой length-based проверке (N === N) этот вызов молча
    // проглатывался — WS-канал обновления был мёртв до конца сессии.
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
    // B ждёт аппрув. Воркспейс авто-открывает его (вкладка Approval). Пользователь
    // уходит в Feed — ожидание не должно «потеряться»: оно остаётся ОТДЕЛЬНОЙ
    // glow-вкладкой-маяком Approval (не подменяя history, R3 #7). Клик по маяку
    // возвращает к плану ожидающей стадии.
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
    // Одна стадия ждёт (её же ожидание = маяк). В feed-виде средний блёклый
    // detail-таб (имя стадии) вёл бы в ТО ЖЕ ожидание, что и маяк — дубликат.
    // Скрываем detail-таб, красивый glow-маяк остаётся.
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
      expect(labels.some((l) => l === 'Gamma')).toBe(false) // блёклый дубль скрыт
      // Feed + постоянная Cost-вкладка (Task 11) + маяк + постоянная Full feed
      // (Task 6) — никакого блёклого дубля-detail сверх этих четырёх.
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

    // The waiting stage is initially auto-opened as Attention. Return to Feed
    // first so the rail click is tested in the global workspace, like the live
    // dashboard scenario.
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

  test('another stage awaits in Feed: a completed stage does not open a detail tab', async () => {
    // Ждёт s2, а выбрана в рейле завершённая s1. Маяк указывает на чужое
    // ожидание (s2), а клик по завершённой стадии оставляет Feed активным.
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
      expect(labels.some((l) => l === 'Alpha')).toBe(false)
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

    // Кликаем по s1 — история завершённой стадии не открывается автоматически.
    fireEvent.click(document.querySelector('[data-stage-id="s1"] .stage-row') as HTMLElement)

    const labels = screen.getAllByRole('tab').map((t) => t.textContent ?? '')
    expect(screen.getByRole('tab', { name: 'Feed' })).toHaveAttribute('aria-selected', 'true')
    expect(labels.some((l) => /Done stage/.test(l))).toBe(false)
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
      expect(screen.queryByRole('group', { name: /history view/i })).not.toBeInTheDocument()
      expect(document.getElementById('dialog-section')).toBeNull()
      expect(document.getElementById('plan-section')).toBeNull()
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
    // Стадия B входит в awaiting_approval, пока выбрана и работает стадия A.
    // Воркспейс обязан сам открыть ожидание B (его нельзя «потерять»), а не
    // остаться на A. До подключения workspace-редьюсера auto-open считался
    // только из выбранной стадии, поэтому ожидание на невыбранной B не всплывало.
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
    // interactive-стадия в awaiting_user_input: сервер отдаёт и show_plan, и
    // show_dialog=true. Раньше рендерились ОБЕ панели (обе flex:1), и вопрос
    // получал ~полэкрана, а над ним висел исторический план. Теперь показываем
    // ровно одну панель — диалог — на всю высоту, без плана рядом.
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
    // Root cause of the reported bug: the old check fired only in the exact
    // tick the selected stage transitioned to done, searching forward for an
    // already-active stage right then. Script stages (Stage.IsScript()) can
    // run so briefly that a whole extra stage finishes between two polls —
    // if NO stage was active yet in the snapshot where s1 was first seen
    // done, the old code gave up permanently instead of trying again on the
    // next poll once s3 actually became active.
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
    // Воркспейс авто-открывает стадию, ждущую действия: 'Downstream' (failed)
    // — это attention-элемент и всплывает сам (Finding #1), а не 'Upstream'
    // (running). Здесь это лишь гейт «приложение отрендерилось»; суть теста —
    // что idle не тикает при idle_since=null.
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
    // Feed (per-stage) больше не фильтрует общий поток через тумблер — она
    // читает уже отфильтрованную стадией ленту (useStageEvents). Full feed —
    // отдельная постоянная вкладка, читающая весь поток событий флоу.
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
    // После смены набора стадий выбранная s1 исчезает, а активных/failed стадий
    // нет → selectedStageId устаревает, workspaceStage=null → FeedWorkspace
    // получает stageId=null и показывает нейтральный empty hint (нет тумблера,
    // который раньше скрывался в этом случае — он просто больше не существует).
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
    // Клик по постоянной вкладке Cost должен убрать имя/статус ранее выбранной
    // стадии (WorkspaceHeader, id detail-title) — иначе создаётся ложное
    // впечатление, что отчёт отфильтрован по этой стадии.
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
    // accounting.enabled: false / AFM_ACCOUNTING=0 → /api/status omits the
    // accounting object → supported:false. The permanent Cost tab must NOT
    // render (and neither the Est. cost tile — see RunMetrics test), otherwise
    // the display switch wouldn't actually hide the cost UI. `accounting: null`
    // opts this payload out of the mock's enabled-by-default injection.
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
    // Живой ревью-баг: Cost был второй вкладкой сразу после Feed, поэтому
    // detail-таб выбранной стадии и attention-маяк оказывались ПОСЛЕ Cost —
    // Cost «застревала» посередине списка. Cost должна всегда идти предпоследней
    // (Full feed, Task 6, теперь всегда самая последняя).
    // s1 done с планом — клик по ней оставляет Feed; s2 ждёт аппрув — отдельный
    // маяк Approval. Порядок: Feed, Approval (маяк), Cost, Full feed.
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
      expect(labels).not.toContain('Greet')
      expect(labels.some((l) => /Approval/.test(l))).toBe(true)
    })

    // Клик по Cost по-прежнему активирует вкладку отчёта, даже будучи предпоследней.
    fireEvent.click(screen.getByRole('tab', { name: 'Cost' }))
    await waitFor(() => expect(screen.getByRole('tab', { name: 'Cost' })).toHaveAttribute('aria-selected', 'true'))
  })

  test('FIX 1: Cost view never shows a redundant stage-detail tab alongside the attention beacon', async () => {
    // User-reported bug: Cost is a GLOBAL report, not stage-scoped, but the
    // tab list still emitted a detail-tab named after the selected stage
    // AND the attention beacon for that same stage — two tabs pointing at
    // the same thing. s1 is awaiting approval (its own attention); it's also
    // the selected/workspace stage while we're on Cost. Expected order:
    // Feed | Approval·1 | Cost | Full feed — no third "Propose" detail tab.
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
    // Round-5 #6 спеки: after the popover closes and Cost opens, focus must
    // land on the destination (the Cost tab button), not linger on the now
    // out-of-context "…" disclosure button.
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

    // Раскрываем несколько строк ВНУТРИ таблицы, а не любую кнопку с именем
    // "Stage N" — в рейле слева тоже есть кнопки-строки с такими же именами
    // (клик по ним переключил бы workspace обратно на attention/history и
    // увёл бы нас с вкладки Cost, см. handleSelectStage).
    const expandButtons = within(table).getAllByRole('button', { name: /Stage \d+/ })
    fireEvent.click(expandButtons[0] as HTMLElement)
    fireEvent.click(expandButtons[5] as HTMLElement)
    fireEvent.click(expandButtons[10] as HTMLElement)

    const totalRow = within(table).getByText('Total').closest('tr')
    expect(totalRow).not.toBeNull()
    expect(totalRow).toHaveTextContent('$20.00')
    expect(document.querySelector('.cost-panel-scroll')).not.toBeNull()
  })

  // Task 6: клик по dialog_question/dialog_answer элементу ленты (FeedWorkspace's
  // onOpenDialog) должен выбрать стадию вопроса и открыть её диалог — live-вопрос
  // (attention), если стадия сейчас awaiting_user_input, иначе read-only историю.
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

    // Возвращаемся в Feed вручную — сигнатура авто-открытия для s2 уже
    // потрачена (rule 9: ровно один раз на прибытие), так что редьюсер не
    // перебросит нас назад сам.
    fireEvent.click(screen.getByRole('tab', { name: 'Feed' }))
    await waitFor(() => expect(document.getElementById('dialog-section')).toBeNull())

    // Клик по Feed вернул выбор в attention-эпизод s2 (см. useEffect,
    // синхронизирующий selectedStageId с активным attention-элементом) —
    // Feed выбранной стадии (useStageEvents) уже показывает события s2.
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
})
