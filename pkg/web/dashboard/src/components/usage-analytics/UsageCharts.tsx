import type { ReactElement } from 'react'
import type { UsageAnalyticsController } from '../../hooks/use-usage-analytics'
import { modelColorStyle } from '../../lib/model-colors'
import type { UsageAnalytics, UsageInterval, UsageMetric, UsageRow } from '../../types/usage-analytics'
import { ModelMark } from './ModelMark'
import { formatCount, formatDurationMs, formatMoney, formatOffset, formatShare, formatTickMs, tickedDuration, tickedMetricValue } from './usage-format'

type UsageChartsProps = {
  analytics: UsageAnalyticsController
  data: UsageAnalytics
  // Время, прошедшее после as_of: running-job на шкале растёт на нём.
  sinceAsOfMs: number
}

export const METRIC_NAMES: Record<UsageMetric, string> = { cost: 'Estimated cost', agent_time: 'Agent time', tokens: 'Tokens' }

function formatMetric(value: number | null, metric: UsageMetric): string {
  if (metric === 'cost') return formatMoney(value)
  return metric === 'agent_time' ? formatDurationMs(value) : formatCount(value)
}

// Полоса агента берёт цвет единственной модели; при смешанной или неизвестной модели цвет нейтральный, а подпись говорит почему.
function agentModelNote(row: UsageRow): string {
  if (row.agent === null || row.modelState === 'single') return ''
  return row.modelState === 'mixed' ? 'Mixed models' : 'Model unknown'
}

function Comparison({ analytics, data, sinceAsOfMs }: UsageChartsProps): ReactElement | null {
  const charts = data.charts
  if (charts === null) return null
  const metric = analytics.query.metric
  const byModel = charts.group === 'model'
  const ranked = charts.comparison
    .map((row) => ({ row, value: tickedMetricValue(row, metric, sinceAsOfMs) }))
    .sort((a, b) => (b.value ?? -1) - (a.value ?? -1))
  const max = Math.max(0, ...ranked.map((entry) => entry.value ?? 0))
  const sum = ranked.reduce((acc, entry) => acc + (entry.value ?? 0), 0)
  const heading = `${METRIC_NAMES[metric]} by ${charts.group}`

  return (
    <section className="usage-chart" aria-label={heading}>
      <div className="usage-chart-heading">
        <h3>{heading}</h3>
        <span>Select a bar to see jobs</span>
      </div>
      <div className="usage-bars">
        {ranked.map(({ row, value }) => {
          const share = formatShare(value, sum)
          const text = formatMetric(value, metric)
          const note = byModel ? '' : agentModelNote(row)
          return (
            <button
              key={row.key}
              type="button"
              className="usage-bar-row"
              aria-label={`${row.label}: ${text}${share === '' ? '' : `, ${share.replace('%', '')} percent`}. View jobs.`}
              onClick={() => analytics.drill(byModel ? { model: row.key } : { agent: row.agent?.id ?? row.key })}
            >
              <span className="usage-bar-label">
                {row.label}
                {note !== '' && <small>{note}</small>}
              </span>
              <span className="usage-bar-track">
                <span
                  className="usage-bar-fill"
                  style={{ ...modelColorStyle(row.model?.colorOrdinal ?? null), width: `${max > 0 && value !== null ? (value / max) * 100 : 0}%` }}
                />
              </span>
              <span className="usage-bar-value">
                {text}
                {share !== '' && <small>{share}</small>}
              </span>
            </button>
          )
        })}
      </div>
    </section>
  )
}

const TICKS = [0, 0.25, 0.5, 0.75, 1]

function unplacedNote(count: number): string {
  const subject = count === 1 ? '1 job has' : `${count} jobs have`
  return `${subject} no place on the timeline, for example after an interruption or in an older run.`
}

function Timeline({ analytics, data, sinceAsOfMs }: UsageChartsProps): ReactElement | null {
  const charts = data.charts
  if (charts === null) return null
  const { timeline } = charts
  const modelOf = (key: string): { name: string; ordinal: number | null } => {
    const model = data.catalog.models.find((m) => m.key === key)
    return { name: model?.model ?? 'Unknown model', ordinal: model?.colorOrdinal ?? null }
  }
  const agentTitle = (id: string): string => data.catalog.agents.find((agent) => agent.id === id)?.title ?? id
  const durationOf = (interval: UsageInterval): number =>
    tickedDuration(interval.durationMs, interval.status === 'running', sinceAsOfMs) ?? interval.durationMs
  const start = timeline.rangeStartMs
  const end = Math.max(timeline.rangeEndMs, ...timeline.intervals.map((i) => i.runOffsetMs + durationOf(i)))
  const span = Math.max(1, end - start)

  const agentIds = [...new Set(timeline.intervals.map((interval) => interval.agentId))]
  const legend = [...new Set(timeline.intervals.map((interval) => interval.modelKey))]

  return (
    <section className="usage-chart usage-timeline" aria-label="Agent job timeline">
      <div className="usage-chart-heading">
        <h3>When agents worked</h3>
        <span>Elapsed active time · select a job</span>
      </div>
      {timeline.intervals.length === 0 ? (
        <p className="usage-caption">No timed jobs to place on the timeline yet.</p>
      ) : (
        <>
          <ul className="usage-legend" aria-label="Models">
            {legend.map((key) => {
              const { name, ordinal } = modelOf(key)
              return <li key={key}><ModelMark ordinal={ordinal} />{name}</li>
            })}
          </ul>
          <div className="usage-timeline-axis" aria-hidden="true">
            <span />
            <div>
              {TICKS.map((tick) => (
                <span key={tick} style={{ left: `${tick * 100}%` }} className={tick === 0 ? 'first' : tick === 1 ? 'last' : undefined}>
                  {formatTickMs(start + tick * span, span)}
                </span>
              ))}
            </div>
          </div>
          {agentIds.map((agentId) => (
            <div key={agentId} className="usage-timeline-row">
              <span className="usage-timeline-label" title={agentTitle(agentId)}>{agentTitle(agentId)}</span>
              <div className="usage-timeline-track">
                {TICKS.map((tick) => <i key={tick} className="usage-timeline-grid" style={{ left: `${tick * 100}%` }} />)}
                {timeline.intervals
                  .filter((interval) => interval.agentId === agentId)
                  .map((interval) => {
                    const { name, ordinal } = modelOf(interval.modelKey)
                    const description = `${interval.title}, ${name}, starts ${formatOffset(interval.runOffsetMs)}, duration ${formatDurationMs(durationOf(interval))}`
                    return (
                      <button
                        key={interval.jobId}
                        type="button"
                        className="usage-timeline-job"
                        style={{
                          ...modelColorStyle(ordinal),
                          left: `${((interval.runOffsetMs - start) / span) * 100}%`,
                          width: `${(durationOf(interval) / span) * 100}%`,
                        }}
                        title={description}
                        aria-label={`${description}. View job.`}
                        onClick={() => analytics.focusJob(interval.jobId)}
                      />
                    )
                  })}
              </div>
            </div>
          ))}
          <p className="usage-caption">Each bar is one agent job. Overlapping rows show parallel work.</p>
        </>
      )}
      {timeline.unplaced > 0 && <p className="usage-caption">{unplacedNote(timeline.unplaced)}</p>}
      {timeline.truncated && <p className="usage-caption">{`Showing ${timeline.limit} of ${timeline.total} jobs.`}</p>}
    </section>
  )
}

export function UsageCharts(props: UsageChartsProps): ReactElement {
  return (
    <>
      <Comparison {...props} />
      <Timeline {...props} />
    </>
  )
}
