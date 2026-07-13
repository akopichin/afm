import { useEffect, useState } from 'react'

// Секундомер прошедшего времени от startedAt. Соответствует elapsedTimer в текущем app.js:
// собственный односекундный интервал, не привязанный к циклу поллинга стадий, чтобы
// счётчик обновлялся плавно каждую секунду.
const TICK_INTERVAL_MS = 1000

export function useElapsed(startedAt: string): number {
  const [elapsedMs, setElapsedMs] = useState(0)

  useEffect(() => {
    const startMs = Date.parse(startedAt)
    if (Number.isNaN(startMs)) {
      setElapsedMs(0)
      return
    }

    setElapsedMs(Date.now() - startMs)

    const timer = setInterval(() => {
      setElapsedMs(Date.now() - startMs)
    }, TICK_INTERVAL_MS)

    return () => clearInterval(timer)
  }, [startedAt])

  return elapsedMs
}
