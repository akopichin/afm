import { useEffect, useRef, useState, type ReactElement } from 'react'

type Decision = { decision: string; reason: string } | null

type SupervisorDecisionProps = {
  stageId: string
}

// Решение супервизора для выбранной стадии. Точка в углу статус-бейджа; причина —
// в поповере по клику. Источник: /api/stages/<id>/supervisor (поллинг), не теряется
// при позднем подключении дашборда. См. также .status-badge-wrap в App.tsx.
export function SupervisorDecision({ stageId }: SupervisorDecisionProps): ReactElement | null {
  const [decision, setDecision] = useState<Decision>(null)
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLSpanElement>(null)

  // Загрузка решения (поллинг). Смена стадии сбрасывает открытый поповер.
  useEffect(() => {
    setOpen(false)
    let cancelled = false
    async function load(): Promise<void> {
      try {
        const res = await fetch(`/api/stages/${encodeURIComponent(stageId)}/supervisor`)
        if (!res.ok) {
          if (!cancelled) setDecision(null)
          return
        }
        const data = (await res.json()) as { decision?: string; reason?: string }
        if (cancelled) return
        setDecision(
          data.decision != null ? { decision: data.decision, reason: data.reason ?? '' } : null,
        )
      } catch {
        /* сеть/ответ — оставляем прошлое значение */
      }
    }
    void load()
    const timer = setInterval(load, 3000)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [stageId])

  // Закрытие поповера по клику вне и по Escape — слушатели активны только при open.
  useEffect(() => {
    if (!open) return
    function onDown(e: MouseEvent): void {
      if (rootRef.current !== null && !rootRef.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    function onKey(e: KeyboardEvent): void {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  if (decision == null) return null
  const trackClass = decision.decision === 'autonomous' ? 'autonomous' : 'standard'
  return (
    <span className="supervisor-decision" ref={rootRef}>
      <button
        type="button"
        className={`supervisor-dot ${trackClass}`}
        aria-label={`supervisor decision: ${decision.decision}`}
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      />
      {open && (
        <div className="supervisor-popover" role="dialog">
          <div className="supervisor-popover-title">supervisor: {decision.decision}</div>
          {decision.reason !== '' && (
            <div className="supervisor-popover-reason">{decision.reason}</div>
          )}
        </div>
      )}
    </span>
  )
}
