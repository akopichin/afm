import { useEffect, useState } from 'react'

const TICK_INTERVAL_MS = 1000

// Секундомер накопленного Idle-времени: accumulatedMs плюс живая дельта с since, пока флоу простаивает
// прямо сейчас.
export function useIdleMs(accumulatedMs: number, since: string | null, statusLive: boolean): number {
  const [displayMs, setDisplayMs] = useState(accumulatedMs)

  useEffect(() => {
    function compute(): number {
      if (since === null) return accumulatedMs
      const sinceMs = Date.parse(since)
      if (Number.isNaN(sinceMs)) return accumulatedMs
      return accumulatedMs + Math.max(0, Date.now() - sinceMs)
    }

    if (!statusLive) {
      if (since === null) setDisplayMs(accumulatedMs)
      return
    }

    setDisplayMs(compute())
    const timer = setInterval(() => setDisplayMs(compute()), TICK_INTERVAL_MS)
    return () => clearInterval(timer)
  }, [accumulatedMs, since, statusLive])

  return displayMs
}
