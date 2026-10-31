import { useId, useState, type ReactElement } from 'react'
import { plural } from '../../lib/plural'
import type { UsageAnalytics, UsageCoverage } from '../../types/usage-analytics'
import { cacheWriteTokens, coverageLabel, formatCount, formatDurationMs, formatMoney, reasonText, tickedDuration } from './usage-format'

type UsageSummaryProps = {
  data: UsageAnalytics
  hasTokens: boolean
  hasMoney: boolean
  sinceAsOfMs: number
}

function Completeness({ data, hasTokens, hasMoney }: { data: UsageAnalytics; hasTokens: boolean; hasMoney: boolean }): ReactElement | null {
  const entries: [Parameters<typeof coverageLabel>[0], UsageCoverage | undefined][] = [
    ['time', data.completeness.time],
    ['tokens', hasTokens ? data.completeness.tokens : undefined],
    ['cost', hasMoney ? data.completeness.price : undefined],
  ]
  const items = entries.flatMap(([subject, coverage]) => {
    const label = coverage === undefined ? null : coverageLabel(subject, coverage.status)
    if (coverage === undefined || label === null) return []
    const reasons = coverage.reasons.flatMap((code) => reasonText(code, hasMoney) ?? [])
    return [{ subject, label, reasons }]
  })
  if (items.length === 0) return null
  return (
    <ul className="usage-coverage" aria-label="Data completeness">
      {items.map(({ subject, label, reasons }) => (
        <li key={subject}>
          <strong>{label}</strong>
          {reasons.length > 0 && <span>{`: ${reasons.join('; ')}`}</span>}
        </li>
      ))}
    </ul>
  )
}

// UsageSummary — итоги отфильтрованного набора, раскрытие состава токенов и подписи неполноты.
export function UsageSummary({ data, hasTokens, hasMoney, sinceAsOfMs }: UsageSummaryProps): ReactElement {
  const [mixOpen, setMixOpen] = useState(false)
  const mixId = useId()
  const { totals } = data
  const counts = totals.tokens?.counts ?? null
  const duration = tickedDuration(totals.agentTime.durationMs, totals.activeJobs > 0, totals.activeJobs * sinceAsOfMs)
  const outside = data.outsideRunTotal
  const outsideTime = outside === null ? null : tickedDuration(outside.agentTime.durationMs, outside.activeJobs > 0, outside.activeJobs * sinceAsOfMs)

  return (
    <>
      <div className="usage-summary" role="group" aria-label="Filtered usage totals">
        {hasMoney && <span><strong>{formatMoney(totals.money?.estimatedCostUsd ?? null)}</strong> estimated</span>}
        {hasTokens &&
          (counts === null ? (
            <span><strong>{formatCount(null)}</strong> tokens</span>
          ) : (
            <button type="button" className="usage-summary-tokens" aria-expanded={mixOpen} aria-controls={mixId} onClick={() => setMixOpen(!mixOpen)}>
              <strong>{formatCount(counts.total)}</strong> tokens <span className="usage-chevron" aria-hidden="true" />
            </button>
          ))}
        <span><strong>{formatDurationMs(duration)}</strong> agent time</span>
        <span><strong>{totals.jobs}</strong> agent {totals.jobs === 1 ? 'job' : 'jobs'}</span>
      </div>
      {hasTokens && counts !== null && (
        <div id={mixId} className="usage-token-mix" hidden={!mixOpen}>
          <dl>
            <div><dt>Uncached input</dt><dd>{formatCount(counts.uncachedInput)}</dd></div>
            <div><dt>Cache read</dt><dd>{formatCount(counts.cacheRead)}</dd></div>
            <div><dt>Cache write</dt><dd>{formatCount(cacheWriteTokens(counts))}</dd></div>
            <div><dt>Output</dt><dd>{formatCount(counts.output)}</dd></div>
          </dl>
          <p>Output includes reasoning. Uncached input excludes cache tokens.</p>
        </div>
      )}
      <Completeness data={data} hasTokens={hasTokens} hasMoney={hasMoney} />
      {outside !== null && outside.jobs > 0 && (
        <p className="usage-outside-note">
          {`Outside run total: ${plural(outside.jobs, 'agent job')}, ${formatDurationMs(outsideTime)} agent time (the Assistant is not part of the run).`}
        </p>
      )}
    </>
  )
}
