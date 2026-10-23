import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { mergeCapped, useEventFeed } from './use-event-feed'
import type { AfmEvent } from '../../types'

// Минимальный fake WebSocket для тестирования подписки и backoff-реконнекта.
class FakeWebSocket {
  static instances: FakeWebSocket[] = []

  url: string
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  closed = false

  static last(): FakeWebSocket {
    const instance = FakeWebSocket.instances[FakeWebSocket.instances.length - 1]
    if (instance === undefined) {
      throw new Error('no FakeWebSocket instance')
    }

    return instance
  }

  constructor(url: string) {
    this.url = url
    FakeWebSocket.instances.push(this)
  }

  // close() идемпотентен и эмитит onclose — как нативный WebSocket
  // (close() в браузере асинхронно вызывает событие close).
  close() {
    if (this.closed) return
    this.closed = true
    this.onclose?.()
  }

  emitOpen() {
    this.onopen?.()
  }

  emitClose() {
    this.onclose?.()
  }

  emitMessage(data: unknown) {
    this.onmessage?.({ data: JSON.stringify(data) })
  }
}

describe('useEventFeed', () => {
  beforeEach(() => {
    FakeWebSocket.instances = []
    vi.stubGlobal('WebSocket', FakeWebSocket)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  test('connects, reports connected and accumulates events', () => {
    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))

    expect(FakeWebSocket.instances).toHaveLength(1)
    expect(result.current.connected).toBe(false)

    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    expect(result.current.connected).toBe(true)

    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'approved', data: 'ok', stage_id: 's1' })
    })
    expect(result.current.events).toHaveLength(1)
    expect(result.current.events[0]?.type).toBe('approved')
    expect(result.current.events[0]?.stageId).toBe('s1')
  })

  test('collapses consecutive duplicate status changes for the same stage', () => {
    // Регресс #2: лента засорялась потоком одинаковых «TASK-REVIEW → ready».
    // Бэкенд фиксирует переход один раз; подряд идущие повторы схлопываются.
    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))

    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'stage_status_changed', data: 'ready', stage_id: 'task-review' })
      FakeWebSocket.last().emitMessage({ type: 'stage_status_changed', data: 'ready', stage_id: 'task-review' })
      FakeWebSocket.last().emitMessage({ type: 'stage_status_changed', data: 'ready', stage_id: 'task-review' })
    })

    expect(result.current.events).toHaveLength(1)

    // Смена статуса той же стадии (ready → running) — уже не дубликат, добавляется.
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'stage_status_changed', data: 'running', stage_id: 'task-review' })
    })
    expect(result.current.events).toHaveLength(2)

    // Тот же статус, но другая стадия — тоже не дубликат.
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'stage_status_changed', data: 'running', stage_id: 'plan' })
    })
    expect(result.current.events).toHaveLength(3)
  })

  test('reconnects with exponential backoff', () => {
    vi.useFakeTimers()
    renderHook(() => useEventFeed('/ws', 'run-a'))

    act(() => {
      FakeWebSocket.last().emitClose()
    })

    // первый реконнект через 1000мс
    vi.advanceTimersByTime(1000)
    expect(FakeWebSocket.instances).toHaveLength(2)

    act(() => {
      FakeWebSocket.last().emitClose()
    })

    // второй реконнект через 2000мс (удвоение)
    vi.advanceTimersByTime(1999)
    expect(FakeWebSocket.instances).toHaveLength(2)
    vi.advanceTimersByTime(1)
    expect(FakeWebSocket.instances).toHaveLength(3)
  })

  test('resets backoff to 1000ms after a successful open', () => {
    vi.useFakeTimers()
    renderHook(() => useEventFeed('/ws', 'run-a'))

    act(() => {
      FakeWebSocket.last().emitClose()
    })
    vi.advanceTimersByTime(1000) // реконнект #2

    act(() => {
      FakeWebSocket.last().emitOpen() // сброс задержки к 1000мс
      FakeWebSocket.last().emitClose()
    })
    vi.advanceTimersByTime(1000) // реконнект по сброшенной задержке, а не 2000мс

    expect(FakeWebSocket.instances).toHaveLength(3)
  })

  test('closes the socket and stops reconnecting on unmount', () => {
    vi.useFakeTimers()
    const { unmount } = renderHook(() => useEventFeed('/ws', 'run-a'))

    unmount()

    expect(FakeWebSocket.last().closed).toBe(true)

    act(() => {
      FakeWebSocket.last().emitClose()
    })

    const countAfterUnmount = FakeWebSocket.instances.length
    vi.advanceTimersByTime(30_000)
    expect(FakeWebSocket.instances).toHaveLength(countAfterUnmount)
  })

  test('filters heartbeat out of the event feed', () => {
    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))

    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'heartbeat' })
      FakeWebSocket.last().emitMessage({ type: 'stage_status_changed', stage_id: 's1' })
    })

    expect(result.current.events).toHaveLength(1)
    expect(result.current.events[0]?.type).toBe('stage_status_changed')
  })

  test('watchdog closes the connection after prolonged silence', () => {
    vi.useFakeTimers()
    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))

    act(() => {
      FakeWebSocket.last().emitOpen()
    })

    // WATCHDOG_SILENCE_MS = 75с; продвигаем таймеры за порог тишины.
    act(() => {
      vi.advanceTimersByTime(80_000)
    })

    expect(FakeWebSocket.last().closed).toBe(true)
    expect(result.current.connected).toBe(false)
  })

  test('ignores invalid JSON messages without throwing or changing events', () => {
    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))

    act(() => {
      FakeWebSocket.last().emitOpen()
    })

    expect(() => {
      act(() => {
        FakeWebSocket.last().onmessage?.({ data: '{not valid json' })
      })
    }).not.toThrow()

    expect(result.current.events).toHaveLength(0)
  })

  test('caps the global feed at 10000 events', () => {
    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))

    act(() => {
      FakeWebSocket.last().emitOpen()
    })

    act(() => {
      for (let i = 0; i < 10200; i += 1) {
        FakeWebSocket.last().emitMessage({ type: 'agent_action', data: i, stage_id: `s${i}` })
      }
    })

    expect(result.current.events).toHaveLength(10000)
    expect(result.current.events[0]?.stageId).toBe('s200')
    expect(result.current.events[9999]?.stageId).toBe('s10199')
  })

  // mergeCapped — генерализация mergeHistory с явным cap-параметром (Task 3
  // переиспользует её для per-stage ленты с другим, меньшим капом).
  test('mergeCapped keeps history as base and appends only live-only, capped', () => {
    const ev = (id: string): AfmEvent => ({
      type: 'agent_action',
      payload: id,
      stageId: id,
      timestamp: '2026-09-23T10:00:00.000Z',
      seq: undefined,
    })
    const k = (e: AfmEvent): string => e.stageId

    const history = [ev('a'), ev('b')]
    const live = [ev('b'), ev('c')] // 'b' — дубликат по dedupeKey

    expect(mergeCapped(history, live, 10).map(k)).toEqual(['a', 'b', 'c'])
    expect(mergeCapped(history, live, 2).map(k)).toEqual(['b', 'c']) // last-N
  })

  test('backoff climbs to and stays at the 10000ms ceiling across repeated reconnects', () => {
    vi.useFakeTimers()
    renderHook(() => useEventFeed('/ws', 'run-a'))

    // Задержки без успешного open между ними удваиваются: 1000 -> 2000 -> 4000 -> 8000 -> 10000 (потолок).
    const delays = [1000, 2000, 4000, 8000, 10000]

    for (const delay of delays) {
      act(() => {
        FakeWebSocket.last().emitClose()
      })
      const countBefore = FakeWebSocket.instances.length
      vi.advanceTimersByTime(delay - 1)
      expect(FakeWebSocket.instances).toHaveLength(countBefore)
      vi.advanceTimersByTime(1)
      expect(FakeWebSocket.instances).toHaveLength(countBefore + 1)
    }

    // Ещё один цикл на потолке — задержка остаётся 10000ms, не растёт дальше.
    act(() => {
      FakeWebSocket.last().emitClose()
    })
    const countBefore = FakeWebSocket.instances.length
    vi.advanceTimersByTime(9999)
    expect(FakeWebSocket.instances).toHaveLength(countBefore)
    vi.advanceTimersByTime(1)
    expect(FakeWebSocket.instances).toHaveLength(countBefore + 1)
  })

  test('activity within the silence window resets the watchdog and keeps the socket open', () => {
    vi.useFakeTimers()
    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))

    act(() => {
      FakeWebSocket.last().emitOpen()
    })

    // 60с тишины, затем сообщение сбрасывает lastMessageAt.
    act(() => {
      vi.advanceTimersByTime(60_000)
    })
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'agent_action', stage_id: 's1' })
    })

    // Ещё 60с (суммарно 120с от начала, но только 60с с последней активности) — меньше порога 75с.
    act(() => {
      vi.advanceTimersByTime(60_000)
    })

    expect(FakeWebSocket.last().closed).toBe(false)
    expect(result.current.connected).toBe(true)
  })

  test('seeds history from /api/events on mount and merges live events without duplicating by seq', async () => {
    const historyPayload = [
      { type: 'stage_status_changed', stage_id: 's1', data: 'running', timestamp: '2026-07-27T10:00:00.000Z', seq: 1 },
      { type: 'stage_status_changed', stage_id: 's1', data: 'done', timestamp: '2026-07-27T10:05:00.000Z', seq: 2 },
    ]
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        json: () => Promise.resolve(historyPayload),
      }),
    )

    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))

    // Живое сообщение с тем же seq=2 приходит по WS ДО того, как REST-фетч резолвится.
    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'stage_status_changed', stage_id: 's1', data: 'done', seq: 2 })
    })

    await waitFor(() => {
      expect(result.current.events.filter((e) => e.seq === 2)).toHaveLength(1)
    })
    // История (running, seq=1) должна присутствовать — не потеряна, т.к. это
    // другое логическое событие (другой seq), а не дубликат.
    expect(result.current.events.some((e) => e.seq === 1 && e.payload === 'running')).toBe(true)
    // Timestamp из истории — реальный, не "сейчас".
    const historic = result.current.events.find((e) => e.seq === 1)
    expect(historic?.timestamp).toBe('2026-07-27T10:00:00.000Z')
  })

  test('dedupes events without seq (e.g. agent_action) by content as a fallback', async () => {
    const historyPayload = [
      { type: 'agent_action', stage_id: 's1', data: { tool: 'Bash', detail: 'pwd' }, timestamp: '2026-07-27T10:00:00.000Z' },
    ]
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        json: () => Promise.resolve(historyPayload),
      }),
    )

    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))

    // agent_action не производится от FSM-transition — ни история, ни live
    // не несут seq для него, дедуп падает на ключ по содержимому.
    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'agent_action', stage_id: 's1', data: { tool: 'Bash', detail: 'pwd' } })
    })

    await waitFor(() => {
      expect(result.current.events.filter((e) => e.type === 'agent_action')).toHaveLength(1)
    })
  })

  // History остаётся хронологической базой; только live-only события дописываются в конец.
  test('merge keeps chronological order when a live event races ahead of /api/events (Finding #4)', async () => {
    // Полная упорядоченная история; seq=3 — «случилось, пока шёл фетч».
    const history = [
      { type: 'stage_status_changed', stage_id: 's1', data: 'running', timestamp: '2026-07-27T10:00:00.000Z', seq: 1 },
      { type: 'stage_status_changed', stage_id: 's1', data: 'awaiting_approval', timestamp: '2026-07-27T10:01:00.000Z', seq: 2 },
      { type: 'stage_status_changed', stage_id: 's1', data: 'done', timestamp: '2026-07-27T10:02:00.000Z', seq: 3 },
    ]
    let resolveFetch: (value: unknown) => void = () => {}
    const fetchPromise = new Promise((res) => {
      resolveFetch = res
    })
    vi.stubGlobal(
      'fetch',
      vi.fn().mockReturnValue(fetchPromise),
    )

    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))
    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    // WS доставляет seq=2 ДО резолва /api/events → live = [seq2].
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'stage_status_changed', stage_id: 's1', data: 'awaiting_approval', seq: 2 })
    })
    // Теперь резолвим полную историю → срабатывает mergeHistory(history, [seq2]).
    await act(async () => {
      resolveFetch({ ok: true, json: () => Promise.resolve(history) })
      await fetchPromise
    })

    await waitFor(() => {
      expect(result.current.events).toHaveLength(3)
    })
    // Хронологический порядок сохранён: seq3 (done) — В КОНЦЕ, а не в начале.
    expect(result.current.events.map((e) => e.seq)).toEqual([1, 2, 3])
  })

  // T4b: dialog_question/dialog_answer публикуются И live, И в notices.jsonl — без seq, поэтому дедуп идёт
  // по контенту.
  test('dialog_question arriving live first, then the same question in history — one row', async () => {
    const questionPayload = { id: 'q1', question: 'Proceed?', options: ['yes', 'no'] }
    let resolveFetch: (value: unknown) => void = () => {}
    const fetchPromise = new Promise((res) => {
      resolveFetch = res
    })
    vi.stubGlobal('fetch', vi.fn().mockReturnValue(fetchPromise))

    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))
    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'dialog_question', stage_id: 's1', data: questionPayload })
    })

    await act(async () => {
      resolveFetch({
        ok: true,
        json: () =>
          Promise.resolve([
            { type: 'dialog_question', stage_id: 's1', data: questionPayload, timestamp: '2026-09-15T10:00:00.000Z' },
          ]),
      })
      await fetchPromise
    })

    await waitFor(() => {
      expect(result.current.events.filter((e) => e.type === 'dialog_question')).toHaveLength(1)
    })
  })

  // Обратный порядок — тот самый баг: onmessage слепо аппендил live-событие,
  // даже если та же самая (по контенту) запись уже пришла из истории раньше.
  test('dialog_question already in history, then the same question arrives live — dedup keeps one row', async () => {
    const questionPayload = { id: 'q1', question: 'Proceed?', options: ['yes', 'no'] }
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        json: () =>
          Promise.resolve([
            { type: 'dialog_question', stage_id: 's1', data: questionPayload, timestamp: '2026-09-15T10:00:00.000Z' },
          ]),
      }),
    )

    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))

    await waitFor(() => {
      expect(result.current.events.filter((e) => e.type === 'dialog_question')).toHaveLength(1)
    })

    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    // Та же question, что уже вошла из истории, теперь долетает по WS (была
    // в полёте на момент фетча) — без фикса onmessage добавил бы вторую строку.
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'dialog_question', stage_id: 's1', data: questionPayload })
    })

    expect(result.current.events.filter((e) => e.type === 'dialog_question')).toHaveLength(1)
  })

  // Trap #1 из брифа: одна FSM-transition может породить ДВА события с общим seq. Дедуп по одному только
  // seq схлопнул бы их в одно — ключ должен учитывать ещё и type.
  test('stage_status_changed and retry_scheduled sharing the same seq both survive merge with history', async () => {
    let resolveFetch: (value: unknown) => void = () => {}
    const fetchPromise = new Promise((res) => {
      resolveFetch = res
    })
    vi.stubGlobal('fetch', vi.fn().mockReturnValue(fetchPromise))

    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))
    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    // Live-версия одной половины пары (stage_status_changed) долетает до
    // резолва /api/events.
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'stage_status_changed', stage_id: 's1', data: 'retrying', seq: 5 })
    })

    // История содержит СОСЕДНЕЕ семантическое событие с ТЕМ ЖЕ seq — это
    // другая, самостоятельная запись, а не дубликат.
    await act(async () => {
      resolveFetch({
        ok: true,
        json: () =>
          Promise.resolve([
            {
              type: 'retry_scheduled',
              stage_id: 's1',
              data: { attempt: 1 },
              timestamp: '2026-09-15T10:00:00.000Z',
              seq: 5,
            },
          ]),
      })
      await fetchPromise
    })

    await waitFor(() => {
      expect(result.current.events).toHaveLength(2)
    })
    expect(result.current.events.map((e) => e.type).sort()).toEqual(['retry_scheduled', 'stage_status_changed'])
  })

  // Trap #2 из брифа: контент-дедуп на входе не должен схлопывать легитимные
  // повторы agent_action/script_output — только dialog_question/dialog_answer.
  test('identical seq-less agent_action live messages are not content-deduped', () => {
    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))
    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'agent_action', stage_id: 's1', data: { tool: 'Bash', detail: 'pwd' } })
      FakeWebSocket.last().emitMessage({ type: 'agent_action', stage_id: 's1', data: { tool: 'Bash', detail: 'pwd' } })
    })

    expect(result.current.events).toHaveLength(2)
  })

  // agent_note публикуется И live, И в notices.jsonl без seq — как dialog_answer. Уникальный id в payload
  // делает контент-ключ надёжным: тот же note (live+replay) схлопывается в одну строку.
  test('the same agent_note arriving live and via history collapses to one row', () => {
    const notePayload = { id: 7, text: 'fix it' }
    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))
    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'agent_note', stage_id: 's1', data: notePayload })
      // Тот же note повторно (гонка live/replay) — не должен удвоиться.
      FakeWebSocket.last().emitMessage({ type: 'agent_note', stage_id: 's1', data: notePayload })
    })

    expect(result.current.events.filter((e) => e.type === 'agent_note')).toHaveLength(1)
  })

  // ...но две РАЗНЫЕ заметки с одинаковым текстом (разный id) — две строки:
  // именно для этого id и нужен, иначе контент-дедуп схлопнул бы их.
  test('two agent_notes with identical text but different id are not collapsed', () => {
    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))
    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'agent_note', stage_id: 's1', data: { id: 1, text: 'fix it' } })
      FakeWebSocket.last().emitMessage({ type: 'agent_note', stage_id: 's1', data: { id: 2, text: 'fix it' } })
    })

    expect(result.current.events.filter((e) => e.type === 'agent_note')).toHaveLength(2)
  })

  // Один script_failed occurrence имеет одинаковый data.seq в live/history и схлопывается при merge.
  test('a script_failed present in both history and live resolves to one row via mergeCapped', () => {
    const scriptFailed = (): AfmEvent => ({
      type: 'script_failed',
      payload: { error: 'exit status 1', stderr_tail: 'boom', seq: '1' },
      stageId: 's1',
      timestamp: '2026-09-23T10:00:00.000Z',
      seq: undefined,
    })

    const history = [scriptFailed()]
    const live = [scriptFailed()] // тот же контент (тот же seq) — дубликат по dedupeKey

    const merged = mergeCapped(history, live, 10)
    expect(merged.filter((e) => e.type === 'script_failed')).toHaveLength(1)
  })

  // Проверяем ingest-дедуп script_failed: история уже загружена, затем приходит live с тем же data.seq.
  test('script_failed already in history, then the same script_failed arrives live — dedup keeps one row', async () => {
    const scriptFailedPayload = { error: 'exit status 1', stderr_tail: 'boom', seq: '1' }
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        json: () =>
          Promise.resolve([
            { type: 'script_failed', stage_id: 's1', data: scriptFailedPayload, timestamp: '2026-09-23T10:00:00.000Z' },
          ]),
      }),
    )

    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))

    await waitFor(() => {
      expect(result.current.events.filter((e) => e.type === 'script_failed')).toHaveLength(1)
    })

    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    // Тот же script_failed, что уже вошёл из истории, теперь долетает по WS
    // (был в полёте на момент фетча) — без фикса onmessage добавил бы вторую строку.
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'script_failed', stage_id: 's1', data: scriptFailedPayload })
    })

    expect(result.current.events.filter((e) => e.type === 'script_failed')).toHaveLength(1)
  })

  // Regression for the dedup-collapses-distinct-failures bug: two genuinely DIFFERENT script_failed
  // occurrences for the same stage must NOT collapse into one row.
  test('two script_failed for the same stage with different seq are both kept (distinct occurrences)', () => {
    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))

    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    act(() => {
      FakeWebSocket.last().emitMessage({
        type: 'script_failed',
        stage_id: 's1',
        data: { error: 'exit status 1', stderr_tail: 'boom', seq: '1' },
      })
      FakeWebSocket.last().emitMessage({
        type: 'script_failed',
        stage_id: 's1',
        data: { error: 'exit status 1', stderr_tail: 'boom', seq: '2' },
      })
    })

    expect(result.current.events.filter((e) => e.type === 'script_failed')).toHaveLength(2)
  })

  // Навигируемый авто-ответ публикуется И live, И в notices.jsonl без seq — как dialog_answer.
  test('the same navigable auto_answered arriving via WS then history collapses to one row', async () => {
    const answerPayload = { phase: 'implementation', id: 'q7', answer: 'Option B', from_options: true, question_title: 'Which?', dialog: true }
    let resolveFetch: (value: unknown) => void = () => {}
    const fetchPromise = new Promise((res) => {
      resolveFetch = res
    })
    vi.stubGlobal('fetch', vi.fn().mockReturnValue(fetchPromise))

    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))
    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'auto_answered', stage_id: 's1', data: answerPayload })
    })

    await act(async () => {
      resolveFetch({
        ok: true,
        json: () =>
          Promise.resolve([
            { type: 'auto_answered', stage_id: 's1', data: answerPayload, timestamp: '2026-09-15T10:00:00.000Z' },
          ]),
      })
      await fetchPromise
    })

    await waitFor(() => {
      expect(result.current.events.filter((e) => e.type === 'auto_answered')).toHaveLength(1)
    })
  })

  // Обратный порядок — ингест-путь onmessage: навигируемый авто-ответ уже вошёл из истории, теперь та же
  // запись долетает по WS.
  test('navigable auto_answered already in history, then the same one arrives live — dedup keeps one row', async () => {
    const answerPayload = { phase: 'implementation', id: 'q7', answer: 'Option B', from_options: true, question_title: 'Which?', dialog: true }
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        json: () =>
          Promise.resolve([
            { type: 'auto_answered', stage_id: 's1', data: answerPayload, timestamp: '2026-09-15T10:00:00.000Z' },
          ]),
      }),
    )

    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))

    await waitFor(() => {
      expect(result.current.events.filter((e) => e.type === 'auto_answered')).toHaveLength(1)
    })

    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'auto_answered', stage_id: 's1', data: answerPayload })
    })

    expect(result.current.events.filter((e) => e.type === 'auto_answered')).toHaveLength(1)
  })

  // Дедуп навигируемого авто-ответа НЕ должен зависеть от порядка ключей в payload — ключ строится из
  // именованных полей type|stageId|phase|id|answer.
  test('navigable auto_answered dedupes regardless of payload key order (WS vs history)', async () => {
    const wsPayload = { dialog: true, id: 'q7', phase: 'implementation', answer: 'Option B', from_options: true }
    const historyPayload = { phase: 'implementation', id: 'q7', answer: 'Option B', dialog: true, from_options: true }
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        json: () =>
          Promise.resolve([
            { type: 'auto_answered', stage_id: 's1', data: historyPayload, timestamp: '2026-09-15T10:00:00.000Z' },
          ]),
      }),
    )

    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))

    await waitFor(() => {
      expect(result.current.events.filter((e) => e.type === 'auto_answered')).toHaveLength(1)
    })

    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'auto_answered', stage_id: 's1', data: wsPayload })
    })

    expect(result.current.events.filter((e) => e.type === 'auto_answered')).toHaveLength(1)
  })

  // Два РАЗНЫХ авто-ответа на один и тот же вопрос (тот же id/phase, разный answer) — это две
  // самостоятельные записи и обе должны выжить: ключ включает answer.
  test('two navigable auto_answered with same id/phase but different answer are both kept', () => {
    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))
    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'auto_answered', stage_id: 's1', data: { phase: 'planning', id: 'q1', answer: 'yes', dialog: true } })
      FakeWebSocket.last().emitMessage({ type: 'auto_answered', stage_id: 's1', data: { phase: 'planning', id: 'q1', answer: 'no', dialog: true } })
    })

    expect(result.current.events.filter((e) => e.type === 'auto_answered')).toHaveLength(2)
  })

  // Legacy/repair авто-ответы (без dialog-флага) НЕ проходят dialog-дедуп: два
  // разных прогресс-сообщения ремонта с одинаковым id обязаны оба остаться.
  test('two different repair auto_answered (no dialog flag) are both kept', () => {
    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))
    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'auto_answered', stage_id: 's1', data: { phase: 'planning', id: 'q1', answer: '⚙️ step 1' } })
      FakeWebSocket.last().emitMessage({ type: 'auto_answered', stage_id: 's1', data: { phase: 'planning', id: 'q1', answer: '⚙️ step 2' } })
    })

    expect(result.current.events.filter((e) => e.type === 'auto_answered')).toHaveLength(2)
  })

  // Тот же id/answer, но в РАЗНЫХ стадиях/фазах — разные записи (ключ включает
  // stageId и phase), обе выживают.
  test('navigable auto_answered with same id/answer but different stage or phase are both kept', () => {
    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))
    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'auto_answered', stage_id: 's1', data: { phase: 'planning', id: 'q1', answer: 'yes', dialog: true } })
      FakeWebSocket.last().emitMessage({ type: 'auto_answered', stage_id: 's2', data: { phase: 'planning', id: 'q1', answer: 'yes', dialog: true } })
      FakeWebSocket.last().emitMessage({ type: 'auto_answered', stage_id: 's1', data: { phase: 'implementation', id: 'q1', answer: 'yes', dialog: true } })
    })

    expect(result.current.events.filter((e) => e.type === 'auto_answered')).toHaveLength(3)
  })

  // Review HIGH #3: агент может переиспользовать отвеченный id для ДРУГОГО вопроса — dialog_question с тем
  // же id/phase, но разным title, это две записи.
  test('two dialog_question with same id/phase but different title are both kept', () => {
    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))
    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'dialog_question', stage_id: 's1', data: { phase: 'planning', id: 'q1', title: 'Use Postgres?' } })
      FakeWebSocket.last().emitMessage({ type: 'dialog_question', stage_id: 's1', data: { phase: 'planning', id: 'q1', title: 'Enable cache?' } })
    })

    expect(result.current.events.filter((e) => e.type === 'dialog_question')).toHaveLength(2)
  })

  // Review HIGH #2/#3: два навигируемых авто-ответа на переиспользованный id с ОДИНАКОВЫМ answer, но для
  // РАЗНЫХ вопросов — обе записи.
  test('two navigable auto_answered with same id/answer but different question_title are both kept', () => {
    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))
    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    act(() => {
      FakeWebSocket.last().emitMessage({ type: 'auto_answered', stage_id: 's1', data: { phase: 'planning', id: 'q1', answer: 'Yes', question_title: 'Use Postgres?', dialog: true } })
      FakeWebSocket.last().emitMessage({ type: 'auto_answered', stage_id: 's1', data: { phase: 'planning', id: 'q1', answer: 'Yes', question_title: 'Enable cache?', dialog: true } })
    })

    expect(result.current.events.filter((e) => e.type === 'auto_answered')).toHaveLength(2)
  })

  test('re-fetches and merges /api/events after a reconnect completes (not just on initial mount)', () => {
    vi.useFakeTimers()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve([]) })
    vi.stubGlobal('fetch', fetchMock)

    renderHook(() => useEventFeed('/ws', 'run-a'))
    expect(fetchMock).toHaveBeenCalledTimes(1)

    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    // Первый onopen — это НЕ реконнект, лишнего фетча быть не должно.
    expect(fetchMock).toHaveBeenCalledTimes(1)

    act(() => {
      FakeWebSocket.last().emitClose()
    })
    act(() => {
      vi.advanceTimersByTime(1000) // реконнект через INITIAL_RECONNECT_DELAY_MS
    })
    expect(FakeWebSocket.instances).toHaveLength(2)

    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    // Второй onopen — реконнект после close — досасывает историю ещё раз.
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  // T11.2 — discussion_message/assistant_task_status events (the helper-agents feature) never carry a
  // stable `seq` (AGENTS.md: "discussion/assistant-task bus.Events never set this field") — unlike
  // stage-transition events, this is the ONE family of events whose reconnect-dedup falls back to
  // feedDedupeKey's content-based key, not seq. These two tests pin that the fallback is still correct for
  // exactly that family, not just for the seq-carrying case every other test in this file already covers.
  test('TestHistory_WebSocketLossCompensatedByCursorReplay', async () => {
    // The WS connection is down (closed) for a stretch during which the orchestrator durably appended a
    // discussion message — the client never saw it live. A reconnect's /api/events replay (durable,
    // seq-anchored on the SERVER side even though this particular event's own JSON has no `seq`) must still
    // deliver it.
    vi.useFakeTimers()
    const missed = { type: 'discussion_message', stage_id: 'impl', data: { author: 'reviewer', text: 'please add a timeout' }, timestamp: '2026-10-16T10:00:00.000Z' }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve([]) }) // initial mount: nothing yet
      .mockResolvedValue({ ok: true, json: () => Promise.resolve([missed]) }) // reconnect: the missed message is now durable

    vi.stubGlobal('fetch', fetchMock)
    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))
    await act(async () => { await vi.advanceTimersByTimeAsync(0) }) // flush the initial-mount fetch

    act(() => {
      FakeWebSocket.last().emitOpen() // first connect — not a reconnect, no extra fetch
    })
    expect(result.current.events.filter((e) => e.type === 'discussion_message')).toHaveLength(0)

    // WS loss, never delivered live.
    act(() => {
      FakeWebSocket.last().emitClose()
    })
    await act(async () => { await vi.advanceTimersByTimeAsync(1000) }) // reconnect timer fires
    act(() => {
      FakeWebSocket.last().emitOpen() // reconnect → syncHistory() re-fetches /api/events
    })
    await act(async () => { await vi.advanceTimersByTimeAsync(0) }) // flush the reconnect fetch

    expect(result.current.events.filter((e) => e.type === 'discussion_message')).toHaveLength(1)
    expect(result.current.events.find((e) => e.type === 'discussion_message')?.payload).toEqual(missed.data)
  })

  test('TestHistory_NoDuplicatesAfterReconnect', async () => {
    // The SAME discussion message was already delivered live over the WS BEFORE the drop. A reconnect's full
    // /api/events replay re-sends it (it is still durably in the log) — the no-seq content-based dedupe must
    // collapse it back to one row, not double it.
    vi.useFakeTimers()
    const msg = { type: 'discussion_message', stage_id: 'impl', data: { author: 'reviewer', text: 'please add a timeout' } }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve([]) }) // initial mount: not durable yet
      .mockResolvedValue({ ok: true, json: () => Promise.resolve([{ ...msg, timestamp: '2026-10-16T10:00:00.000Z' }]) }) // reconnect: now durable
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    act(() => {
      FakeWebSocket.last().emitMessage(msg) // delivered live first
    })
    expect(result.current.events.filter((e) => e.type === 'discussion_message')).toHaveLength(1)

    act(() => {
      FakeWebSocket.last().emitClose()
    })
    await act(async () => { await vi.advanceTimersByTimeAsync(1000) })
    act(() => {
      FakeWebSocket.last().emitOpen() // reconnect re-fetches the SAME message from history
    })
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })

    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(result.current.events.filter((e) => e.type === 'discussion_message')).toHaveLength(1)
  })

  // Agent Mesh (T7.2): mesh_* notices (pkg/orchestrator/bus.go's EventMesh*) never carry a stable `seq`
  // either — the SAME content-based fallback this file already proves for discussion_message above must
  // dedupe them too, with no mesh-specific code in use-event-feed.ts (dev-plan.md T7.2's explicit
  // instruction to extend this exact test pattern to a mesh event).
  test('TestHistory_NoDuplicatesAfterReconnect_MeshEvent', async () => {
    vi.useFakeTimers()
    const msg = { type: 'mesh_message', stage_id: '', data: { message_id: 'm1', thread_id: 't1', sender_id: 'qa', kind: 'message' } }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve([]) }) // initial mount: not durable yet
      .mockResolvedValue({ ok: true, json: () => Promise.resolve([{ ...msg, timestamp: '2026-10-18T10:00:00.000Z' }]) }) // reconnect: now durable
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useEventFeed('/ws', 'run-a'))
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    act(() => {
      FakeWebSocket.last().emitMessage(msg) // delivered live first
    })
    expect(result.current.events.filter((e) => e.type === 'mesh_message')).toHaveLength(1)

    act(() => {
      FakeWebSocket.last().emitClose()
    })
    await act(async () => { await vi.advanceTimersByTimeAsync(1000) })
    act(() => {
      FakeWebSocket.last().emitOpen() // reconnect re-fetches the SAME mesh notice from history
    })
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })

    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(result.current.events.filter((e) => e.type === 'mesh_message')).toHaveLength(1)
  })
  describe('run change', () => {
    const history = (stageId: string, seq: number) => ({ type: 'agent_action', stage_id: stageId, seq, timestamp: '2026-01-01T00:00:00.000Z' })

    test('a new run drops the old events and subscription, and shows only its own feed even with the same stage and seq', async () => {
      const histories: Record<string, unknown[]> = { 'run-a': [history('s1', 1)], 'run-b': [history('s1', 1)] }
      let served = 'run-a'
      vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => histories[served] })))

      const { result, rerender } = renderHook(({ runId }) => useEventFeed('/ws', runId), { initialProps: { runId: 'run-a' } })
      await waitFor(() => expect(result.current.events).toHaveLength(1))
      const first = FakeWebSocket.last()
      act(() => first.emitOpen())
      expect(result.current.connected).toBe(true)

      served = 'run-b'
      histories['run-b'] = [{ ...history('s1', 1), type: 'approved' }]
      rerender({ runId: 'run-b' })

      expect(first.closed).toBe(true)
      expect(FakeWebSocket.instances).toHaveLength(2)
      expect(result.current.events).toEqual([]) // never a frame of run A's feed under run B
      expect(result.current.connected).toBe(false)
      await waitFor(() => expect(result.current.events).toHaveLength(1))
      expect(result.current.events[0]?.type).toBe('approved')

      // The old socket stays closed: a late frame from it never reaches run B's feed.
      act(() => first.emitMessage({ type: 'agent_action', data: 'late A', stage_id: 's1', seq: 2 }))
      expect(result.current.events).toHaveLength(1)
    })

    test('a late history response of the old run is ignored', async () => {
      let resolveA: (v: unknown) => void = () => {}
      const fetchMock = vi.fn()
        .mockReturnValueOnce(new Promise((res) => { resolveA = res }))
        .mockResolvedValue({ ok: true, json: async () => [history('s1', 7)] })
      vi.stubGlobal('fetch', fetchMock)

      const { result, rerender } = renderHook(({ runId }) => useEventFeed('/ws', runId), { initialProps: { runId: 'run-a' } })
      rerender({ runId: 'run-b' })
      await waitFor(() => expect(result.current.events).toHaveLength(1))

      await act(async () => {
        resolveA({ ok: true, json: async () => [history('s1', 1), history('s1', 2)] })
        await Promise.resolve()
      })
      expect(result.current.events.map((e) => e.seq)).toEqual([7])
    })

    test("the first run id ('' → run) keeps the events and the connection", async () => {
      vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => [] })))
      const { result, rerender } = renderHook(({ runId }) => useEventFeed('/ws', runId), { initialProps: { runId: '' } })
      act(() => FakeWebSocket.last().emitOpen())
      act(() => FakeWebSocket.last().emitMessage({ type: 'approved', data: 'ok', stage_id: 's1', seq: 1 }))
      expect(result.current.events).toHaveLength(1)

      rerender({ runId: 'run-a' })

      expect(result.current.events).toHaveLength(1)
      expect(result.current.connected).toBe(true)
      expect(FakeWebSocket.instances).toHaveLength(1)
    })
  })
})
