import { useCallback, useEffect, useRef, useState } from 'react'
import type { Stage, StageStatus } from '../../types'
import { STAGE_STATUSES } from '../../types'

// Периодический опрос состояния флоу. Соответствует loadState в текущем app.js,
// но в React-форме: поллинг по таймеру + возможность немедленного обновления через
// refresh() (WS-события — канал обновления состояния, см. корневую композицию).
const POLL_INTERVAL_MS = 3000

export type FlowStatus = {
  flowName: string
  stages: Stage[]
  startedAt: string
  // Описание флоу (из корня flow.yaml) — опциональное поле GET /api/status для
  // подзаголовка в шапке (см. FlowHeader). Бэкенд пока его не отдаёт, поле
  // читается защитно (undefined, если отсутствует), без нового API-вызова —
  // как только бэкенд начнёт присылать description, подзаголовок появится сам.
  description?: string
  // idle/backoff — накопленное на бэкенде время (пережившее restart) плюс
  // необязательный анкер ТЕКУЩЕГО открытого периода/эпизодов, см.
  // useIdleMs/useBackoffMs. idleSince — null, если флоу не простаивает
  // прямо сейчас; backoffOpenSince — по одному значению на каждую стадию,
  // сейчас находящуюся в retrying (может быть пустым).
  idleAccumulatedMs: number
  idleSince: string | null
  backoffAccumulatedMs: number
  backoffOpenSince: string[]
}

const EMPTY_STATUS: FlowStatus = {
  flowName: '',
  stages: [],
  startedAt: '',
  idleAccumulatedMs: 0,
  idleSince: null,
  backoffAccumulatedMs: 0,
  backoffOpenSince: [],
}

// Сырой ответ GET /api/status приводится к FlowStatus в normalizeStatus: stages —
// уже упорядоченный массив StageView (см. pkg/server/stageview.go), маппинг 1:1
// через toStage.

export function useStatus(): FlowStatus & { refresh: () => void } {
  const [status, setStatus] = useState<FlowStatus>(EMPTY_STATUS)
  const cancelledRef = useRef(false)
  // Поллинг и WS-триггерный refresh() issue независимые fetch('/api/status') —
  // несколько запросов могут быть в полёте одновременно (напр. burst значимых
  // событий), а сетевые ответы приходят не обязательно в порядке отправки.
  // latestRequestId — счётчик поколений: каждый load() запоминает свой номер
  // ДО await и применяет ответ, только если он всё ещё самый свежий issued
  // запрос — иначе более старый (но позже резолвившийся) ответ откатил бы
  // состояние назад (реальный баг: пропущенный auto-advance стадии в App).
  const latestRequestId = useRef(0)

  const load = useCallback(async () => {
    const requestId = ++latestRequestId.current

    let response: Response
    try {
      response = await fetch('/api/status')
    } catch {
      return
    }

    if (!response.ok) return

    // Единственная точка приведения типа для внешнего JSON.
    const data: unknown = await response.json()
    if (cancelledRef.current) return
    if (requestId !== latestRequestId.current) return

    setStatus(normalizeStatus(data))
  }, [])

  const refresh = useCallback(() => {
    void load()
  }, [load])

  useEffect(() => {
    cancelledRef.current = false
    void load()

    const timer = setInterval(() => {
      void load()
    }, POLL_INTERVAL_MS)

    return () => {
      cancelledRef.current = true
      clearInterval(timer)
    }
  }, [load])

  // Фоновая/свёрнутая вкладка троттлит setInterval сильнее, чем доставку
  // WS-сообщений (см. use-event-feed.ts) — поэтому долгий флоу может
  // завершиться, пока опрос выше не тикнул ни разу. Возврат вкладки в фокус —
  // сигнал незамедлительно подтянуть актуальный статус, а не ждать следующего
  // (возможно, отложенного браузером) тика.
  useEffect(() => {
    const onVisible = () => {
      if (document.visibilityState === 'visible') void load()
    }
    // focus не нуждается в проверке visibilityState — сам факт фокуса окна
    // уже означает, что вкладка активна.
    const onFocus = () => void load()
    document.addEventListener('visibilitychange', onVisible)
    window.addEventListener('focus', onFocus)
    return () => {
      document.removeEventListener('visibilitychange', onVisible)
      window.removeEventListener('focus', onFocus)
    }
  }, [load])

  return { ...status, refresh }
}

export function normalizeStatus(raw: unknown): FlowStatus {
  const obj = isRecord(raw) ? raw : {}

  const flowName = typeof obj.flow_name === 'string' ? obj.flow_name : ''
  const startedAt = typeof obj.started_at === 'string' ? obj.started_at : ''
  const description = typeof obj.description === 'string' ? obj.description : undefined

  const stages: Stage[] = Array.isArray(obj.stages) ? obj.stages.map(toStage).filter((s): s is Stage => s !== null) : []

  const idleAccumulatedMs = typeof obj.idle_accumulated_ms === 'number' ? obj.idle_accumulated_ms : 0
  const idleSince = typeof obj.idle_since === 'string' ? obj.idle_since : null
  const backoffAccumulatedMs = typeof obj.backoff_accumulated_ms === 'number' ? obj.backoff_accumulated_ms : 0
  const backoffOpenSince = Array.isArray(obj.backoff_open_since)
    ? obj.backoff_open_since.filter((v): v is string => typeof v === 'string')
    : []

  return { flowName, stages, startedAt, description, idleAccumulatedMs, idleSince, backoffAccumulatedMs, backoffOpenSince }
}

function toStage(raw: unknown): Stage | null {
  const obj = isRecord(raw) ? raw : null
  if (obj === null || typeof obj.id !== 'string') return null

  const status: StageStatus = isStageStatus(obj.status) ? obj.status : 'pending'
  const updatedAt = typeof obj.updated_at === 'string' ? obj.updated_at : ''
  const name = typeof obj.name === 'string' ? obj.name : ''

  return {
    id: obj.id,
    name,
    status,
    updatedAt,
    interactive: obj.interactive === true,
    autonomous: obj.autonomous === true,
    autoApprove: obj.auto_approve === true,
    hasDialog: obj.has_dialog === true,
    showPlan: obj.show_plan === true,
    showDialog: obj.show_dialog === true,
    isScript: obj.is_script === true,
    pausedFrom: isStageStatus(obj.paused_from) ? obj.paused_from : '',
    preNote: typeof obj.pre_note === 'string' ? obj.pre_note : '',
    buttons: Array.isArray(obj.buttons) ? obj.buttons.filter((x): x is string => typeof x === 'string') : [],
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object'
}

function isStageStatus(value: unknown): value is StageStatus {
  return typeof value === 'string' && (STAGE_STATUSES as readonly string[]).includes(value)
}
