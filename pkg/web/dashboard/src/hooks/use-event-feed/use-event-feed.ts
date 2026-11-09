import { useEffect, useRef, useState } from 'react'
import { SIGNIFICANT_EVENT_TYPES, type AfmEvent } from '../../types'
import { parseAssistantActivity, type AssistantActivityEntry } from '../../types/assistant-activity'
import { useRunEpoch } from '../use-run-epoch'
import { EventBuffer, NO_ARRIVALS, type ArrivalCounts } from './event-buffer'

// Подписка на WebSocket /ws с автопереконнектом и keepalive. Heartbeat от сервера в ленту событий НЕ
// попадает (liveness only).
const INITIAL_RECONNECT_DELAY_MS = 1000
const MAX_RECONNECT_DELAY_MS = 10000
// Входящие события копятся и публикуются не чаще раза за это окно: число рендеров ограничено частотой flush,
// а не потоком событий. Значимые события публикуются сразу.
export const FEED_FLUSH_MS = 80
// Живой хвост активности помощников — отдельное кольцо: объёмный поток не должен вытеснять события стадий и
// не запускает обновление статуса. Полноту истории обеспечивает опрос /activity, а не сокет.
const MAX_LIVE_ACTIVITY = 2000
const ACTIVITY_EVENT_TYPE = 'assistant_activity'
const WATCHDOG_INTERVAL_MS = 5000
const WATCHDOG_SILENCE_MS = 75000

// runId нужен, чтобы пережить авто-повтор флоу: при смене рана старый сокет закрывается, лента очищается и
// история нового рана грузится заново (seq у каждого рана свой, поэтому смешивать нельзя).
// significantVersion растёт на каждой публикации, где было значимое событие: по нему App обновляет состояние
// флоу, даже если значимое событие оказалось не последним в пачке.
export function useEventFeed(
  url: string,
  runId: string,
): { events: AfmEvent[]; arrivals: ArrivalCounts; connected: boolean; activity: AssistantActivityEntry[]; significantVersion: number } {
  const [events, setEvents] = useState<AfmEvent[]>([])
  // Принятые live-события нарастающим итогом, публикуются вместе с events: по ним считают непросмотренное.
  const [arrivals, setArrivals] = useState(NO_ARRIVALS)
  const [activity, setActivity] = useState<AssistantActivityEntry[]>([])
  const [connected, setConnected] = useState(false)
  const [significantVersion, setSignificantVersion] = useState(0)
  const runEpoch = useRunEpoch(runId)
  const [feedEpoch, setFeedEpoch] = useState(runEpoch)
  // Окно живёт между переподключениями одного рана (смена url) и пересоздаётся при смене рана.
  const bufferRef = useRef<{ epoch: number; buffer: EventBuffer } | null>(null)
  if (feedEpoch !== runEpoch) {
    setFeedEpoch(runEpoch)
    setEvents([])
    setArrivals(NO_ARRIVALS)
    setActivity([])
    setConnected(false)
  }

  useEffect(() => {
    if (bufferRef.current?.epoch !== runEpoch) bufferRef.current = { epoch: runEpoch, buffer: new EventBuffer() }
    const buffer = bufferRef.current.buffer
    let activityQueue: AssistantActivityEntry[] = []
    let hasSignificantQueued = false
    let flushTimer: ReturnType<typeof setTimeout> | undefined
    let cancelledFetch = false

    function stopFlushTimer() {
      if (flushTimer === undefined) return
      clearTimeout(flushTimer)
      flushTimer = undefined
    }

    function scheduleFlush() {
      flushTimer ??= setTimeout(flush, FEED_FLUSH_MS)
    }

    function flushActivity() {
      if (activityQueue.length === 0) return
      const batch = activityQueue
      activityQueue = []
      setActivity((prev) => prev.concat(batch).slice(-MAX_LIVE_ACTIVITY))
    }

    // Публикует очередь: один setEvents и один setActivity на flush.
    function flush() {
      stopFlushTimer()

      const next = buffer.flush()
      if (next !== null) {
        setEvents(next)
        setArrivals(buffer.arrivals())
      }

      flushActivity()

      if (hasSignificantQueued) {
        hasSignificantQueued = false
        setSignificantVersion((v) => v + 1)
      }
    }

    // Тянет /api/events и мёржит с уже накопленными live-событиями.
    function syncHistory() {
      fetch('/api/events')
        .then((r) => (r.ok ? r.json() : []))
        .then((raw: unknown) => {
          if (cancelledFetch || !Array.isArray(raw)) return
          // Очередь событий входит в слияние, поэтому flush-таймер не нужен; очередь активности публикуем здесь же.
          stopFlushTimer()
          const merged = buffer.merge(raw.map(toEvent))
          setEvents(merged.events)
          setArrivals(buffer.arrivals())
          flushActivity()
          if (merged.hasNewSignificant || hasSignificantQueued) {
            hasSignificantQueued = false
            setSignificantVersion((v) => v + 1)
          }
        })
        .catch(() => {
          // /api/events недоступен (старая сборка сервера, сетевая ошибка) —
          // деградируем к чистому live-потоку, как было до этой правки.
        })
    }

    syncHistory()

    let socket: WebSocket | null = null
    let reconnectTimer: ReturnType<typeof setTimeout> | undefined
    let watchdogTimer: ReturnType<typeof setInterval> | undefined
    let reconnectDelay = INITIAL_RECONNECT_DELAY_MS
    let lastMessageAt = Date.now()
    let cancelled = false
    let hasConnectedBefore = false

    function connect() {
      socket = new WebSocket(url)
      lastMessageAt = Date.now()

      socket.onopen = () => {
        if (cancelled) return
        setConnected(true)
        reconnectDelay = INITIAL_RECONNECT_DELAY_MS

        if (hasConnectedBefore) syncHistory()
        hasConnectedBefore = true
      }

      socket.onclose = () => {
        if (cancelled) return
        flush()
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

        // Активность помощника не событие ленты: мимо окна событий и дедупа, в своё ограниченное кольцо.
        if (isActivityMessage(raw)) {
          const entry = parseAssistantActivity(raw.data)
          if (entry !== null) {
            activityQueue.push(entry)
            scheduleFlush()
          }
          return
        }

        // Единственная точка приведения типа для данных входящего сообщения.
        const event = toEvent(raw)
        if (!buffer.push(event)) return

        if (SIGNIFICANT_EVENT_TYPES.has(event.type)) {
          hasSignificantQueued = true
          flush()
        } else {
          scheduleFlush()
        }
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
      cancelledFetch = true
      cancelled = true
      stopFlushTimer()
      buffer.discardPending()
      if (reconnectTimer !== undefined) clearTimeout(reconnectTimer)
      if (watchdogTimer !== undefined) clearInterval(watchdogTimer)
      socket?.close()
    }
  }, [url, runEpoch])

  return { events, arrivals, connected, activity, significantVersion }
}

function isHeartbeat(raw: unknown): boolean {
  return typeof raw === 'object' && raw !== null && (raw as { type?: unknown }).type === 'heartbeat'
}

function isActivityMessage(raw: unknown): raw is { type: string; data?: unknown } {
  return isRecord(raw) && raw.type === ACTIVITY_EVENT_TYPE
}

// Переиспользуется per-stage лентой при разборе своего фетча — единая точка приведения типа.
export function toEvent(raw: unknown): AfmEvent {
  const obj = isRecord(raw) ? raw : {}

  // Берём timestamp из payload истории; для live WS используем время приёма.
  const timestamp = typeof obj.timestamp === 'string' ? obj.timestamp : new Date().toISOString()
  const seq = typeof obj.seq === 'number' ? obj.seq : undefined

  return {
    type: typeof obj.type === 'string' ? obj.type : '',
    payload: obj.data,
    stageId: typeof obj.stage_id === 'string' ? obj.stage_id : '',
    timestamp,
    seq,
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object'
}
