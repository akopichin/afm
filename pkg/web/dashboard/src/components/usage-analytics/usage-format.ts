import { formatStageDuration } from '../../lib/stage-timing'
import { formatTokensCompact } from '../../lib/format-tokens'
import type { UsageCoverageStatus, UsageMetric, UsageMetrics, UsageTokenCounts } from '../../types/usage-analytics'

// null — «неизвестно» и показывается тире; настоящий ноль остаётся нулём.
const UNKNOWN = '—'

export function formatCount(value: number | null): string {
  return value === null ? UNKNOWN : formatTokensCompact(value)
}

export function formatDurationMs(ms: number | null): string {
  return ms === null ? UNKNOWN : formatStageDuration(ms / 1000)
}

export function formatMoney(usd: number | null): string {
  if (usd === null) return UNKNOWN
  if (usd > 0 && usd < 0.01) return '<$0.01'
  return `$${usd.toFixed(2)}`
}

// Старт job — смещение на активной шкале рана, а не часы стены.
export function formatOffset(ms: number | null): string {
  return ms === null ? UNKNOWN : `+${formatStageDuration(ms / 1000)}`
}

export function formatShare(value: number | null, total: number | null): string {
  if (value === null || total === null || total <= 0) return ''
  return `${Math.round((value / total) * 100)}%`
}

// Метка оси времени: единица зависит от длины всей шкалы, чтобы соседние метки различались (у короткого рана
// «0m» на всех). Округление, а не floor: последняя метка показывает конец диапазона.
export function formatTickMs(ms: number, spanMs: number): string {
  if (spanMs < 2 * 60_000) return `${Math.round(ms / 1000)}s`
  if (spanMs < 10 * 60_000) {
    const seconds = Math.round(ms / 1000)
    return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`
  }
  const minutes = Math.round(ms / 60_000)
  if (minutes < 60) return `${minutes}m`
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`
}

export function cacheWriteTokens(counts: UsageTokenCounts): number {
  return counts.cacheWrite5m + counts.cacheWrite1h + counts.cacheWriteOther
}

export function metricValue(metrics: UsageMetrics, metric: UsageMetric): number | null {
  if (metric === 'agent_time') return metrics.agentTime.durationMs
  if (metric === 'tokens') return metrics.tokens?.counts?.total ?? null
  return metrics.money?.estimatedCostUsd ?? null
}

// Значение для сравнения: только время тикает, у running-строки оно растёт на время после as_of.
export function tickedMetricValue(metrics: UsageMetrics, metric: UsageMetric, sinceAsOfMs: number): number | null {
  if (metric !== 'agent_time') return metricValue(metrics, metric)
  return tickedDuration(metrics.agentTime.durationMs, metrics.activeJobs > 0, metrics.activeJobs * sinceAsOfMs)
}

// Длительность running-job на asOf плюс время, прошедшее после ответа; у законченного job и у неизвестной она не тикает.
export function tickedDuration(durationMs: number | null, running: boolean, sinceAsOfMs: number): number | null {
  return durationMs !== null && running ? durationMs + sinceAsOfMs : durationMs
}

const COVERAGE_SUBJECT = { time: 'time', tokens: 'tokens', cost: 'cost' } as const

export function coverageLabel(subject: keyof typeof COVERAGE_SUBJECT, status: UsageCoverageStatus): string | null {
  if (status === 'full') return null
  return status === 'partial' ? `Partial ${COVERAGE_SUBJECT[subject]}` : `${capitalize(COVERAGE_SUBJECT[subject])} unknown`
}

function capitalize(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1)
}

const REASONS: Record<string, string> = {
  interrupted_job: 'a job was interrupted, its duration is unknown',
  start_failed: 'some attempts never started and are not counted',
  unmetered_job: 'some jobs reported no usage',
  pending_usage: 'running jobs have not reported usage yet',
  unpriced_model: 'some models have no price',
  usage_store_unavailable: 'the usage journal is unavailable',
  usage_disabled: 'usage accounting is off',
  jobs_store_unavailable: 'the agent jobs journal is unavailable',
  active_jobs: 'jobs are still running',
  pending_start: 'a job is starting',
  legacy_journal: 'this run has no agent jobs journal',
  started_before_recording: 'work started before jobs were recorded',
}

const MONEY_REASON = /price|cost|money/

// Код причины с сервера — человеческая фраза; неизвестный код читается как есть. Ценовые причины скрыты вместе с деньгами.
export function reasonText(code: string, showMoney: boolean): string | null {
  if (!showMoney && MONEY_REASON.test(code)) return null
  return REASONS[code] ?? code.replaceAll('_', ' ')
}
