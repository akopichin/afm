import { useEffect, useId, useRef, useState, type ReactElement } from 'react'
import type { AccountingState, CostSummary, CoverageIssue } from '../../types/cost'
import { costReason, summarizeCoverageGroups } from '../cost-panel/coverage-summary'

// costReason/summarizeCoverageGroups живут в components/cost-panel/coverage-summary
// (Task 11) — и тайл Est. cost здесь, и CostPanel строят строку покрытия по одним
// и тем же группам issues; ре-экспортируем их отсюда же, чтобы не ломать
// существующие импорты (`import { costReason, summarizeCoverageGroups } from
// './RunMetrics'` в RunMetrics.test.tsx).
export { costReason, summarizeCoverageGroups }

type RunMetricsProps = {
  startedAt: string
  elapsedMs: number
  // Idle — суммарное время, когда хоть одна стадия ждала пользователя. Backoff —
  // суммарное время авто-retry-backoff. Оба приходят готовыми из
  // useElapsed/useIdleMs/useBackoffMs (тик и offline-freeze — забота хуков, см.
  // App.tsx); RunMetrics только форматирует, поведение аккумуляторов не меняет.
  idleMs: number
  backoffMs: number
  // Пятая метрика — Est. cost (Increment 2). runCost отсутствует, пока
  // accounting не поддержан/нет данных — тогда тайл показывает плоский «—»
  // без маркера. coverageIssues/accounting приходят готовыми из FlowStatus
  // (см. types/cost.ts) — RunMetrics не считает деньги, только форматирует и
  // решает, показывать ли amber-маркер. onOpenCost — переход на вкладку Cost.
  runCost?: CostSummary
  coverageIssues: CoverageIssue[]
  accounting: AccountingState
  // fromPopover сообщает вызывающему (App.tsx), что активация пришла из
  // поповера «⋯» — только тогда финальный фокус нужно увести дальше, на саму
  // вкладку Cost воркспейса (round-5 #6 спеки, FIX 3): здесь, внутри
  // RunMetrics, фокус лишь стабилизируется на «⋯» (кнопка никуда не денется),
  // а окончательный перенос на Cost-таб — забота App.tsx, который один знает
  // о существовании WorkspaceTabs.
  onOpenCost: (fromPopover: boolean) => void
}

type Metric = { key: string; label: string; value: string; icon: ReactElement }

// hasCostMarker — единственное место, где решается «показывать amber-маркер
// или нет» (round-4 #6 спеки): пробел покрытия ИЛИ недоступность хранилища.
// hasData сюда сознательно не входит — health/coverage уже сами по себе
// достаточны, а «нет данных» — это отдельный, не тревожный, случай (плоский
// «—» без маркера).
function hasCostMarker(coverageIssues: CoverageIssue[], accounting: AccountingState): boolean {
  return coverageIssues.length > 0 || (accounting.supported && accounting.health === 'unavailable')
}

// RunMetrics — пять метрик прогона (Started/Elapsed/Idle/Backoff/Est. cost) в
// центре компактной шапки. Форматирование Started/Elapsed/Idle/Backoff
// перенесено 1:1 из старого Footer (тот же formatClock/formatDuration), но
// презентация — иконка+лейбл+значение с tabular-nums, чтобы значение не
// «прыгало» каждую секунду. id started-at/elapsed/idle/backoff сохранены для
// совместимости с существующими проверками. Est. cost — единственная метрика,
// которая всегда <button> (переход на вкладку Cost), см. renderCostMetric.
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
    { key: 'started', label: 'Started', value: formatClock(startedAt), icon: iconPlay() },
    { key: 'elapsed', label: 'Elapsed', value: hasStarted ? formatDuration(elapsedMs) : '--', icon: iconTimer() },
    { key: 'idle', label: 'Idle', value: hasStarted ? formatDuration(idleMs) : '--', icon: iconPause() },
    { key: 'backoff', label: 'Backoff', value: hasStarted ? formatDuration(backoffMs) : '--', icon: iconPulse() },
  ]

  // Est. cost скрыт целиком, когда accounting не поддержан бэкендом
  // (accounting.enabled: false / AFM_ACCOUNTING=0 → /api/status без объекта
  // accounting → supported:false). Тогда ни тайла, ни маркера, ни поповер-копии.
  const showCost = accounting.supported
  const costMarker = hasCostMarker(coverageIssues, accounting)
  const costReasonText = costMarker ? costReason(coverageIssues, accounting) : ''
  const costValue = runCost?.displayCost ?? '—'
  // Один id на оба рендера тайла (инлайн + поповер) — twin render не должен
  // плодить дубликаты id в DOM (round-5 #6 спеки). useId() уникален на
  // экземпляр RunMetrics, так что двух RunMetrics на странице тоже не столкнёт.
  const costReasonId = useId()

  // Поповер «⋯» — на узкой шапке (<1280px, CSS прячет часть метрик инлайн)
  // раскрывает все пять метрик с подписями, чтобы вторичные не пропадали
  // молча. На десктопе кнопка скрыта CSS-ом. Закрытие — клик вне / Escape /
  // активация Est. cost изнутри поповера (см. handleCostActivate).
  const [moreOpen, setMoreOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)
  const moreButtonRef = useRef<HTMLButtonElement>(null)

  // Finding #3 раунда 4: на desktop-брейкпоинте (>=1280px) все метрики видны
  // инлайн, а кнопка «⋯» скрыта CSS-ом. Если поповер был открыт на узкой шапке
  // и окно расширили, moreOpen оставался true и поповер продолжал висеть без
  // видимой управляющей кнопки. Закрываем состояние по matchMedia при переходе
  // на desktop (тот же порог, что и в run-metrics.css).
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

  // Активация Est. cost из поповера убирает саму нажатую кнопку из DOM (поповер
  // закрывается) — если не увести фокус явно, браузер уронит его на <body>
  // (round-5 #6 спеки: "focus explicitly moved to a stable Cost target").
  // Возвращаем фокус на «⋯»-кнопку — она никуда не денется; дальнейшее
  // перемещение фокуса на саму вкладку Cost — забота onOpenCost (App.tsx).
  // Инлайновая активация ничего дополнительно не делает — та кнопка и так
  // остаётся на месте.
  function handleCostActivate(fromPopover: boolean): void {
    if (fromPopover) {
      setMoreOpen(false)
      moreButtonRef.current?.focus()
    }
    onOpenCost(fromPopover)
  }

  // withId=true только для инлайновых метрик (id started-at/elapsed/idle/backoff
  // — на них завязаны существующие проверки); копии в поповере id НЕ несут,
  // иначе в DOM оказалось бы два элемента с одним id.
  function renderMetric(m: Metric, withId: boolean): ReactElement {
    return (
      <div className="metric" data-metric={m.key} key={m.key}>
        <span className="metric-icon" aria-hidden="true">{m.icon}</span>
        <span className="metric-text">
          <span className="metric-label">{m.label}</span>
          <span id={withId ? (m.key === 'started' ? 'started-at' : m.key) : undefined} className="metric-value">{m.value}</span>
        </span>
      </div>
    )
  }

  // Est. cost — единственная метрика-кнопка (destination = вкладка Cost).
  // Маркер — отдельный элемент, а не часть metric-label, чтобы не пропадать
  // вместе с лейблом на узкой шапке (<1280px, run-metrics.css прячет
  // .metric-label). Tooltip (FIX 4, codex#4): раньше причина была доступна
  // только через нативный title — тот показывается исключительно по mouse
  // hover, клавиатурный фокус его не раскрывает. data-tooltip + CSS-псевдоэлемент
  // (run-metrics.css, :hover/:focus-visible) делают ту же причину видимой и по
  // Tab-фокусу, без второй интерактивности — тайл остаётся кнопкой навигации.
  // aria-describedby остаётся отдельно — SR не зависит от CSS-видимости тултипа.
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
        <span className="metric-icon" aria-hidden="true">{iconCoin()}</span>
        <span className="metric-text">
          <span className="metric-label">Est. cost</span>
          <span id={fromPopover ? undefined : 'cost'} className="metric-value">{costValue}</span>
        </span>
      </button>
    )
  }

  return (
    <div className="run-metrics" role="group" aria-label="Run metrics" ref={rootRef}>
      {metrics.map((m) => renderMetric(m, true))}
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

// Иконки — инлайновый SVG на currentColor (наследует --text-secondary/--accent),
// stroke-based, 16×16, чтобы совпадать с остальным набором в шапке.
function svg(children: ReactElement): ReactElement {
  return (
    <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
      {children}
    </svg>
  )
}
function iconPlay(): ReactElement {
  return svg(<><circle cx="12" cy="12" r="9" /><path d="M10 8.5 L16 12 L10 15.5 Z" fill="currentColor" stroke="none" /></>)
}
function iconTimer(): ReactElement {
  return svg(<><circle cx="12" cy="13" r="8" /><path d="M12 13 V8.5" /><path d="M9 2.5 h6" /></>)
}
function iconPause(): ReactElement {
  return svg(<><circle cx="12" cy="12" r="9" /><path d="M10 9 V15 M14 9 V15" /></>)
}
function iconPulse(): ReactElement {
  return svg(<path d="M3 12 h4 l2 -5 l3 10 l2 -5 h7" />)
}
function iconCoin(): ReactElement {
  return svg(<><circle cx="12" cy="12" r="9" /><path d="M12 7 V17 M9.5 9.3 a2.5 1.6 0 0 1 5 0 c0 2 -5 1.4 -5 3.4 a2.5 1.6 0 0 0 5 0" /></>)
}
