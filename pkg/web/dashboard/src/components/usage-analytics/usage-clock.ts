import { useState } from 'react'
import { useStageClock } from '../../hooks/use-stage-clock'

// useSinceAsOf — сколько миллисекунд прошло после as_of ответа, пока тикать можно (свежий /api/status и есть
// running-работа). Без этого длительности стоят на значениях as_of.
export function useSinceAsOf(asOf: string | undefined, active: boolean): number {
  const nowMs = useStageClock(active)
  const anchor = asOf === undefined ? Number.NaN : Date.parse(asOf)
  const ticking = active && Number.isFinite(anchor) ? Math.max(0, nowMs - anchor) : null
  // Без свежего статуса длительность замирает на последнем показанном значении, а не откатывается к as_of;
  // прибавка помнится только для своего as_of, у нового ответа её ещё нет.
  const [frozen, setFrozen] = useState({ asOf, sinceMs: 0 })
  if (ticking !== null && (frozen.asOf !== asOf || frozen.sinceMs !== ticking)) setFrozen({ asOf, sinceMs: ticking })
  return ticking ?? (frozen.asOf === asOf ? frozen.sinceMs : 0)
}
