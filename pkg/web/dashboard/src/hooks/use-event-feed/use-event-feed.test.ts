import { act, renderHook } from '@testing-library/react'
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
})
