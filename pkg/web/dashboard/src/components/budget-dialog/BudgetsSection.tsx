import type { ReactElement } from 'react'
import { POLICY_TITLES, budgetFigures, scopeLabel, spendFigures, statusLevel, statusText } from '../../lib/budget-format'
import type { BudgetView } from '../../types/budget'
import { useBudgetUi } from './budget-context'
import { BudgetEditButton } from './BudgetEditButton'

type BudgetsSectionProps = { stageIds: string[] }

type Row = { scope: string; view: BudgetView | undefined; nested: 'helper' | 'run_agent' | null }

// BudgetsSection — раздел Budgets вкладки Cost: все области рана (Run, run agents под ним, стадии с помощниками), у каждой
// «Edit». Не зависит от accounting: лимит можно задать и увидеть при скрытой аналитике, и здесь же — до порога 80%.
export function BudgetsSection({ stageIds }: BudgetsSectionProps): ReactElement | null {
  const { enabled, views, names, stageOf } = useBudgetUi()
  if (!enabled) return null

  const all = [...views.values()]
  const helpersOf = (stageId: string): Row[] =>
    all.filter((v) => v.kind === 'helper' && stageOf.get(v.scope) === stageId).map((v) => ({ scope: v.scope, view: v, nested: 'helper' }))
  const rows: Row[] = [{ scope: 'run', view: views.get('run'), nested: null }]
  for (const v of all) if (v.kind === 'run_agent') rows.push({ scope: v.scope, view: v, nested: 'run_agent' })
  for (const id of stageIds) {
    const scope = `stage:${id}`
    rows.push({ scope, view: views.get(scope), nested: null }, ...helpersOf(id))
  }
  // Помощник стадии, которой нет в списке (или у задачи нет известной стадии), всё равно виден.
  const shown = new Set(rows.map((r) => r.scope))
  for (const v of all) if (!shown.has(v.scope)) rows.push({ scope: v.scope, view: v, nested: v.kind === 'run_agent' ? 'run_agent' : v.kind === 'helper' ? 'helper' : null })

  return (
    <section className="cost-budgets" aria-label="Budgets">
      <h3>Budgets</h3>
      <ul className="cost-budgets-list">
        {rows.map(({ scope, view, nested }) => {
          const limited = view?.effective != null
          const level = view === undefined ? 'ok' : statusLevel(view)
          const status = statusText(view)
          return (
            <li key={scope} className="cost-budgets-row" data-scope={scope} data-nested={nested ?? undefined} data-limited={limited} data-level={level}>
              <span className="cost-budgets-label">{scopeLabel(scope, names, view?.kind)}</span>
              <span className="cost-budgets-figures">
                {view === undefined || view.effective === null
                  ? `No limit${view !== undefined ? ` · ${spendFigures(view.spent)}` : ''}`
                  : `${budgetFigures(view.effective, view.spent, view.evaluation)} · ${POLICY_TITLES[view.effective.stopPolicy]}`}
                {status !== null && ` · ${status}`}
              </span>
              <BudgetEditButton scope={scope} text="Edit" />
            </li>
          )
        })}
      </ul>
    </section>
  )
}
