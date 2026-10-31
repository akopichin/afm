import { useEffect, useState } from 'react'

const TICK_INTERVAL_MS = 1000

// useStageClock отдаёт текущее время для живых интервалов стадий. Пока active=false таймера нет и значение
// застывает; при возврате оно сразу обновляется, а не ждёт первого тика.
export function useStageClock(active: boolean): number {
  const [nowMs, setNowMs] = useState(() => Date.now())

  useEffect(() => {
    if (!active) return
    setNowMs(Date.now())
    const timer = setInterval(() => setNowMs(Date.now()), TICK_INTERVAL_MS)
    return () => clearInterval(timer)
  }, [active])

  return nowMs
}
