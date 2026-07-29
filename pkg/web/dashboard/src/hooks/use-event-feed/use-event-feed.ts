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
    let cancelledFetch = false

    // Тянет /api/events и мёржит с уже накопленными live-событиями.
    // Вызывается один раз на монтировании (первичная история) и повторно на
    // каждом реконнекте после первого успешного open (см. hasConnectedBefore
    // ниже) — иначе транзишены, случившиеся, пока сокет был разорван, тихо
    // теряются: /ws не реплеит пропущенные сообщения, а без ресинка счётчики
    // Idle/Backoff (useStatusDuration) могли бы навсегда зависнуть «открытыми».
    function syncHistory() {
      fetch('/api/events')
        .then((r) => (r.ok ? r.json() : []))
        .then((raw: unknown) => {
          if (cancelledFetch || !Array.isArray(raw)) return
          const history = raw.map(toEvent)
          setEvents((prev) => mergeHistory(history, prev))
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
        setEvents((prev) => {
          // Схлопываем подряд идущие одинаковые смены статуса одной стадии
          // (напр. поток «TASK-REVIEW → ready»): бэкенд фиксирует переход один
          // раз, но повторы (реконнект/дребезг) не должны засорять ленту.
          const last = prev[prev.length - 1]
          if (last !== undefined && isSameStatusEvent(last, event)) return prev
          return [...prev, event].slice(-MAX_EVENTS)
        })
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

// Два события — это один и тот же переход статуса стадии (та же стадия, тот же
// целевой статус). Используется для схлопывания подряд идущих дубликатов в ленте.
function isSameStatusEvent(a: AfmEvent, b: AfmEvent): boolean {
  return (
    a.type === 'stage_status_changed' &&
    b.type === 'stage_status_changed' &&
    a.stageId === b.stageId &&
    statusOf(a) === statusOf(b)
  )
}

// dedupeKey — стабильный ключ дедупликации между историей из /api/events и
// live-событиями WebSocket. Приоритет — реальный seq FSM-transition: бэкенд
// теперь прикладывает его и к live-событиям (triggerWithSeq в orchestrator.go),
// а не только к реплею истории, так что seq — надёжный ключ для событий,
// производных от transition (stage_status_changed/ask_user/user_answered/
// retry_scheduled/retry_exhausted). Для типов без transition (agent_action,
// agent_completed, context_warning, supervisor_decision) seq не приходит ни
// оттуда, ни отсюда — падаем на ключ по содержимому (type+stageId+payload).
function dedupeKey(e: AfmEvent): string {
  if (e.seq !== undefined) return `seq:${e.seq}`
  return `${e.type}|${e.stageId}|${JSON.stringify(e.payload)}`
}

// mergeHistory сливает историю из /api/events (history) с уже накопленными
// live-событиями (live), дедуплицируя по dedupeKey: если событие с тем же
// ключом уже пришло по WS (могло случиться в гонке между открытием сокета и
// резолвом REST-фетча), историческая запись-дубликат отбрасывается.
function mergeHistory(history: AfmEvent[], live: AfmEvent[]): AfmEvent[] {
  const liveKeys = new Set(live.map(dedupeKey))
  const deduped = history.filter((e) => !liveKeys.has(dedupeKey(e)))
  return [...deduped, ...live].slice(-MAX_EVENTS)
}

function statusOf(event: AfmEvent): string {
  const data = event.payload
  if (typeof data === 'string') return data
  if (isRecord(data) && typeof data.status === 'string') return data.status
  return ''
}

function toEvent(raw: unknown): AfmEvent {
  const obj = isRecord(raw) ? raw : {}

  // timestamp — из payload, если сервер его прислал (реплей истории из
  // /api/events, Task 4); live WS-сообщения его не несут — падаем на время
  // приёма, как и раньше.
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
