import { Fragment, useId, useState, type ReactElement } from 'react'
import type { Stage } from '../../types'
import type { AccountingState, CostSummary, CoverageIssue } from '../../types/cost'
import { summarizeCoverageGroups } from './coverage-summary'

export type CostPanelProps = {
  // stages — полный список стадий флоу; панель сама отбирает только те, у
  // которых есть cost (см. StageView.Cost/omitempty — стадия без записи об
  // использовании ничего не потратила, показывать для неё пустую строку
  // с прочерками незачем).
  stages: Stage[]
  runCost?: CostSummary
  runOverheadCost?: CostSummary
  coverageIssues: CoverageIssue[]
  accounting: AccountingState
}

// RowKey — дискриминированный ключ раскрываемой строки: стадия ИЛИ
// run-overhead. Раздельные варианты (а не общий string id), потому что
// стадия может быть буквально названа `__overhead__` — сравнение по голому
// id столкнуло бы её состояние раскрытия с настоящей overhead-строкой.
type RowKey = { kind: 'stage'; id: string } | { kind: 'overhead' }

function rowKeyToString(key: RowKey): string {
  return key.kind === 'stage' ? `stage:${key.id}` : 'overhead'
}

type CostRow = { key: RowKey; label: string; cost: CostSummary }

// hasAccountingData — единственное место, где считается, «есть ли вообще
// данные учёта затрат» (см. round-6 #2 спеки: has_data == len(Records())>0
// на бэкенде). На старом бэкенде (accounting.supported===false) данных по
// определению нет — сюда включаем и этот случай, чтобы порядок пустых
// состояний ниже был единой веткой if/else, а не отдельным условием на
// unsupported.
function hasAccountingData(accounting: AccountingState): boolean {
  return accounting.supported && accounting.hasData
}

function formatTokens(n: number): string {
  return n.toLocaleString('en-US')
}

function formatCacheHit(ratio: number | null): string {
  return ratio === null ? '—' : `${Math.round(ratio * 100)}%`
}

// CostPanel — постоянная вкладка «Cost»: таблица затрат по стадиям + run
// overhead + Total, с раскрываемой по клику детализацией (token-mix бар +
// сетка цифр) для каждой строки. Порядок пустых состояний и структура
// колонок/детализации — см. task-11-brief.md / docs/superpowers/specs/
// 2026-09-15-cost-ui-increment2-design.md.
export function CostPanel({ stages, runCost, runOverheadCost, coverageIssues, accounting }: CostPanelProps): ReactElement {
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const idBase = useId()

  function toggleExpand(keyStr: string): void {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(keyStr)) next.delete(keyStr)
      else next.add(keyStr)
      return next
    })
  }

  const hasData = hasAccountingData(accounting)
  const unavailable = accounting.supported && accounting.health === 'unavailable'

  // Порядок пустых состояний ТОЧНО в этом порядке (round-4 #5 спеки):
  //   1. unavailable && !hasData → "Cost unavailable this run" (хранилище
  //      реально сломано, и снапшота показать нечего).
  //   2. !hasData (неподдержано, либо ok+нет данных) → "No usage data" —
  //      нейтральный случай, не тревожный.
  //   3. данные есть → таблица, плюс баннер неполноты, если unavailable.
  if (unavailable && !hasData) {
    return (
      <div className="cost-panel cost-panel-empty">
        <p className="empty-hint">Cost unavailable this run</p>
      </div>
    )
  }
  if (!hasData) {
    return (
      <div className="cost-panel cost-panel-empty">
        <p className="empty-hint">No usage data</p>
      </div>
    )
  }

  const stageRows: CostRow[] = stages
    .filter((s): s is Stage & { cost: CostSummary } => s.cost != null)
    .map((s) => ({ key: { kind: 'stage', id: s.id }, label: s.name !== '' ? s.name : s.id, cost: s.cost }))

  const overheadRow: CostRow | null =
    runOverheadCost != null ? { key: { kind: 'overhead' }, label: 'Run overhead', cost: runOverheadCost } : null

  const rows: CostRow[] = overheadRow !== null ? [...stageRows, overheadRow] : stageRows

  function renderRow(row: CostRow, index: number): ReactElement {
    const keyStr = rowKeyToString(row.key)
    const isExpanded = expanded.has(keyStr)
    // Ids for aria-controls/aria-labelledby are derived from the row's
    // POSITION, not from the raw stage id embedded in keyStr: stage ids
    // legally contain spaces (flow validation only forbids `/`, `\`, `.`/`..`,
    // NUL), and aria-controls/aria-labelledby are space-delimited IDREF-LIST
    // attributes — an id with a space is parsed as two broken references by
    // assistive tech. The `expanded` Set still keys off keyStr (discriminated,
    // collision-free by construction); only the DOM id needed the index
    // (same technique StagesList.tsx already uses for its cost figure ids).
    const domSuffix = row.key.kind === 'overhead' ? 'overhead' : String(index)
    const regionId = `${idBase}-region-${domSuffix}`
    const labelId = `${idBase}-label-${domSuffix}`
    const rowClassName = row.key.kind === 'overhead' ? 'cost-row cost-row-overhead' : 'cost-row'

    return (
      <Fragment key={keyStr}>
        <tr className={rowClassName}>
          <td className="cost-col-stage">
            <button
              type="button"
              className="cost-expand-btn"
              aria-expanded={isExpanded}
              aria-controls={regionId}
              onClick={() => toggleExpand(keyStr)}
            >
              <span className={`cost-chevron${isExpanded ? ' open' : ''}`} aria-hidden="true">▸</span>
              <span id={labelId} className="cost-row-label">{row.label}</span>
            </button>
          </td>
          <td className="cost-col-model">{row.cost.models.length > 0 ? row.cost.models.join(', ') : '—'}</td>
          <td className="cost-col-tokens">{formatTokens(row.cost.totalTokens)}</td>
          <td className="cost-col-cacherw">{formatTokens(row.cost.cacheRead)} / {formatTokens(row.cost.cacheWriteTotal)}</td>
          <td className="cost-col-cost">{row.cost.displayCost}</td>
        </tr>
        {isExpanded && (
          <tr className="cost-detail-row">
            <td colSpan={5}>
              <div id={regionId} role="region" aria-labelledby={labelId} className="cost-detail">
                <TokenMixBar cost={row.cost} />
                <FigureGrid cost={row.cost} />
              </div>
            </td>
          </tr>
        )}
      </Fragment>
    )
  }

  return (
    <div className="cost-panel">
      {unavailable && (
        <div className="cost-panel-banner" role="status">
          Cost accounting storage is unavailable right now — totals shown below may be incomplete.
        </div>
      )}
      <div className="cost-panel-scroll">
        <div className="cost-table-wrap">
          <table className="cost-table">
            <thead>
              <tr>
                <th scope="col" className="cost-col-stage">Stage</th>
                <th scope="col" className="cost-col-model">Model</th>
                <th scope="col" className="cost-col-tokens">Tokens</th>
                <th scope="col" className="cost-col-cacherw">Cache R/W</th>
                <th scope="col" className="cost-col-cost">Cost</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row, index) => renderRow(row, index))}
              <tr className="cost-row cost-row-total">
                <td className="cost-col-stage">Total</td>
                <td className="cost-col-model" />
                <td className="cost-col-tokens" />
                <td className="cost-col-cacherw" />
                <td className="cost-col-cost">{runCost?.displayCost ?? '—'}</td>
              </tr>
            </tbody>
          </table>
        </div>
        {coverageIssues.length > 0 && (
          <p className="cost-coverage-line">Coverage gaps: {summarizeCoverageGroups(coverageIssues)}.</p>
        )}
      </div>
    </div>
  )
}

// TokenMixBar — сегментированная полоса токенов одной строки: доли
// cacheRead/cacheWrite(total)/output/uncachedInput от totalTokens.
// totalTokens===0 → нейтральный пустой трек БЕЗ процентов и без деления на
// ноль (round-4 требование спеки) — вместо NaN%-сегментов рисуем один пустой
// прямоугольник.
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

// FigureGrid — детальная сетка цифр под баром: разбивка cache write по TTL
// (бар их суммирует в один сегмент, здесь — раздельно), output, uncached
// input, cache-hit ratio (null → «—», не «0%» — это разные факты, см.
// types/cost.ts) и phases×count.
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
