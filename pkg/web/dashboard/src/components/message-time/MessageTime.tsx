import type { ReactElement } from 'react'
import { useMessageTimeZone } from '../../lib/message-time-zone'

export type MessageTimeProps = {
  timestamp: string | number
  // Already bucketed by the caller (for example, formatEventGap returns "7s").
  gap: string
  // Set only for a standalone label outside an interactive row.
  focusable?: boolean
  // A zero-second gap can also follow another event in the same second.
  firstEvent?: boolean
}

function formatHostTime(date: Date, timeZone: string): { clock: string; full: string } {
  const options = { timeZone, hourCycle: 'h23', hour: '2-digit', minute: '2-digit' } as const
  return {
    clock: new Intl.DateTimeFormat('en-US', options).format(date),
    full: new Intl.DateTimeFormat('en-US', {
      ...options,
      month: 'short', day: 'numeric', year: 'numeric', second: '2-digit',
    }).format(date),
  }
}

// The IANA zone preserves historical DST; the offset is a fallback for unsupported zones.
export function displayTime(date: Date, timeZone: string, utcOffsetMinutes: number): { clock: string; full: string } {
  if (timeZone !== '') {
    try {
      return formatHostTime(date, timeZone)
    } catch (error) {
      if (!(error instanceof RangeError)) throw error
    }
  }
  return formatHostTime(new Date(date.getTime() + utcOffsetMinutes * 60_000), 'UTC')
}

export function MessageTime({ timestamp, gap, focusable = false, firstEvent = false }: MessageTimeProps): ReactElement | null {
  const { timeZone, utcOffsetMinutes } = useMessageTimeZone()
  const date = new Date(timestamp)
  if (!Number.isFinite(date.getTime())) return null

  const { clock, full } = displayTime(date, timeZone, utcOffsetMinutes)
  const elapsed = firstEvent ? `first event (+${gap})` : `+${gap} after previous`
  const description = `${full} (host time) · ${elapsed}`

  return (
    <time className="message-time" dateTime={date.toISOString()} title={description} aria-label={description} tabIndex={focusable ? 0 : undefined}>
      <span className="message-time-clock" aria-hidden="true">{clock}</span>
      <span className="message-time-gap" aria-hidden="true">+{gap}</span>
    </time>
  )
}
