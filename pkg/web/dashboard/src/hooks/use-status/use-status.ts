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
}

const EMPTY_STATUS: FlowStatus = { flowName: '', stages: [], startedAt: '' }

// Сырой ответ GET /api/status приводится к FlowStatus в normalizeStatus: stages —
// объект по id, порядок — в stage_order, имена — в stage_names (как в текущем app.js).

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

  return { ...status, refresh }
}

export function normalizeStatus(raw: unknown): FlowStatus {
  const obj = isRecord(raw) ? raw : {}

  const flowName = typeof obj.flow_name === 'string' ? obj.flow_name : ''
  const startedAt = typeof obj.started_at === 'string' ? obj.started_at : ''
  const description = typeof obj.description === 'string' ? obj.description : undefined

  const stagesObj = isRecord(obj.stages) ? obj.stages : {}
  const namesObj = isRecord(obj.stage_names) ? obj.stage_names : {}
  const interactiveObj = isRecord(obj.stage_interactive) ? obj.stage_interactive : {}
  const autonomousObj = isRecord(obj.stage_autonomous) ? obj.stage_autonomous : {}
  const autoApproveObj = isRecord(obj.stage_auto_approve) ? obj.stage_auto_approve : {}

  const order = resolveOrder(obj.stage_order, stagesObj)

  const stages: Stage[] = order.map((id) =>
    toStage(id, stagesObj[id], namesObj[id], interactiveObj[id] === true, autonomousObj[id] === true, autoApproveObj[id] === true),
  )

  return { flowName, stages, startedAt, description }
}

function resolveOrder(stageOrder: unknown, stagesObj: Record<string, unknown>): string[] {
  if (Array.isArray(stageOrder) && stageOrder.length > 0) {
    const filtered = stageOrder.filter((id): id is string => typeof id === 'string')
    if (filtered.length > 0) return filtered
  }

  return Object.keys(stagesObj).sort()
}

function toStage(
  id: string,
  raw: unknown,
  nameRaw: unknown,
  interactive: boolean,
  autonomous: boolean,
  autoApprove: boolean,
): Stage {
  const obj = isRecord(raw) ? raw : {}

  const status: StageStatus = isStageStatus(obj.status) ? obj.status : 'pending'
  const updatedAt = typeof obj.updated_at === 'string' ? obj.updated_at : ''
  const name = typeof nameRaw === 'string' ? nameRaw : ''

  return { id, name, status, updatedAt, interactive, autonomous, autoApprove }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object'
}

function isStageStatus(value: unknown): value is StageStatus {
  return typeof value === 'string' && (STAGE_STATUSES as readonly string[]).includes(value)
}
