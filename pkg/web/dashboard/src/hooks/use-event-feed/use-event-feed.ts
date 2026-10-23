import { useEffect, useState } from 'react'
import type { AfmEvent } from '../../types'
import { useRunEpoch } from '../use-run-epoch'

// Подписка на WebSocket /ws с автопереконнектом и keepalive. Heartbeat от сервера в ленту событий НЕ
// попадает (liveness only).
const INITIAL_RECONNECT_DELAY_MS = 1000
const MAX_RECONNECT_DELAY_MS = 10000
// Лента событий ограничена.
const MAX_EVENTS = 10000
const WATCHDOG_INTERVAL_MS = 5000
const WATCHDOG_SILENCE_MS = 75000
// Типы, для которых onmessage дедупит по dedupeKey ПРИ ПРИЁМЕ. agent_action/script_output намеренно
// исключены — легитимные одинаковые повторы не должны схлопываться.
const CONTENT_DEDUPE_ON_INGEST = new Set(['agent_note', 'script_failed'])

// runId нужен, чтобы пережить авто-повтор флоу: при смене рана старый сокет закрывается, лента очищается и
// история нового рана грузится заново (seq у каждого рана свой, поэтому смешивать нельзя).
export function useEventFeed(url: string, runId: string): { events: AfmEvent[]; connected: boolean } {
  const [events, setEvents] = useState<AfmEvent[]>([])
  const [connected, setConnected] = useState(false)
  const runEpoch = useRunEpoch(runId)
  const [feedEpoch, setFeedEpoch] = useState(runEpoch)
  if (feedEpoch !== runEpoch) {
    setFeedEpoch(runEpoch)
    setEvents([])
    setConnected(false)
  }

  useEffect(() => {
    let cancelledFetch = false

    // Тянет /api/events и мёржит с уже накопленными live-событиями.
    function syncHistory() {
      fetch('/api/events')
        .then((r) => (r.ok ? r.json() : []))
        .then((raw: unknown) => {
          if (cancelledFetch || !Array.isArray(raw)) return
          const history = raw.map(toEvent)
          setEvents((prev) => mergeCapped(history, prev, MAX_EVENTS))
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
          // Схлопываем подряд идущие одинаковые смены статуса одной стадии: бэкенд фиксирует переход один
          // раз, но повторы не должны засорять ленту.
          const last = prev[prev.length - 1]
          if (last !== undefined && isSameStatusEvent(last, event)) return prev

          // Дедупим контент только CONTENT_DEDUPE_ON_INGEST; dialog использует именованные поля, а повторы
          // agent_action/script_output сохраняем.
          if (isDialogContentEvent(event) || CONTENT_DEDUPE_ON_INGEST.has(event.type)) {
            const key = feedDedupeKey(event)
            if (prev.some((e) => feedDedupeKey(e) === key)) return prev
          }

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
  }, [url, runEpoch])

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

// dedupeKey — стабильный ключ дедупликации между историей из /api/events и live-событиями WebSocket.
function dedupeKey(e: AfmEvent): string {
  if (e.seq !== undefined) return `seq:${e.seq}|${e.type}`
  return `${e.type}|${e.stageId}|${JSON.stringify(e.payload)}`
}

// isDialogContentEvent — событие принадлежит «семейству диалога»: dialog_question, dialog_answer, а также
// auto_answered ТОЛЬКО когда это настоящий авто-ответ на вопрос.
function isDialogContentEvent(e: AfmEvent): boolean {
  if (e.type === 'dialog_question' || e.type === 'dialog_answer') return true
  if (e.type !== 'auto_answered') return false
  const p = e.payload
  return typeof p === 'object' && p !== null && (p as { dialog?: unknown }).dialog === true
}

// dialogContentKey зеркалит серверный ключ из именованных полей; текст различает повторно использованные
// phase/id, порядок JSON-ключей не важен.
function dialogContentKey(e: AfmEvent): string {
  const p = e.payload
  const obj = typeof p === 'object' && p !== null ? (p as Record<string, unknown>) : {}
  const field = (v: unknown): string => (typeof v === 'string' ? v : v === undefined || v === null ? '' : String(v))
  return `${e.type}|${e.stageId}|${field(obj.phase)}|${field(obj.id)}|${field(obj.title)}|${field(obj.question_title)}|${field(obj.answer)}`
}

// feedDedupeKey — единый дедуп-ключ для ленты, используемый И ингест-путём, И mergeCapped: семейство
// диалога → контент-ключ из именованных полей, всё остальное → прежний dedupeKey.
function feedDedupeKey(e: AfmEvent): string {
  return isDialogContentEvent(e) ? dialogContentKey(e) : dedupeKey(e)
}

// mergeCapped сливает историю из /api/events с уже накопленными live-событиями. Раньше было наоборот —
// [...deduped, ...live], где deduped = history без live-ключей.
export function mergeCapped(history: AfmEvent[], live: AfmEvent[], cap: number): AfmEvent[] {
  const historyKeys = new Set(history.map(feedDedupeKey))
  const liveOnly = live.filter((e) => !historyKeys.has(feedDedupeKey(e)))
  return [...history, ...liveOnly].slice(-cap)
}

function statusOf(event: AfmEvent): string {
  const data = event.payload
  if (typeof data === 'string') return data
  if (isRecord(data) && typeof data.status === 'string') return data.status
  return ''
}

function toEvent(raw: unknown): AfmEvent {
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

// dedupeKey/toEvent переиспользуются per-stage лентой при разборе своего собственного фетча — единая точка
// приведения типа и дедуп-ключа для истории и live-потока, не дублируется.
export { dedupeKey, toEvent }
