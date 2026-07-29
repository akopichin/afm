import { useEffect, useRef, useState } from 'react'
import { extractStageStatus, type AfmEvent, type StageStatus } from '../../types'

const TICK_INTERVAL_MS = 1000

// Вопрос к пользователю — всегда idle, независимо от того, работает ли в
// этот момент другой агент: открытый диалог или ожидание одобрения плана.
const QUESTION_STATUSES: ReadonlySet<StageStatus> = new Set(['awaiting_user_input', 'awaiting_approval'])
// Агент реально что-то делает. retrying сюда намеренно не входит — это
// пассивный бэкофф-таймер, а не работа агента (см. BACKOFF_STATUSES).
const AGENT_ACTIVE_STATUSES: ReadonlySet<StageStatus> = new Set(['running', 'planning', 'revising'])

// Idle — время, когда флоу реально ждёт пользователя, а не просто «какая-то
// стадия упала, пока другая работает». Это ЕДИНОЕ состояние на весь флоу
// (в отличие от useStatusDuration, который суммирует независимые per-стадийные
// эпизоды):
//
//   idle(t) = есть вопрос к пользователю на ЛЮБОЙ стадии
//             ИЛИ (есть failed-стадия И ни один агент сейчас не активен)
//
// Баг, который это чинит: ретраишь упавшую стадию (агент реально думает), а
// downstream-стадии, упавшие каскадно (blocked_by_dep), продолжали копить
// Idle просто потому что формально «failed» — хотя пользователя никто не
// ждёт, работа идёт полным ходом. Теперь failed перестаёт быть источником
// Idle, как только активен ЛЮБОЙ агент.
//
// Инкрементально обходит всю хронологию stage_status_changed (ключ дедупа —
// stageId+timestamp+статус, как в useStatusDuration) — уже посчитанное время
// не теряется, даже если события вытесняются из капа MAX_EVENTS=200 у
// useEventFeed. Между двумя последовательными событиями мир (статусы всех
// стадий) не меняется, поэтому вклад в Idle считается по состоянию ДО
// применения очередного события.
export function useIdleTime(events: AfmEvent[]): number {
  const processedKeys = useRef<Set<string>>(new Set())
  const statusByStage = useRef<Map<string, StageStatus>>(new Map())
  const lastTs = useRef<number | null>(null)
  const [accumulatedMs, setAccumulatedMs] = useState(0)
  const [, forceTick] = useState(0)
  const [, forceRerender] = useState({})

  useEffect(() => {
    let delta = 0

    for (const event of events) {
      if (event.type !== 'stage_status_changed') continue

      const status = extractStageStatus(event.payload)
      if (status === null) continue

      const key = `${event.stageId}|${event.timestamp}|${status}`
      if (processedKeys.current.has(key)) continue
      processedKeys.current.add(key)

      const ts = Date.parse(event.timestamp)
      if (Number.isNaN(ts)) continue

      if (lastTs.current !== null && ts > lastTs.current && isIdle(statusByStage.current)) {
        delta += ts - lastTs.current
      }

      statusByStage.current.set(event.stageId, status)
      lastTs.current = ts
    }

    if (delta !== 0) {
      setAccumulatedMs((prev) => prev + delta)
    }
    // Пересчитать, если сейчас idle, но дельта = 0 (нужен ре-рендер, чтобы
    // ниже пересчиталась живая liveMs-дельта).
    if (delta === 0 && lastTs.current !== null && isIdle(statusByStage.current)) {
      forceRerender({})
    }
  }, [events])

  useEffect(() => {
    const timer = setInterval(() => forceTick((t) => t + 1), TICK_INTERVAL_MS)
    return () => clearInterval(timer)
  }, [])

  let liveMs = 0
  if (lastTs.current !== null && isIdle(statusByStage.current)) {
    liveMs = Date.now() - lastTs.current
  }

  return accumulatedMs + liveMs
}

function isIdle(statusByStage: ReadonlyMap<string, StageStatus>): boolean {
  let hasFailed = false
  let anyActive = false

  for (const status of statusByStage.values()) {
    if (QUESTION_STATUSES.has(status)) return true
    if (status === 'failed') hasFailed = true
    if (AGENT_ACTIVE_STATUSES.has(status)) anyActive = true
  }

  return hasFailed && !anyActive
}
