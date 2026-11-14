import { createContext, useContext } from 'react'
import { NO_SCOPE_NAMES, scopeLabel, type BudgetScopeNames } from '../../lib/budget-format'
import type { BudgetView, BudgetViewKind } from '../../types/budget'

// BudgetUi — всё, что поверхностям бюджета нужно от App: данные списка, подписи областей и вход в окно.
// Контекст по умолчанию — «бюджетов нет»: компоненты вне App (и тесты) ничего не рисуют.
export type BudgetUi = {
  enabled: boolean
  views: ReadonlyMap<string, BudgetView>
  names: BudgetScopeNames
  // stageOf — стадия помощника по области задачи: для группировки в списке Budgets.
  stageOf: ReadonlyMap<string, string>
  // openDialog открывает окно области; opener получает фокус при закрытии, если он ещё в документе.
  openDialog: (scope: string, opener: HTMLElement | null) => void
}

export const NO_BUDGET_UI: BudgetUi = { enabled: false, views: new Map(), names: NO_SCOPE_NAMES, stageOf: new Map(), openDialog: () => {} }

export const BudgetUiContext = createContext<BudgetUi>(NO_BUDGET_UI)

export function useBudgetUi(): BudgetUi {
  return useContext(BudgetUiContext)
}

export function useScopeLabel(): (scope: string, kind?: BudgetViewKind) => string {
  const { names, views } = useBudgetUi()
  return (scope, kind) => scopeLabel(scope, names, kind ?? views.get(scope)?.kind)
}
