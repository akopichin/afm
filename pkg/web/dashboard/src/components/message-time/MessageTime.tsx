import type { ReactElement } from 'react'
import { formatHostTime, useMessageTimeZone } from '../../lib/message-time-zone'

export type MessageTimeProps = {
  timestamp: string | number
  // Already bucketed by the caller (for example, formatEventGap returns "7s").
  gap: string
  // Set only for a standalone label outside an interactive row.
  focusable?: boolean
  // A zero-second gap can also follow another event in the same second.
  firstEvent?: boolean
}

export function MessageTime({ timestamp, gap, focusable = false, firstEvent = false }: MessageTimeProps): ReactElement | null {
  const zone = useMessageTimeZone()
  const date = new Date(timestamp)
  if (!Number.isFinite(date.getTime())) return null

  const { clock, full } = formatHostTime(date, zone)
  const elapsed = firstEvent ? `first event (+${gap})` : `+${gap} after previous`
  const description = `${full} (host time) · ${elapsed}`

  return (
    <time className="message-time" dateTime={date.toISOString()} title={description} aria-label={description} tabIndex={focusable ? 0 : undefined}>
      <span className="message-time-clock" aria-hidden="true">{clock}</span>
      <span className="message-time-gap" aria-hidden="true">+{gap}</span>
    </time>
  )
}
