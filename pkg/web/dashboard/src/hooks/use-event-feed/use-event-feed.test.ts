import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { useEventFeed } from './use-event-feed'

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
    const { result } = renderHook(() => useEventFeed('/ws'))

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
    const { result } = renderHook(() => useEventFeed('/ws'))

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
    renderHook(() => useEventFeed('/ws'))

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
    renderHook(() => useEventFeed('/ws'))

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
    const { unmount } = renderHook(() => useEventFeed('/ws'))

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
    const { result } = renderHook(() => useEventFeed('/ws'))

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
    const { result } = renderHook(() => useEventFeed('/ws'))

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
    const { result } = renderHook(() => useEventFeed('/ws'))

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

  test('caps the event feed at 200 entries, keeping the most recent', () => {
    const { result } = renderHook(() => useEventFeed('/ws'))

    act(() => {
      FakeWebSocket.last().emitOpen()
    })

    act(() => {
      for (let i = 0; i < 205; i += 1) {
        FakeWebSocket.last().emitMessage({ type: 'agent_action', data: i, stage_id: `s${i}` })
      }
    })

    expect(result.current.events).toHaveLength(200)
    expect(result.current.events[0]?.stageId).toBe('s5')
    expect(result.current.events[199]?.stageId).toBe('s204')
  })

  test('backoff climbs to and stays at the 10000ms ceiling across repeated reconnects', () => {
    vi.useFakeTimers()
    renderHook(() => useEventFeed('/ws'))

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
    const { result } = renderHook(() => useEventFeed('/ws'))

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

    const { result } = renderHook(() => useEventFeed('/ws'))

    // Живое сообщение с тем же seq=2 (та же transition, что и последняя
    // запись истории) приходит по WS ДО того, как REST-фетч резолвится
    // (гонка между открытием WS и /api/events). Бэкенд теперь прикладывает
    // реальный seq и к live-событиям (triggerWithSeq в orchestrator.go), не
    // только к реплею истории — после merge не должно быть дубля.
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

    const { result } = renderHook(() => useEventFeed('/ws'))

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

  test('re-fetches and merges /api/events after a reconnect completes (not just on initial mount)', () => {
    vi.useFakeTimers()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve([]) })
    vi.stubGlobal('fetch', fetchMock)

    renderHook(() => useEventFeed('/ws'))
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
})
