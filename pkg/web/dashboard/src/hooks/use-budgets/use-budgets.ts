import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { fetchBudgets } from '../../api/budget-client'
import type { AfmEvent } from '../../types'
import type { BudgetView } from '../../types/budget'

const NO_VIEWS: BudgetView[] = []

// lastBudgetEventKey — ключ последнего budget_*-события окна: меняется на каждое новое событие, даже когда окно
// вытесняет старые и его длина не растёт.
export function lastBudgetEventKey(events: AfmEvent[]): string {
  for (let i = events.length - 1; i >= 0; i--) {
    const e = events[i] as AfmEvent
    if (e.type.startsWith('budget_')) return `${e.seq ?? ''}:${e.receipt ?? ''}:${e.timestamp}:${e.type}`
  }
  return ''
}

type UseBudgetsOptions = {
  enabled: boolean
  runId: string
  // tick — счётчик успешных ответов /api/status: список обновляется в такт опросу статуса.
  tick: number
  // eventKey — lastBudgetEventKey: новое budget_*-событие обновляет список сразу.
  eventKey: string
}

export type BudgetsState = {
  views: BudgetView[]
  byScope: ReadonlyMap<string, BudgetView>
  // apply кладёт свежий view из ответа update/resume, не дожидаясь следующего опроса.
  apply: (view: BudgetView) => void
}

// useBudgets — список бюджетов рана. Запросов нет без capabilities.budgets; ответ предыдущего рана или
// устаревший ответ не перезаписывают новое состояние.
export function useBudgets({ enabled, runId, tick, eventKey }: UseBudgetsOptions): BudgetsState {
  const [state, setState] = useState<{ runId: string; views: BudgetView[] }>({ runId, views: NO_VIEWS })
  const latest = useRef(0)

  useEffect(() => {
    const requestId = ++latest.current
    if (!enabled) {
      setState((prev) => (prev.views === NO_VIEWS ? prev : { runId, views: NO_VIEWS }))
      return
    }
    fetchBudgets().then(
      (views) => {
        if (latest.current === requestId) setState({ runId, views })
      },
      () => {
        // Сбой одного опроса оставляет прошлый список: следующий тик повторит запрос.
      },
    )
  }, [enabled, runId, tick, eventKey])

  const apply = useCallback(
    (view: BudgetView) => {
      latest.current++
      setState((prev) => {
        const current = prev.runId === runId ? prev.views : NO_VIEWS
        const next = current.some((v) => v.scope === view.scope) ? current.map((v) => (v.scope === view.scope ? view : v)) : [...current, view]
        return { runId, views: next }
      })
    },
    [runId],
  )

  const views = state.runId === runId ? state.views : NO_VIEWS
  const byScope = useMemo(() => new Map(views.map((v) => [v.scope, v])), [views])
  return { views, byScope, apply }
}
