import { useEffect, useRef, type ReactElement } from 'react'
import type { UsageAnalyticsController } from '../../hooks/use-usage-analytics'
import type { UsageAnalytics, UsageJob, UsageJobStatus } from '../../types/usage-analytics'
import { ModelMark } from './ModelMark'
import { SortHeader } from './SortHeader'
import { formatCount, formatDurationMs, formatMoney, formatOffset, tickedDuration } from './usage-format'

type JobsTableProps = {
  analytics: UsageAnalyticsController
  data: UsageAnalytics
  // Время, прошедшее после as_of: длительность running-job тикает на нём.
  sinceAsOfMs: number
}

// Статус показывается словами только когда он объясняет цифры: ok ничего не добавляет.
const STATUS_LABELS: Record<UsageJobStatus, string> = {
  running: 'Running',
  ok: '',
  error: 'Failed',
  interrupted: 'Interrupted',
  idle_timeout: 'Idle timeout',
  usage_limit: 'Usage limit',
  cancelled: 'Cancelled',
  yielded: 'Yielded',
}

export function JobsTable({ analytics, data, sinceAsOfMs }: JobsTableProps): ReactElement | null {
  const page = data.jobs
  const rowRefs = useRef(new Map<string, HTMLTableRowElement>())
  const focusedFor = useRef('')
  const highlighted = analytics.query.highlightJobId

  // Выделенная строка получает фокус один раз на выделение: поллинг не должен отбирать фокус у пользователя.
  useEffect(() => {
    if (highlighted === '') {
      focusedFor.current = ''
      return
    }
    const row = rowRefs.current.get(highlighted)
    if (row === undefined || focusedFor.current === highlighted) return
    focusedFor.current = highlighted
    row.focus({ preventScroll: true })
    row.scrollIntoView?.({ block: 'nearest' })
  }, [highlighted, page?.items])

  if (page === null) return null
  const hasTokens = analytics.metrics.includes('tokens') && data.metrics.includes('tokens')
  const hasMoney = analytics.metrics.includes('cost') && data.metrics.includes('cost')
  const sort = analytics.query.sort
  const stageTitle = (id: string): string => (id === '' ? 'Run-wide' : (data.catalog.stages.find((stage) => stage.id === id)?.title ?? id))

  function renderRow(job: UsageJob): ReactElement {
    const running = job.status === 'running'
    const isHighlighted = job.id === highlighted
    const status = STATUS_LABELS[job.status]
    const stage = stageTitle(job.stageId)
    // Стадийный агент назван по своей стадии: повтор имени ничего не добавляет.
    const meta = job.agent.title === stage ? stage : `${job.agent.title} · ${stage}`
    return (
      <tr
        key={job.id}
        ref={(row) => {
          if (row === null) rowRefs.current.delete(job.id)
          else rowRefs.current.set(job.id, row)
        }}
        data-job-id={job.id}
        className={isHighlighted ? 'is-highlighted' : undefined}
        aria-current={isHighlighted ? 'true' : undefined}
        tabIndex={isHighlighted ? -1 : undefined}
      >
        <td className="usage-job-name">
          <strong>{job.title}</strong>
          <small>
            {meta}
            {job.agent.outsideRunTotal && <span className="usage-job-flag">Outside run total</span>}
            {status !== '' && <span className={`usage-job-flag usage-job-status is-${job.status}`}>{status}</span>}
          </small>
        </td>
        <td className="usage-job-model">
          <ModelMark ordinal={job.model?.colorOrdinal ?? null} />
          <span>{job.model?.model ?? 'Unknown model'}</span>
          {job.modelSource === 'hint' && <small> configured</small>}
        </td>
        <td className="usage-numeric" data-label="Start">{formatOffset(job.runOffsetMs)}</td>
        <td className="usage-numeric" data-label="Duration">{formatDurationMs(tickedDuration(job.durationMs, running, sinceAsOfMs))}</td>
        {hasTokens && <td className="usage-numeric" data-label="Tokens">{formatCount(job.tokens?.counts?.total ?? null)}</td>}
        {hasMoney && <td className="usage-numeric usage-money" data-label="Est. cost">{formatMoney(job.money?.estimatedCostUsd ?? null)}</td>}
      </tr>
    )
  }

  const first = analytics.page * page.limit + 1
  const last = analytics.page * page.limit + page.items.length
  // aria-disabled, не disabled: нажатая кнопка не должна терять фокус, пока грузится страница.
  const loading = analytics.state === 'loading'
  return (
    <>
      <table className="usage-table usage-jobs" aria-label="Agent jobs">
        <thead>
          <tr>
            <SortHeader label="Job / stage" sortKey="name" current={sort} onSort={analytics.sortBy} />
            <SortHeader label="Model" sortKey="model" current={sort} onSort={analytics.sortBy} />
            <SortHeader label="Start" sortKey="started" current={sort} onSort={analytics.sortBy} numeric />
            <SortHeader label="Duration" sortKey="time" current={sort} onSort={analytics.sortBy} numeric />
            {hasTokens && <SortHeader label="Tokens" sortKey="tokens" current={sort} onSort={analytics.sortBy} numeric />}
            {hasMoney && <SortHeader label="Est. cost" sortKey="cost" current={sort} onSort={analytics.sortBy} numeric />}
          </tr>
        </thead>
        <tbody>{page.items.map(renderRow)}</tbody>
      </table>
      <div className="usage-pagination">
        <span>{`${first}–${last} of ${page.total} ${page.total === 1 ? 'job' : 'jobs'}`}</span>
        <span className="usage-start-note">Start is relative to run start (active time)</span>
        <div className="usage-pager">
          <button type="button" className="usage-pager-button" aria-label="Previous jobs" disabled={page.prevCursor === null} aria-disabled={loading} onClick={analytics.prevPage}>
            <span className="usage-pager-arrow is-prev" aria-hidden="true" />
          </button>
          <span>{`${analytics.page + 1} / ${analytics.pageCount}`}</span>
          <button type="button" className="usage-pager-button" aria-label="Next jobs" disabled={page.nextCursor === null} aria-disabled={loading} onClick={analytics.nextPage}>
            <span className="usage-pager-arrow" aria-hidden="true" />
          </button>
        </div>
      </div>
    </>
  )
}
