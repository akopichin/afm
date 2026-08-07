import { useEffect, useState } from 'react'

const TICK_INTERVAL_MS = 1000

// Секундомер накопленного Idle-времени: accumulatedMs (пережившее restart,
// см. RunState.IdleAccumulatedMs на бэкенде) плюс живая дельта с since, пока
// флоу простаивает прямо сейчас (since не null). Пока connected=false —
// отображаемое значение просто держится на месте: сокет не обновляет
// accumulatedMs/since, поэтому дальнейший локальный тик мог бы показать
// неверное значение (стадия могла давно перестать простаивать).
export function useIdleMs(accumulatedMs: number, since: string | null, connected: boolean): number {
  const [displayMs, setDisplayMs] = useState(accumulatedMs)

  useEffect(() => {
    function compute(): number {
      if (since === null) return accumulatedMs
      const sinceMs = Date.parse(since)
      if (Number.isNaN(sinceMs)) return accumulatedMs
      return accumulatedMs + Math.max(0, Date.now() - sinceMs)
    }

    if (!connected) {
      setDisplayMs(compute())
      return
    }

    setDisplayMs(compute())
    const timer = setInterval(() => setDisplayMs(compute()), TICK_INTERVAL_MS)
    return () => clearInterval(timer)
  }, [accumulatedMs, since, connected])

  return displayMs
}
