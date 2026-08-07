import { useEffect, useState } from 'react'

const TICK_INTERVAL_MS = 1000

// Секундомер накопленного Backoff-времени: accumulatedMs (пережившее restart)
// плюс сумма живых дельт для каждого сейчас открытого эпизода в openSince —
// параллельные ретраи суммируются, а не мёржатся (см. use-status-duration.ts,
// которую этот хук заменяет). Как и useIdleMs, замораживает отображаемое
// значение при connected=false.
export function useBackoffMs(accumulatedMs: number, openSince: string[], connected: boolean): number {
  const [displayMs, setDisplayMs] = useState(accumulatedMs)

  useEffect(() => {
    function compute(): number {
      const now = Date.now()
      let liveMs = 0
      for (const since of openSince) {
        const sinceMs = Date.parse(since)
        if (!Number.isNaN(sinceMs)) {
          liveMs += Math.max(0, now - sinceMs)
        }
      }
      return accumulatedMs + liveMs
    }

    if (!connected) {
      setDisplayMs(compute())
      return
    }

    setDisplayMs(compute())
    const timer = setInterval(() => setDisplayMs(compute()), TICK_INTERVAL_MS)
    return () => clearInterval(timer)
  }, [accumulatedMs, openSince, connected])

  return displayMs
}
