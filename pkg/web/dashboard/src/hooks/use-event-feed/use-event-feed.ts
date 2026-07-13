import { useEffect, useState } from 'react'
import type { AfmEvent } from '../../types'

// Подписка на WebSocket /ws с автопереконнектом и keepalive.
// Реконнект: экспоненциальный backoff (1с → 10с) по onclose (как в app.js).
// Watchdog: нет сообщений (событий ИЛИ heartbeat) > 75с → принудительный close
// → срабатывает onclose → реконнект. Так клиент ловит «мёртвый сервер» быстрее
// TCP-таймаута. Heartbeat от сервера в ленту событий НЕ попадает (liveness only).
const INITIAL_RECONNECT_DELAY_MS = 1000
const MAX_RECONNECT_DELAY_MS = 10000
// Лента событий ограничена (как $feedContent в app.js обрезается до 200 записей).
const MAX_EVENTS = 200
const WATCHDOG_INTERVAL_MS = 5000
const WATCHDOG_SILENCE_MS = 75000

export function useEventFeed(url: string): { events: AfmEvent[]; connected: boolean } {
  const [events, setEvents] = useState<AfmEvent[]>([])
  const [connected, setConnected] = useState(false)

  useEffect(() => {
    let socket: WebSocket | null = null
    let reconnectTimer: ReturnType<typeof setTimeout> | undefined
    let watchdogTimer: ReturnType<typeof setInterval> | undefined
    let reconnectDelay = INITIAL_RECONNECT_DELAY_MS
    let lastMessageAt = Date.now()
    let cancelled = false

    function connect() {
      socket = new WebSocket(url)
      lastMessageAt = Date.now()

      socket.onopen = () => {
        if (cancelled) return
        setConnected(true)
        reconnectDelay = INITIAL_RECONNECT_DELAY_MS
      }

      socket.onclose = () => {
        if (cancelled) return
        setConnected(false)
        reconnectTimer = setTimeout(connect, reconnectDelay)
        reconnectDelay = Math.min(reconnectDelay * 2, MAX_RECONNECT_DELAY_MS)
      }

      socket.onmessage = (message) => {
        if (cancelled) return
        lastMessageAt = Date.now()

        let raw: unknown
        try {
          raw = JSON.parse(message.data as string)
        } catch {
          return
        }
        // Heartbeat обновляет lastMessageAt (keepalive), но в ленту не попадает.
        if (isHeartbeat(raw)) return

        // Единственная точка приведения типа для данных входящего сообщения.
        const event = toEvent(raw)
        setEvents((prev) => [...prev, event].slice(-MAX_EVENTS))
      }
    }

    // Watchdog: раз в WATCHDOG_INTERVAL_MS проверяет тишину (по времени, не по
    // числу событий — иначе простой без событий ложно триггерил бы реконнект).
    watchdogTimer = setInterval(() => {
      if (cancelled) return
      if (Date.now() - lastMessageAt > WATCHDOG_SILENCE_MS) {
        socket?.close() // → onclose → backoff-реконнект
      }
    }, WATCHDOG_INTERVAL_MS)

    connect()

    return () => {
      cancelled = true
      if (reconnectTimer !== undefined) clearTimeout(reconnectTimer)
      if (watchdogTimer !== undefined) clearInterval(watchdogTimer)
      socket?.close()
    }
  }, [url])

  return { events, connected }
}

function isHeartbeat(raw: unknown): boolean {
  return typeof raw === 'object' && raw !== null && (raw as { type?: unknown }).type === 'heartbeat'
}

function toEvent(raw: unknown): AfmEvent {
  const obj = isRecord(raw) ? raw : {}

  return {
    type: typeof obj.type === 'string' ? obj.type : '',
    payload: obj.data,
    stageId: typeof obj.stage_id === 'string' ? obj.stage_id : '',
    timestamp: new Date().toISOString(),
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object'
}
