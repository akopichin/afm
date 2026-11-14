import type { ReactElement } from 'react'
import { useBudgetUi, useScopeLabel } from './budget-context'

type BudgetEditButtonProps = {
  scope: string
  // compact — только иконка (для тесных строк); подпись остаётся в aria-label.
  compact?: boolean
  // text — видимая подпись; по умолчанию «Edit budget».
  text?: string
  className?: string
}

const PENCIL = (
  <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M4 20h4L19 9a2.8 2.8 0 0 0-4-4L4 16v4ZM13.5 6.5l4 4" />
  </svg>
)

// BudgetEditButton — вход в окно бюджета области. Окно показывает ТЕКУЩИЙ view области, а не значения notice, из
// которого его открыли. Без бюджетов (capability выключена) кнопки нет.
export function BudgetEditButton({ scope, compact = false, text = 'Edit budget', className = '' }: BudgetEditButtonProps): ReactElement | null {
  const { enabled, openDialog } = useBudgetUi()
  const label = useScopeLabel()(scope)
  if (!enabled) return null
  return (
    <button
      type="button"
      className={`budget-edit-btn${compact ? ' budget-edit-btn--compact' : ''}${className !== '' ? ` ${className}` : ''}`}
      aria-label={`Edit budget for ${label}`}
      title="Edit budget"
      onClick={(e) => openDialog(scope, e.currentTarget)}
    >
      {PENCIL}
      {!compact && <span className="budget-edit-btn-label">{text}</span>}
    </button>
  )
}
