import { useEffect, useState } from 'react'

// Elapsed складывает завершённые активные интервалы с текущим. Для старого
// сервера без накопителя остаётся расчёт от startedAt. Отдельный секундный
// интервал даёт плавное обновление между ответами /api/status.
const TICK_INTERVAL_MS = 1000

export function useElapsed(
  startedAt: string,
  endedAt: string | null = null,
  live = true,
  accumulatedMs: number | null = null,
  activeSince: string | null = null,
): number {
  const [elapsedMs, setElapsedMs] = useState(0)

  useEffect(() => {
    const startMs = Date.parse(startedAt)
    const endMs = endedAt === null ? NaN : Date.parse(endedAt)
    const activeSinceMs = activeSince === null ? NaN : Date.parse(activeSince)
    const ended = !Number.isNaN(endMs)
    const hasActiveCounter = accumulatedMs !== null

    function compute(): number {
      if (hasActiveCounter) {
        if (ended || Number.isNaN(activeSinceMs)) return Math.max(0, accumulatedMs ?? 0)
        return Math.max(0, (accumulatedMs ?? 0) + Math.max(0, Date.now() - activeSinceMs))
      }
      if (Number.isNaN(startMs)) return 0
      return Math.max(0, (ended ? endMs : Date.now()) - startMs)
    }

    if (!live && !ended) return
    setElapsedMs(compute())
    if (ended || (hasActiveCounter && Number.isNaN(activeSinceMs)) || (Number.isNaN(startMs) && !hasActiveCounter)) return

    const timer = setInterval(() => {
      setElapsedMs(compute())
    }, TICK_INTERVAL_MS)

    return () => clearInterval(timer)
  }, [startedAt, endedAt, live, accumulatedMs, activeSince])

  return elapsedMs
}
