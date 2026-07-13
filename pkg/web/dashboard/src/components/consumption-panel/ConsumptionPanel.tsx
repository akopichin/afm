import { useEffect, useMemo, useState, type ReactElement } from 'react'
import type { Stage, UsageMetric, UsagePoint } from '../../types'
import { useUsageData } from '../../hooks/use-usage-data'

type ConsumptionPanelProps = {
  stages: Stage[]
}

type UsageColors = {
  mint: string
  amber: string
  inkDim: string
  grid: string
}

const CHART_WIDTH = 320
const CHART_HEIGHT = 180
const PAD_LEFT = 38
const PAD_RIGHT = 10
const PAD_TOP = 12
const PAD_BOTTOM = 24

// Панель потребления: свитч метрик tokens/cost/kb (cost скрывается при пустом пробном
// запросе), фильтр по стейджам, hand-rolled SVG-график. Поведение перенесено из
// loadUsage / probeUsageCost / renderUsageChart / format* в текущем app.js.
export function ConsumptionPanel({ stages }: ConsumptionPanelProps): ReactElement {
  const [open, setOpen] = useState(false)
  const [metric, setMetric] = useState<UsageMetric>('tokens')
  const [stageFilter, setStageFilter] = useState<string>('')

  const points = useUsageData(metric, stageFilter === '' ? null : stageFilter)
  const costPoints = useUsageData('cost', null)
  const costAvailable = costPoints.length > 0

  const colors = useMemo(readUsageColors, [])

  useEffect(() => {
    if (stageFilter !== '' && !stages.some((stage) => stage.id === stageFilter)) {
      setStageFilter('')
    }
  }, [stages, stageFilter])

  useEffect(() => {
    if (!costAvailable && metric === 'cost') {
      setMetric('tokens')
    }
  }, [costAvailable, metric])

  const total = formatUsageValue(sumValues(points), metric)
  const stageLabel = stageFilter !== '' ? stageFilter : 'all stages'

  return (
    <aside id="usage-panel" className={`usage-panel${open ? ' open' : ''}`}>
      <button
        id="usage-toggle"
        className="usage-toggle"
        type="button"
        title="Consumption panel"
        aria-label="Consumption panel"
        onClick={() => setOpen((prev) => !prev)}
      >
        <svg className="usage-toggle-arrow" viewBox="0 0 24 24" aria-hidden="true">
          <path
            d="M15 6 L9 12 L15 18"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      </button>

      <div className="usage-panel-body" aria-hidden={!open}>
        <header className="usage-panel-head">
          <h2>Consumption</h2>
          <span className="usage-total" id="usage-total">{total}</span>
        </header>

        <div className="usage-controls">
          <div className="usage-metric-switch" id="usage-metric-switch" role="group" aria-label="Metric">
            <button
              type="button"
              className={`usage-metric${metric === 'tokens' ? ' active' : ''}`}
              data-metric="tokens"
              onClick={() => setMetric('tokens')}
            >
              Tokens
            </button>
            <button
              type="button"
              className={`usage-metric${metric === 'cost' ? ' active' : ''}${costAvailable ? '' : ' hidden'}`}
              data-metric="cost"
              onClick={() => setMetric('cost')}
            >
              Cost
            </button>
            <button
              type="button"
              className={`usage-metric${metric === 'kb' ? ' active' : ''}`}
              data-metric="kb"
              onClick={() => setMetric('kb')}
            >
              KB
            </button>
          </div>

          <label className="usage-stage-filter" htmlFor="usage-stage-select">
            <span className="usage-stage-label">Stage</span>
            <select
              id="usage-stage-select"
              value={stageFilter}
              onChange={(event) => setStageFilter(event.target.value)}
            >
              <option value="">All stages</option>
              {stages.map((stage) => (
                <option key={stage.id} value={stage.id}>
                  {stage.id}
                </option>
              ))}
            </select>
          </label>
        </div>

        <div className="usage-chart-wrap">
          <svg
            id="usage-chart"
            className="usage-chart"
            viewBox={`0 0 ${CHART_WIDTH} ${CHART_HEIGHT}`}
            preserveAspectRatio="none"
            aria-hidden="true"
            dangerouslySetInnerHTML={{ __html: points.length > 0 ? buildChartSvg(points, colors, metric) : '' }}
          />
          <div className={`usage-empty${points.length > 0 ? ' hidden' : ''}`} id="usage-empty">
            No data
          </div>
        </div>

        <div className="usage-meta" id="usage-meta">
          <span>{`${points.length} points`}</span>
          <span>{stageLabel}</span>
        </div>
      </div>
    </aside>
  )
}

function readUsageColors(): UsageColors {
  const styles = getComputedStyle(document.documentElement)

  const read = (name: string, fallback: string): string => {
    const value = styles.getPropertyValue(name).trim()
    return value !== '' ? value : fallback
  }

  return {
    mint: read('--mint', '#6fd4cc'),
    amber: read('--amber', '#e5d442'),
    inkDim: read('--ink-dim', '#4a8a85'),
    grid: read('--usage-grid', 'rgba(111, 212, 204, 0.10)'),
  }
}

function buildChartSvg(points: UsagePoint[], colors: UsageColors, metric: UsageMetric): string {
  const plotWidth = CHART_WIDTH - PAD_LEFT - PAD_RIGHT
  const plotHeight = CHART_HEIGHT - PAD_TOP - PAD_BOTTOM

  const sorted = [...points].sort((a, b) => (a.timestamp < b.timestamp ? -1 : a.timestamp > b.timestamp ? 1 : 0))

  let max = 0
  for (const point of sorted) {
    if (point.value > max) max = point.value
  }
  if (max <= 0) max = 1

  const count = sorted.length

  const xAt = (index: number): number =>
    count <= 1 ? PAD_LEFT + plotWidth / 2 : PAD_LEFT + (plotWidth * index) / (count - 1)

  const yAt = (value: number): number => PAD_TOP + plotHeight * (1 - value / max)

  const ticks = 4
  let gridSvg = ''
  for (let g = 0; g <= ticks; g++) {
    const gridValue = (max * g) / ticks
    const gridY = yAt(gridValue)
    gridSvg += `<line x1="${PAD_LEFT}" y1="${gridY.toFixed(1)}" x2="${CHART_WIDTH - PAD_RIGHT}" y2="${gridY.toFixed(1)}" stroke="${colors.grid}" stroke-width="1"/>`
    gridSvg += `<text x="${PAD_LEFT - 6}" y="${(gridY + 3).toFixed(1)}" text-anchor="end" fill="${colors.inkDim}" font-size="8" font-family="inherit">${formatUsageAxis(gridValue, metric)}</text>`
  }

  const linePoints: string[] = []
  for (let k = 0; k < count; k++) {
    linePoints.push(`${xAt(k).toFixed(1)},${yAt(sorted[k]?.value ?? 0).toFixed(1)}`)
  }

  const baseY = (PAD_TOP + plotHeight).toFixed(1)
  const lineD = `M ${linePoints.join(' L ')}`
  const areaD = `M ${PAD_LEFT},${baseY} L ${linePoints.join(' L ')} L ${xAt(count - 1).toFixed(1)},${baseY} Z`

  let pointsSvg = ''
  const labelStep = Math.max(1, Math.ceil(count / 6))
  for (let p = 0; p < count; p++) {
    const point = sorted[p]
    if (point === undefined) continue

    const px = xAt(p)
    const py = yAt(point.value)

    pointsSvg += `<circle cx="${px.toFixed(1)}" cy="${py.toFixed(1)}" r="2.4" fill="${colors.amber}"><title>${escapeHtml(formatUsageBucket(point.timestamp))} · ${escapeHtml(formatUsageValue(point.value, metric))}</title></circle>`

    if (p % labelStep === 0 || p === count - 1) {
      pointsSvg += `<text x="${px.toFixed(1)}" y="${CHART_HEIGHT - 8}" text-anchor="middle" fill="${colors.inkDim}" font-size="8" font-family="inherit">${formatUsageBucket(point.timestamp)}</text>`
    }
  }

  return (
    '<defs><linearGradient id="usageAreaGrad" x1="0" y1="0" x2="0" y2="1">' +
    `<stop offset="0%" stop-color="${colors.mint}" stop-opacity="0.35"/>` +
    `<stop offset="100%" stop-color="${colors.mint}" stop-opacity="0.02"/>` +
    '</linearGradient></defs>' +
    gridSvg +
    `<path d="${areaD}" fill="url(#usageAreaGrad)" stroke="none"/>` +
    `<path d="${lineD}" fill="none" stroke="${colors.mint}" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round"/>` +
    pointsSvg
  )
}

function sumValues(points: UsagePoint[]): number {
  let sum = 0
  for (const point of points) {
    sum += point.value
  }
  return sum
}

function formatUsageValue(value: number, metric: UsageMetric): string {
  if (metric === 'cost') return formatUsageCost(value)
  if (metric === 'kb') return `${value.toFixed(1)} KB`

  return `${Math.round(value).toString().replace(/\B(?=(\d{3})+(?!\d))/g, ',')} tok`
}

function formatUsageCost(value: number): string {
  if (value === 0) return '$0'
  if (value < 0.01) return `$${value.toFixed(4)}`

  return `$${value.toFixed(2)}`
}

function formatUsageAxis(value: number, metric: UsageMetric): string {
  if (value === 0) return '0'
  if (metric === 'cost') return value < 0.01 ? value.toFixed(3) : value.toFixed(2)
  if (metric === 'kb') return Math.round(value).toString()
  if (value >= 1000) return `${(value / 1000).toFixed(value >= 10000 ? 0 : 1)}k`

  return Math.round(value).toString()
}

function formatUsageBucket(rfc3339: string): string {
  const match = /T(\d{2}:\d{2})/.exec(rfc3339)

  return match !== null ? match[1] ?? rfc3339 : rfc3339
}

function escapeHtml(value: string): string {
  return value.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}
