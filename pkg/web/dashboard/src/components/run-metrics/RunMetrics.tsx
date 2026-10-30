import { useEffect, useId, useRef, useState, type ReactElement } from 'react'
import type { AccountingState, CostSummary, CoverageIssue } from '../../types/cost'
import { costReason, summarizeCoverageGroups } from '../cost-panel/coverage-summary'
import { formatTokensCompact } from '../../lib/format-tokens'

// costReason/summarizeCoverageGroups живут в components/cost-panel/coverage-summary — и тайл Est.
export { costReason, summarizeCoverageGroups }

type RunMetricsProps = {
  startedAt: string
  elapsedMs: number
  // Idle — суммарное время, когда хоть одна стадия ждала пользователя. Backoff — суммарное время
  // авто-retry-backoff.
  idleMs: number
  backoffMs: number
  // Пятая метрика — Est. coverageIssues/accounting приходят готовыми из FlowStatus (см. types/cost.ts) —
  // RunMetrics не считает деньги, только форматирует и решает, показывать ли amber-маркер.
  runCost?: CostSummary
  coverageIssues: CoverageIssue[]
  accounting: AccountingState
  // fromPopover просит App перенести фокус с кнопки ⋯ на вкладку Cost после активации.
  onOpenCost: (fromPopover: boolean) => void
}

type Metric = { key: string; label: string; value: string }

// hasCostMarker — единственное место, где решается «показывать amber-маркер или нет»: пробел покрытия ИЛИ
// недоступность хранилища.
function hasCostMarker(coverageIssues: CoverageIssue[], accounting: AccountingState): boolean {
  return coverageIssues.length > 0 || (accounting.supported && accounting.health === 'unavailable')
}

// RunMetrics — пять метрик прогона (Started/Elapsed/Idle/Backoff/Est. cost) в центре компактной шапки.
export function RunMetrics({
  startedAt,
  elapsedMs,
  idleMs,
  backoffMs,
  runCost,
  coverageIssues,
  accounting,
  onOpenCost,
}: RunMetricsProps): ReactElement {
  const hasStarted = startedAt !== ''
  const metrics: Metric[] = [
    { key: 'started', label: 'Started', value: formatClock(startedAt) },
    { key: 'elapsed', label: 'Elapsed', value: hasStarted ? formatDuration(elapsedMs) : '--' },
    { key: 'idle', label: 'Idle', value: hasStarted ? formatDuration(idleMs) : '--' },
    { key: 'backoff', label: 'Backoff', value: hasStarted ? formatDuration(backoffMs) : '--' },
  ]

  // Тайл Est. tokens показываем, как только accounting поддержан бэкендом — токены видны всегда,
  // независимо от show_money.
  const showTokens = accounting.supported
  const showCost = accounting.supported && accounting.showMoney
  const costMarker = hasCostMarker(coverageIssues, accounting)
  const costReasonText = costMarker ? costReason(coverageIssues, accounting) : ''
  const costValue = runCost?.displayCost ?? '—'
  // Токены — компактный формат (1.2M/345K). Нет runCost (учёт ещё без данных) →
  // плоский «—», как у Est. cost.
  const tokensValue = runCost !== undefined ? formatTokensCompact(runCost.totalTokens) : '—'
  // Один id на оба рендера тайла — twin render не должен плодить дубликаты id в DOM. useId() уникален на
  // экземпляр RunMetrics, так что двух RunMetrics на странице тоже не столкнёт.
  const costReasonId = useId()

  // Поповер «⋯» — на узкой шапке раскрывает все пять метрик с подписями, чтобы вторичные не пропадали
  // молча. На десктопе кнопка скрыта CSS-ом.
  const [moreOpen, setMoreOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)
  const moreButtonRef = useRef<HTMLButtonElement>(null)

  // На desktop все метрики видны inline; кнопку ⋯ скрывает CSS.
  useEffect(() => {
    const mq = window.matchMedia('(min-width: 1280px)')
    const update = (): void => { if (mq.matches) setMoreOpen(false) }
    update()
    mq.addEventListener('change', update)
    return () => mq.removeEventListener('change', update)
  }, [])

  useEffect(() => {
    if (!moreOpen) return
    function onDocDown(e: MouseEvent): void {
      if (rootRef.current !== null && !rootRef.current.contains(e.target as Node)) setMoreOpen(false)
    }
    function onKey(e: KeyboardEvent): void {
      if (e.key === 'Escape') setMoreOpen(false)
    }
    document.addEventListener('mousedown', onDocDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDocDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [moreOpen])

  // Активация Est. Возвращаем фокус на «⋯»-кнопку — она никуда не денется; дальнейшее перемещение фокуса
  // на саму вкладку Cost — забота onOpenCost (App.tsx).
  function handleCostActivate(fromPopover: boolean): void {
    if (fromPopover) {
      setMoreOpen(false)
      moreButtonRef.current?.focus()
    }
    onOpenCost(fromPopover)
  }

  // withId=true только для инлайновых метрик; копии в поповере id НЕ несут, иначе в DOM оказалось бы два
  // элемента с одним id.
  function renderMetric(m: Metric, withId: boolean): ReactElement {
    return (
      <div className="metric" data-metric={m.key} key={m.key}>
        <span className="metric-label">{m.label}</span>
        <span id={withId ? (m.key === 'started' ? 'started-at' : m.key) : undefined} className="metric-value">{m.value}</span>
      </div>
    )
  }

  // Причина отображается на hover и keyboard focus; нативный title не поддерживает фокус.
  function renderCostMetric(fromPopover: boolean): ReactElement {
    return (
      <button
        type="button"
        className="metric metric-cost"
        data-metric="cost"
        key="cost"
        onClick={() => handleCostActivate(fromPopover)}
        aria-describedby={costMarker ? costReasonId : undefined}
        data-tooltip={costMarker ? costReasonText : undefined}
      >
        {costMarker && <span className="metric-marker" aria-hidden="true" />}
        <span className="metric-label">Est. cost</span>
        <span id={fromPopover ? undefined : 'cost'} className="metric-value">{costValue}{chevron()}</span>
      </button>
    )
  }

  // Est. tokens — как и Est.
  function renderTokensMetric(fromPopover: boolean): ReactElement {
    return (
      <button
        type="button"
        className="metric metric-tokens"
        data-metric="tokens"
        key="tokens"
        onClick={() => handleCostActivate(fromPopover)}
      >
        <span className="metric-label">Est. tokens</span>
        <span id={fromPopover ? undefined : 'est-tokens'} className="metric-value">{tokensValue}{chevron()}</span>
      </button>
    )
  }

  return (
    <div className="run-metrics" role="group" aria-label="Run metrics" ref={rootRef}>
      {metrics.map((m) => renderMetric(m, true))}
      {showTokens && renderTokensMetric(false)}
      {showCost && renderCostMetric(false)}
      <button
        type="button"
        className="metrics-more"
        aria-label="Show all run metrics"
        aria-expanded={moreOpen}
        onClick={() => setMoreOpen((open) => !open)}
        ref={moreButtonRef}
      >
        ⋯
      </button>
      {moreOpen && (
        <div className="metrics-popover" role="group" aria-label="All run metrics">
          {metrics.map((m) => renderMetric(m, false))}
          {showTokens && renderTokensMetric(true)}
          {showCost && renderCostMetric(true)}
        </div>
      )}
      {/* Общий скрытый узел с пояснением маркера — оба рендера тайла (инлайн +
          поповер) ссылаются на ОДИН и тот же id через aria-describedby, чтобы
          twin render не плодил дубликаты id в DOM. */}
      {costMarker && (
        <span id={costReasonId} className="metric-cost-reason">{costReasonText}</span>
      )}
    </div>
  )
}

function formatClock(startedAt: string): string {
  if (startedAt === '') return '--'
  const parsed = new Date(startedAt)
  if (Number.isNaN(parsed.getTime())) return '--'
  return [parsed.getHours(), parsed.getMinutes(), parsed.getSeconds()].map(pad).join(':')
}

function formatDuration(ms: number): string {
  const sec = Math.floor(ms / 1000)
  const hours = Math.floor(sec / 3600)
  const minutes = Math.floor((sec % 3600) / 60)
  const seconds = sec % 60
  if (hours > 0) return `${hours}:${pad(minutes)}:${pad(seconds)}`
  return `${pad(minutes)}:${pad(seconds)}`
}

function pad(value: number): string {
  return value < 10 ? `0${value}` : String(value)
}

// Шеврон справа от значения — подсказка, что плитка открывает вкладку Cost.
function chevron(): ReactElement {
  return (
    <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="m9 5 7 7-7 7" />
    </svg>
  )
}
