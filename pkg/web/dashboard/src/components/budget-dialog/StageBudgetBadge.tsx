import type { ReactElement } from 'react'
import { statusLevel, statusText, viewPercent } from '../../lib/budget-format'
import { useBudgetUi } from './budget-context'

// StageBudgetBadge — бейдж бюджета в строке стадии: только от 80% своего лимита или при удержании. Отдельная кнопка рядом
// со строкой (не внутри неё), поэтому имя кнопки строки не меняется. До 80% окно открывается из Cost.
export function StageBudgetBadge({ stageId }: { stageId: string }): ReactElement | null {
  const { enabled, views, openDialog } = useBudgetUi()
  const view = views.get(`stage:${stageId}`)
  if (!enabled || view === undefined) return null
  const level = statusLevel(view)
  if (level === 'ok') return null
  const full = statusText(view) ?? 'Budget'
  const percent = viewPercent(view)
  const short = view.hold !== null ? 'Stopped' : view.draining ? 'Finishing' : `${percent ?? 0}%`
  return (
    <button
      type="button"
      className="stage-budget-badge"
      data-level={level}
      aria-label={`Budget for stage ${stageId}: ${full}. Open budget`}
      title={full}
      onClick={(e) => openDialog(`stage:${stageId}`, e.currentTarget)}
    >
      {short}
    </button>
  )
}
