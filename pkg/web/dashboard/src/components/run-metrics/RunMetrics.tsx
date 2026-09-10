import { useEffect, useRef, useState, type ReactElement } from 'react'

type RunMetricsProps = {
  startedAt: string
  elapsedMs: number
  // Idle — суммарное время, когда хоть одна стадия ждала пользователя. Backoff —
  // суммарное время авто-retry-backoff. Оба приходят готовыми из
  // useElapsed/useIdleMs/useBackoffMs (тик и offline-freeze — забота хуков, см.
  // App.tsx); RunMetrics только форматирует, поведение аккумуляторов не меняет.
  idleMs: number
  backoffMs: number
}

type Metric = { key: string; label: string; value: string; icon: ReactElement }

// RunMetrics — четыре метрики прогона (Started/Elapsed/Idle/Backoff) в центре
// компактной шапки. Форматирование перенесено 1:1 из старого Footer (тот же
// formatClock/formatDuration), но презентация — иконка+лейбл+значение с
// tabular-nums, чтобы значение не «прыгало» каждую секунду. id started-at/
// elapsed/idle/backoff сохранены для совместимости с существующими проверками.
export function RunMetrics({ startedAt, elapsedMs, idleMs, backoffMs }: RunMetricsProps): ReactElement {
  const hasStarted = startedAt !== ''
  const metrics: Metric[] = [
    { key: 'started', label: 'Started', value: formatClock(startedAt), icon: iconPlay() },
    { key: 'elapsed', label: 'Elapsed', value: hasStarted ? formatDuration(elapsedMs) : '--', icon: iconTimer() },
    { key: 'idle', label: 'Idle', value: hasStarted ? formatDuration(idleMs) : '--', icon: iconPause() },
    { key: 'backoff', label: 'Backoff', value: hasStarted ? formatDuration(backoffMs) : '--', icon: iconPulse() },
  ]

  // Поповер «⋯» — на узкой шапке (<1000px, CSS прячет Idle/Backoff инлайн)
  // раскрывает все четыре метрики с подписями, чтобы вторичные не пропадали
  // молча. На десктопе кнопка скрыта CSS-ом. Закрытие — клик вне / Escape.
  const [moreOpen, setMoreOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)
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

  return (
    <div className="run-metrics" role="group" aria-label="Run metrics" ref={rootRef}>
      {metrics.map((m) => renderMetric(m, true))}
      <button
        type="button"
        className="metrics-more"
        aria-label="Show all run metrics"
        aria-expanded={moreOpen}
        onClick={() => setMoreOpen((open) => !open)}
      >
        ⋯
      </button>
      {moreOpen && (
        <div className="metrics-popover" role="group" aria-label="All run metrics">
          {metrics.map((m) => renderMetric(m, false))}
        </div>
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
