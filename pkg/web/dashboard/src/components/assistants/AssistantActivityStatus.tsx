import { useEffect, useState, type ReactElement } from 'react'
import { ACTIVITY_TEXT_TOOL, type AssistantActivityEntry } from '../../types/assistant-activity'
import { formatHostTime, useMessageTimeZone } from '../../lib/message-time-zone'

const TICK_MS = 1000
// A connected, running helper with no new action for this long gets a "no new actions" note.
const SILENCE_NOTICE_MS = 3 * 60 * 1000

type AssistantActivityStatusProps = {
  last: AssistantActivityEntry | null
  connected: boolean
  // The last activity refresh failed; what is loaded stays on screen.
  refreshFailing: boolean
  // Only a running helper can be "quiet"; a paused or queued one is waiting by design.
  running: boolean
  terminal: boolean
  // A finished helper is not polled any more, so a failed refresh needs this to recover.
  onRetry: () => void
}

// formatSpan renders a duration as the biggest whole unit ("18s", "3m", "2h").
function formatSpan(ms: number): string {
  const seconds = Math.floor(ms / 1000)
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`
  return `${Math.floor(seconds / 3600)}h`
}

// useNow ticks only while `active`; a finished helper has a fixed "last action" and needs no timer.
function useNow(active: boolean): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    setNow(Date.now())
    const timer = setInterval(() => setNow(Date.now()), TICK_MS)
    return () => clearInterval(timer)
  }, [active])
  return now
}

// AssistantActivityStatus — the pinned "Last action: X · 18s ago" line plus the network notice. It owns its
// ticker so the per-second re-render never reaches the feed. Wall-clock silence is only claimed while the
// socket is up: without it the helper may be talking and we just cannot hear it.
export function AssistantActivityStatus({ last, connected, refreshFailing, running, terminal, onRetry }: AssistantActivityStatusProps): ReactElement | null {
  const now = useNow(!terminal)
  const zone = useMessageTimeZone()

  const notice = !terminal && !connected
    ? 'Live updates paused — reconnecting. Showing the activity loaded so far.'
    : refreshFailing
      ? terminal
        ? 'Could not refresh activity. Showing the activity loaded so far.'
        : 'Could not refresh activity — retrying. Showing the activity loaded so far.'
      : null

  const occurredAt = last === null ? NaN : Date.parse(last.occurredAt)
  const age = Number.isFinite(occurredAt) ? Math.max(0, now - occurredAt) : null
  const quiet = running && connected && !refreshFailing && age !== null && age >= SILENCE_NOTICE_MS

  if (last === null && notice === null) return null

  let lastLine: string | null = null
  if (last !== null) {
    const action = last.tool === ACTIVITY_TEXT_TOOL ? 'message' : last.tool
    const when = age === null ? null : terminal ? formatHostTime(new Date(occurredAt), zone).short : `${formatSpan(age)} ago`
    lastLine = when === null ? `Last action: ${action}` : `Last action: ${action} · ${when}`
  }

  return (
    <div className="assistant-activity-status">
      {lastLine !== null && <span className="assistant-activity-last">{lastLine}</span>}
      {quiet && age !== null && <span className="assistant-activity-quiet">No new actions for {formatSpan(age)}</span>}
      {notice !== null && (
        <div className="assistant-activity-notice" role="status">
          {notice}
          {terminal && refreshFailing && (
            <button type="button" className="assistant-activity-retry" onClick={onRetry}>Retry</button>
          )}
        </div>
      )}
    </div>
  )
}
