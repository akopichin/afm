import { Fragment, useId, useState, type ReactElement } from 'react'
import type { Stage } from '../../types'
import type { AccountingState, CostSummary, CoverageIssue } from '../../types/cost'
import { formatStageDuration, type StageTimes, type StageTimeView } from '../../lib/stage-timing'
import { summarizeCoverageGroups } from './coverage-summary'

export type CostPanelProps = {
  stages: Stage[]
  times: StageTimes
  elapsedMs: number
  runCost?: CostSummary
  runOverheadCost?: CostSummary
  coverageIssues: CoverageIssue[]
  accounting: AccountingState
}

type CostRow = {
  key: string
  label: string
  cost?: CostSummary
  time: StageTimeView | null
  incomplete: boolean
  overhead?: boolean
}

function formatTokens(n: number): string {
  return n.toLocaleString('en-US')
}

function formatCacheHit(ratio: number | null): string {
  return ratio === null ? '—' : `${Math.round(ratio * 100)}%`
}

export function CostPanel({ stages, times, elapsedMs, runCost, runOverheadCost, coverageIssues, accounting }: CostPanelProps): ReactElement {
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const idBase = useId()
  const usage = accounting.supported && accounting.hasData
  const money = usage && accounting.showMoney
  const unavailable = accounting.supported && accounting.health === 'unavailable'
  const detailColSpan = 2 + Number(usage) + Number(money)

  function toggleExpand(key: string): void {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  const totalOrUnknown = (seconds: number): number | null => (times.totalsKnown ? seconds : null)

  const rows: CostRow[] = stages.map((stage) => ({
    key: `stage:${stage.id}`, label: stage.name || stage.id,
    cost: usage ? stage.cost : undefined,
    time: times.byStage.get(stage.id) ?? null,
    incomplete: times.incompleteIds.has(stage.id),
  }))
  if (usage && runOverheadCost !== undefined) {
    rows.push({ key: 'overhead', label: 'Run overhead', cost: runOverheadCost, time: null, incomplete: false, overhead: true })
  }

  function renderRow(row: CostRow, index: number): ReactElement {
    const expandable = row.time !== null || row.cost !== undefined
    const isExpanded = expandable && expanded.has(row.key)
    // Position-derived DOM IDs also work for stage IDs containing spaces.
    const regionId = `${idBase}-region-${index}`
    const labelId = `${idBase}-label-${index}`
    return (
      <Fragment key={row.key}>
        <tr className={`cost-row${row.overhead ? ' cost-row-overhead' : ''}`}>
          <td className="cost-col-stage">
            {expandable ? (
              <button type="button" className="cost-expand-btn" aria-expanded={isExpanded}
                aria-controls={regionId} onClick={() => toggleExpand(row.key)}>
                <span className={`cost-chevron${isExpanded ? ' open' : ''}`} aria-hidden="true">▸</span>
                <span id={labelId} className="cost-row-label">{row.label}</span>
              </button>
            ) : <span id={labelId} className="cost-row-label cost-row-static">{row.label}</span>}
            {row.cost !== undefined && row.cost.models.length > 0 && <span className="cost-row-meta">{row.cost.models.join(', ')}</span>}
            {row.incomplete && <span className="cost-row-meta">Incomplete time</span>}
          </td>
          <td className="cost-col-time">{formatStageDuration(row.time?.timeSec ?? null)}</td>
          {usage && <td className="cost-col-tokens">{row.cost ? formatTokens(row.cost.totalTokens) : '—'}</td>}
          {money && <td className="cost-col-cost">{row.cost?.displayCost ?? '—'}</td>}
        </tr>
        {isExpanded && (
          <tr className="cost-detail-row">
            <td colSpan={detailColSpan}>
              <div id={regionId} role="region" aria-labelledby={labelId} className="cost-detail">
                {row.time !== null && <TimeBreakdown time={row.time} />}
                {row.cost !== undefined && <><TokenMixBar cost={row.cost} /><FigureGrid cost={row.cost} /></>}
              </div>
            </td>
          </tr>
        )}
      </Fragment>
    )
  }

  return (
    <div className="cost-panel">
      {unavailable && usage && (
        <div className="cost-panel-banner" role="status">
          Cost accounting storage is unavailable right now — totals shown below may be incomplete.
        </div>
      )}
      <div className="cost-panel-scroll">
        <header className="cost-panel-head">
          <div className="cost-panel-titles">
            <h2 className="cost-panel-title">{usage ? 'Cost, usage & time' : 'Stage time'}</h2>
            <p className="cost-panel-subtitle">
              {money ? 'Time, token usage and estimated cost by stage' : usage ? 'Time and token usage by stage' : 'Time by stage'}
            </p>
          </div>
          {usage && <CoverageBadge issues={coverageIssues} unavailable={unavailable} />}
        </header>
        <dl className="cost-time-summary">
          <TimeFigure label="Run elapsed" seconds={elapsedMs / 1000} />
          <TimeFigure label="Stage work" seconds={totalOrUnknown(times.totals.workSec)} />
          <TimeFigure label="Stage waiting" seconds={totalOrUnknown(times.totals.waitingSec)} />
          <TimeFigure label="Stage paused" seconds={totalOrUnknown(times.totals.pausedSec)} />
        </dl>
        <p className="cost-time-note">Run elapsed is the run’s duration. Stage work, waiting and pauses are added across stages and can overlap when stages run in parallel.</p>
        {times.partial && <p className="cost-time-incomplete">Time totals are incomplete: stages with incomplete timing are excluded.</p>}
        <div className="cost-table-wrap">
          <table className={`cost-table${usage ? ' cost-table-usage' : ' cost-table-time-only'}${money ? ' cost-table-money' : ''}`}>
            <colgroup>
              <col className="cost-width-stage" /><col className="cost-width-time" />
              {usage && <col className="cost-width-tokens" />}{money && <col className="cost-width-money" />}
            </colgroup>
            <thead>
              <tr>
                <th scope="col" className="cost-col-stage">Stage</th>
                <th scope="col" className="cost-col-time">Time</th>
                {usage && <th scope="col" className="cost-col-tokens">Tokens</th>}
                {money && <th scope="col" className="cost-col-cost">Est. cost</th>}
              </tr>
            </thead>
            <tbody>
              {rows.map(renderRow)}
              <tr className="cost-row cost-row-total">
                <td className="cost-col-stage">{usage ? 'Total' : 'Stage total'}</td>
                <td className="cost-col-time">
                  {formatStageDuration(totalOrUnknown(times.totals.timeSec))}
                  {usage && <span className="cost-total-caption">Stage total</span>}
                </td>
                {usage && <td className="cost-col-tokens">{runCost !== undefined ? formatTokens(runCost.totalTokens) : '—'}</td>}
                {money && <td className="cost-col-cost">{runCost?.displayCost ?? '—'}</td>}
              </tr>
            </tbody>
          </table>
        </div>
        <p className="cost-time-note cost-total-note">
          Stage total is the sum of stage times and can exceed Run elapsed when stages run in parallel.
          {usage && (money ? ' Tokens and estimated cost cover the whole run, including Run overhead.' : ' Tokens cover the whole run, including Run overhead.')}
        </p>
        {!usage && <p className="empty-hint">{unavailable ? 'Cost unavailable this run' : 'No usage data'}</p>}
        {usage && runCost !== undefined && <UsageBreakdown cost={runCost} />}
      </div>
    </div>
  )
}

function TimeFigure({ label, seconds, kind }: { label: string; seconds: number | null; kind?: string }): ReactElement {
  return <div><dt>{kind && <span className={`cost-time-swatch ${kind}`} aria-hidden="true" />}{label}</dt><dd>{formatStageDuration(seconds)}</dd></div>
}

function TimeBreakdown({ time }: { time: StageTimeView }): ReactElement {
  const parts = [
    { label: 'Working', seconds: time.workSec, kind: 'work' },
    { label: 'Waiting', seconds: time.waitingSec, kind: 'waiting' },
    { label: 'Paused', seconds: time.pausedSec, kind: 'paused' },
  ]
  return (
    <section className="cost-time-detail">
      <div className="cost-time-title"><h3>Time breakdown</h3><span>{formatStageDuration(time.timeSec)} total</span></div>
      <div className="cost-time-bar" aria-hidden="true">
        {parts.filter((part) => part.seconds > 0).map((part) => (
          <span key={part.kind} className={`cost-time-segment ${part.kind}`} style={{ width: `${part.seconds / time.timeSec * 100}%` }} />
        ))}
      </div>
      <dl className="cost-time-legend">{parts.map((part) => <TimeFigure key={part.kind} {...part} />)}</dl>
      <div className="cost-time-waiting">
        <span>Waiting includes</span>
        <dl>
          <TimeFigure label="Approval & replies" seconds={time.approvalSec} />
          <TimeFigure label="Helpers & colleagues" seconds={time.helpersSec} />
          <TimeFigure label="Backoff & usage limits" seconds={time.backoffSec} />
          {time.schedulingSec > 0 && <TimeFigure label="Scheduling" seconds={time.schedulingSec} />}
        </dl>
      </div>
      <p className="cost-time-note">From the first start, including retries, waiting and pauses. Time queued before starting is excluded.</p>
    </section>
  )
}

// CoverageBadge — «Full coverage», когда пробелов нет; иначе компактный бейдж-раскрывашка с полным списком
// пробелов (длинный текст не должен занимать экран). При недоступном хранилище бейдж молчит: об этом
// уже говорит баннер.
function CoverageBadge({ issues, unavailable }: { issues: CoverageIssue[]; unavailable: boolean }): ReactElement | null {
  if (issues.length > 0) {
    return (
      <details className="cost-coverage">
        <summary className="cost-badge cost-badge-warn">
          Partial coverage · {issues.length} {issues.length === 1 ? 'gap' : 'gaps'}
        </summary>
        <p className="cost-coverage-line">Coverage gaps: {summarizeCoverageGroups(issues)}.</p>
      </details>
    )
  }
  if (unavailable) return null
  return <span className="cost-badge cost-badge-ok">Full coverage</span>
}

// UsageBreakdown — токены всего рана по типам, из тех же полей CostSummary, что и строки таблицы.
function UsageBreakdown({ cost }: { cost: CostSummary }): ReactElement {
  const items = [
    { label: 'Input', value: cost.uncachedInput },
    { label: 'Cache read', value: cost.cacheRead },
    { label: 'Cache write', value: cost.cacheWriteTotal },
    { label: 'Output', value: cost.output },
  ]
  return (
    <div className="cost-usage">
      <h3 className="cost-usage-title">Usage breakdown</h3>
      <dl className="cost-usage-list">
        {items.map((item) => (
          <div key={item.label} className="cost-usage-row">
            <dt>{item.label}</dt>
            <dd>{formatTokens(item.value)} tokens</dd>
          </div>
        ))}
      </dl>
    </div>
  )
}

// TokenMixBar — сегментированная полоса токенов одной строки: доли
// cacheRead/cacheWrite/output/uncachedInput от totalTokens.
function TokenMixBar({ cost }: { cost: CostSummary }): ReactElement {
  const total = cost.totalTokens
  if (total <= 0) {
    return <div className="cost-tokenmix cost-tokenmix-empty" role="img" aria-label="No token data available" />
  }

  const segments = [
    { key: 'cache-read', label: 'Cache read', value: cost.cacheRead, className: 'cost-seg-cacheread' },
    { key: 'cache-write', label: 'Cache write', value: cost.cacheWriteTotal, className: 'cost-seg-cachewrite' },
    { key: 'output', label: 'Output', value: cost.output, className: 'cost-seg-output' },
    { key: 'uncached', label: 'Uncached input', value: cost.uncachedInput, className: 'cost-seg-uncached' },
  ]

  return (
    <div className="cost-tokenmix" role="img" aria-label="Token mix breakdown">
      {segments.map((seg) => (
        <span
          key={seg.key}
          className={`cost-tokenmix-seg ${seg.className}`}
          style={{ width: `${(seg.value / total) * 100}%` }}
          title={`${seg.label}: ${formatTokens(seg.value)}`}
        />
      ))}
    </div>
  )
}

// FigureGrid — детальная сетка цифр под баром: разбивка cache write по TTL, output, uncached input,
// cache-hit ratio и phases×count.
function FigureGrid({ cost }: { cost: CostSummary }): ReactElement {
  const phaseEntries = Object.entries(cost.phases)
  return (
    <dl className="cost-figures">
      <FigureItem label="Cache read" value={formatTokens(cost.cacheRead)} />
      <FigureItem label="Cache write (5m)" value={formatTokens(cost.cacheWrite5m)} />
      <FigureItem label="Cache write (1h)" value={formatTokens(cost.cacheWrite1h)} />
      <FigureItem label="Cache write (other)" value={formatTokens(cost.cacheWriteOther)} />
      <FigureItem label="Output" value={formatTokens(cost.output)} />
      <FigureItem label="Uncached input" value={formatTokens(cost.uncachedInput)} />
      <FigureItem label="Cache hit" value={formatCacheHit(cost.cacheHitRatio)} />
      {phaseEntries.map(([phase, count]) => (
        <FigureItem key={phase} label={phase} value={`×${count}`} />
      ))}
    </dl>
  )
}

function FigureItem({ label, value }: { label: string; value: string }): ReactElement {
  return (
    <div className="cost-figure">
      <dt>{label}</dt>
      <dd>{value}</dd>
    </div>
  )
}
