import { useEffect, useState, type ReactElement } from 'react'
import { formatHostTime, useMessageTimeZone, type MessageTimeZone } from '../../lib/message-time-zone'
import { quotaTitle, type StageQuota } from '../../types'

const TICK_INTERVAL_MS = 1000

type QuotaCountdownProps = {
  quota: StageQuota
  // live — свежесть /api/status (statusAvailable): пока ответ устарел, счётчик замирает, а не продолжает
  // отсчёт по часам браузера. Второго опроса здесь нет.
  live?: boolean
  // compact — бейдж для рейла стадий; иначе блок для панели деталей.
  compact?: boolean
}

// quotaTargetTime — момент, к которому относится счётчик: until (когда афм продолжит сам или проверит
// следующий раз). resetAt — лишь время сброса у провайдера, афм по нему не просыпается, поэтому целью не
// служит. null — афм сам не продолжит (пауза по pause_reason или нет времени) и счётчика нет.
function quotaTargetTime(quota: StageQuota): Date | null {
  if (quota.pauseReason !== '') return null
  const date = new Date(quota.until)
  return quota.until !== '' && Number.isFinite(date.getTime()) ? date : null
}

// quotaPausedReasonText — фактическая причина паузы из ожидания лимита (pause_reason сервера); subject — что
// стоит на паузе (стадия или задача помощника). Пустая причина — ручная пауза во время ожидания. Время сброса —
// в зоне хоста приложения.
export function quotaPausedReasonText(quota: StageQuota | undefined, zone: MessageTimeZone, subject: 'stage' | 'task'): string {
  const limit = quota === undefined ? 'usage limit' : `${quota.provider} usage limit`
  switch (quota?.pauseReason) {
    case 'exceeds_max_wait': {
      const resetAt = new Date(quota.resetAt)
      const when = quota.resetAt !== '' && Number.isFinite(resetAt.getTime()) ? ` at ${formatHostTime(resetAt, zone).short}` : ''
      return `The ${limit} resets${when} — later than the maximum wait, so the ${subject} was paused.`
    }
    case 'no_reset':
      return `The ${quota.provider} quota is exhausted and the provider gave no reset time, so the ${subject} was paused.`
    case 'max_waits':
      return `The ${limit} was hit several times in a row without progress, so the ${subject} was paused.`
    default:
      return `This ${subject} was manually paused while waiting for the ${limit} to reset.`
  }
}

function secondsLeft(target: Date): number {
  return Math.max(0, Math.ceil((target.getTime() - Date.now()) / 1000))
}

// useSecondsLeft тикает раз в секунду только пока live; после нуля интервал останавливается. Смена цели
// (повторный лимит поставил новый until) пересчитывает значение даже при замороженном счётчике.
function useSecondsLeft(target: Date | null, live: boolean): number | null {
  const targetMs = target?.getTime() ?? null
  const [seconds, setSeconds] = useState<number | null>(() => (target === null ? null : secondsLeft(target)))

  useEffect(() => {
    setSeconds(targetMs === null ? null : secondsLeft(new Date(targetMs)))
  }, [targetMs])

  useEffect(() => {
    if (!live || targetMs === null) return
    const deadline = new Date(targetMs)
    const tick = (): number => {
      const left = secondsLeft(deadline)
      setSeconds(left)
      return left
    }
    if (tick() === 0) return
    const timer = setInterval(() => {
      if (tick() === 0) clearInterval(timer)
    }, TICK_INTERVAL_MS)
    return () => clearInterval(timer)
  }, [targetMs, live])

  return seconds
}

function formatCountdown(totalSeconds: number): string {
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  if (hours > 0) return `${hours}h ${String(minutes).padStart(2, '0')}m`
  if (minutes > 0) return `${minutes}m ${String(seconds).padStart(2, '0')}s`
  return `${seconds}s`
}

// QuotaCountdown показывает ожидание сброса лимита: статичную подпись с абсолютным временем в зоне хоста
// приложения (<time dateTime>) и тикающий обратный отсчёт. Если лимит неожидаемый (pauseReason) — вместо
// отсчёта причина: афм сам не продолжит, нужен Continue человека. Отсчёт aria-hidden, чтобы скринридер не читал каждую
// секунду; доступное имя несёт статичный текст.
export function QuotaCountdown({ quota, live = true, compact = false }: QuotaCountdownProps): ReactElement {
  const zone = useMessageTimeZone()
  const target = quotaTargetTime(quota)
  const seconds = useSecondsLeft(target, live)

  const title = quotaTitle(quota)
  const detail = [quota.provider, quota.window].filter((part) => part !== '')
  // probe не знает времени сброса: это момент следующей проверки, а не обещание продолжить.
  const lead = quota.source === 'probe' ? 'Next check' : 'Resumes'
  const absolute = target === null ? null : formatHostTime(target, zone).short

  const countdown = seconds === null ? null : (
    <span className="quota-countdown" aria-hidden="true">{seconds === 0 ? 'Resuming…' : formatCountdown(seconds)}</span>
  )

  if (compact) {
    const staticText = absolute === null
      ? `${title} (${detail.join(', ')})`
      : `${title} (${detail.join(', ')}): ${lead.toLowerCase()} at ${absolute}`
    return (
      <span className="quota-badge" title={staticText}>
        <span aria-hidden="true">⏳</span>
        {countdown}
        {target === null
          ? <span className="quota-sr">{staticText}</span>
          : <time className="quota-sr" dateTime={target.toISOString()}>{staticText}</time>}
      </span>
    )
  }

  return (
    <div className="quota-wait" data-source={quota.source}>
      <div className="quota-wait-head">
        <span className="quota-title">{title}</span>
        <span className="quota-detail">{detail.join(' · ')}</span>
      </div>
      {quota.pauseReason !== '' && <div className="quota-wait-time">{quotaPausedReasonText(quota, zone, 'task')}</div>}
      {target !== null && absolute !== null && (
        <div className="quota-wait-time">
          <time dateTime={target.toISOString()}>{lead} at {absolute}</time>
          {countdown}
        </div>
      )}
    </div>
  )
}
