import { useEffect, useState, type ReactElement } from 'react'

type Decision = { decision: string; reason: string } | null

type SupervisorDecisionProps = {
  stageId: string
}

// Показывает решение супервизора для выбранной стадии (персистентно).
// Источник: /api/stages/<id>/supervisor (читает supervisor.jsonl). В отличие от
// live-события шины EventSupervisorDecision, читается при каждом заходе и поллом —
// не теряется, если дашборд подключился после старта стадии.
export function SupervisorDecision({ stageId }: SupervisorDecisionProps): ReactElement | null {
  const [decision, setDecision] = useState<Decision>(null)

  useEffect(() => {
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

  if (decision == null) return null
  const autonomous = decision.decision === 'autonomous'
  return (
    <span
      className={`supervisor-badge${autonomous ? ' autonomous' : ' standard'}`}
      title={decision.reason}
    >
      <span className="supervisor-icon" aria-hidden="true">
        ◆
      </span>
      supervisor: {decision.decision}
      {decision.reason !== '' && <span className="supervisor-reason"> — {decision.reason}</span>}
    </span>
  )
}
