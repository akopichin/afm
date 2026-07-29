import { useEffect, useRef, useState } from 'react'
import { extractStageStatus, type AfmEvent, type StageStatus } from '../../types'

const TICK_INTERVAL_MS = 1000

// Кумулятивное время (мс), которое хотя бы одна стадия провела в одном из
// статусов набора `statuses` — используется и для Idle (ждём пользователя:
// awaiting_user_input/awaiting_approval/failed), и для Backoff (retrying),
// см. IDLE_STATUSES/BACKOFF_STATUSES в types/stage.ts.
//
// Обрабатывает stage_status_changed ИНКРЕМЕНТАЛЬНО: каждое событие учитывается
// ровно один раз (ключ по stageId+timestamp+статусу), уже закрытые эпизоды
// остаются в accumulatedMs даже после того, как соответствующие события
// вытесняются из капа MAX_EVENTS=200 у useEventFeed — простой рескан всего
// массива при каждом изменении потерял бы старые эпизоды молча.
//
// Пока стадия открыта (её статус в `statuses`), к возвращаемому значению
// добавляется живая дельта (now − openedAt), тикающая раз в секунду — как
// useElapsed. Параллельно открытые эпизоды разных стадий суммируются, не
// мёржатся (см. дизайн-документ) — редкий кейс параллельных стадий может
// дать сумму чуть больше wall-clock elapsed, это осознанное упрощение.
export function useStatusDuration(events: AfmEvent[], statuses: ReadonlySet<StageStatus>): number {
  const processedKeys = useRef<Set<string>>(new Set())
  const openSince = useRef<Map<string, number>>(new Map())
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

      const isOpenStatus = statuses.has(status)
      const openedAt = openSince.current.get(event.stageId)

      if (isOpenStatus && openedAt === undefined) {
        openSince.current.set(event.stageId, ts)
      } else if (!isOpenStatus && openedAt !== undefined) {
        delta += ts - openedAt
        openSince.current.delete(event.stageId)
      }
    }

    // Обновим накопленное время при изменении дельты
    if (delta !== 0) {
      setAccumulatedMs((prev) => prev + delta)
    }
    // Пересчитаем, если открыты эпизоды, но дельта = 0
    // (нужен пересчёт для вычисления и возврата живой liveMs дельты)
    if (delta === 0 && openSince.current.size > 0) {
      forceRerender({})
    }
  }, [events, statuses])

  useEffect(() => {
    const timer = setInterval(() => forceTick((t) => t + 1), TICK_INTERVAL_MS)
    return () => clearInterval(timer)
  }, [])

  let liveMs = 0
  const now = Date.now()
  for (const openedAt of openSince.current.values()) {
    liveMs += now - openedAt
  }

  return accumulatedMs + liveMs
}
