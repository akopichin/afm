import type { ReactElement } from 'react'
import type { UsageMetrics } from '../../types/usage-analytics'
import { formatCount, formatDurationMs, formatMoney, formatShare, tickedDuration } from './usage-format'

// Ячейки метрик одной группы jobs: строки Breakdown и итоги используют одни и те же подписи неполноты.

type CellProps = { metrics: UsageMetrics; label: string }

// TimeCell: неизвестная длительность — тире и подпись, частично известная — подпись Partial time.
export function TimeCell({ metrics, label, sinceAsOfMs }: CellProps & { sinceAsOfMs: number }): ReactElement {
  const { agentTime } = metrics
  const duration = tickedDuration(agentTime.durationMs, metrics.activeJobs > 0, metrics.activeJobs * sinceAsOfMs)
  let note = ''
  if (agentTime.durationMs === null) note = 'Time unknown'
  else if (agentTime.unknownJobs > 0) note = 'Partial time'
  return (
    <td className="usage-numeric" data-label={label}>
      {formatDurationMs(duration)}
      {note !== '' && <small>{note}</small>}
    </td>
  )
}

export function TokensCell({ metrics, label }: CellProps): ReactElement {
  const tokens = metrics.tokens
  const total = tokens?.counts?.total ?? null
  let note = ''
  if (metrics.jobs > 0 && total === null) note = 'No usage'
  else if (tokens !== undefined && (tokens.unmeteredJobs > 0 || tokens.pendingJobs > 0)) note = 'Partial tokens'
  return (
    <td className="usage-numeric" data-label={label}>
      {formatCount(total)}
      {note !== '' && <small>{note}</small>}
    </td>
  )
}

// MoneyCell: доля от итога показывается, только когда итог известен; Assistant вне итога доли не имеет.
export function MoneyCell({ metrics, label, total }: CellProps & { total: number | null }): ReactElement {
  const money = metrics.money
  const cost = money?.estimatedCostUsd ?? null
  const share = formatShare(cost, total)
  let note = share === '' ? '' : `${share} of total`
  if (cost !== null && (money?.unpricedJobs ?? 0) > 0) note = 'Partial cost'
  else if (cost === null && metrics.jobs > 0) note = 'No price'
  return (
    <td className="usage-numeric usage-money" data-label={label}>
      {formatMoney(cost)}
      {note !== '' && <small>{note}</small>}
    </td>
  )
}
