import { useEffect, useState, type ReactElement } from 'react'
import type { AfmEvent } from '../../types'
import { Maximizable } from '../layout/Maximizable'
import { PanelFrame } from '../panel-frame/PanelFrame'
import { useStickToBottom } from '../../hooks/use-stick-to-bottom'

type EventFeedPanelProps = {
  events: AfmEvent[]
}

type FeedLine = {
  msg: string
  msgClass: string
  statusClass: string
  entryClass: string
}

// Правая панель: лента событий флоу. Форматирование записей (текст и классы бейджа)
// совпадает с addFeedEntry в текущем app.js; относительное время обновляется каждые 5с
// (как setInterval refresh feed-time в app.js).
const RELATIVE_TIME_REFRESH_MS = 5000

export function EventFeedPanel({ events }: EventFeedPanelProps): ReactElement {
  // Тик каждые 5с — перевычисляет formatRelativeTime для всех записей.
  const [, setTick] = useState(0)

  // Автоскролл ленты к хвосту при появлении новых событий, пока пользователь
  // не уехал вверх сам. Кнопка «↓ к последнему» возвращается к актуальному.
  const feed = useStickToBottom<HTMLDivElement>()

  useEffect(() => {
    const timer = setInterval(() => {
      setTick((prev) => prev + 1)
    }, RELATIVE_TIME_REFRESH_MS)

    return () => clearInterval(timer)
  }, [])

  return (
    <Maximizable id="feed">
      <PanelFrame title="Event feed" maximizeId="feed">
        <aside id="feed-panel">
          <div id="feed-content" className="event-feed-scroll" ref={feed.ref}>
            {events.map((event) => {
              const ts = Date.parse(event.timestamp)
              const line = toFeedLine(event)

              return (
                <div className={`feed-entry${line.entryClass !== '' ? ` ${line.entryClass}` : ''}`} data-ts={Number.isNaN(ts) ? 0 : ts} key={`${event.timestamp}-${event.type}`}>
                  <span className="feed-time">{formatRelativeTime(Number.isNaN(ts) ? 0 : ts)}</span>
                  <span className={line.msgClass}>
                    {event.stageId !== '' && (
                      <span className={`feed-stage-badge ${line.statusClass}`}>{event.stageId}</span>
                    )}
                    {line.msg}
                  </span>
                </div>
              )
            })}
            {!feed.stick && (
              <button type="button" className="jump-latest" onClick={feed.jumpToBottom}>
                ↓ latest
              </button>
            )}
          </div>
        </aside>
      </PanelFrame>
    </Maximizable>
  )
}

function toFeedLine(event: AfmEvent): FeedLine {
  const data = event.payload

  let msg = ''
  let msgClass = 'feed-msg'
  let statusClass = ''
  let entryClass = ''

  switch (event.type) {
    case 'stage_status_changed': {
      const statusStr = extractStatusString(data)
      msg = `→ ${statusStr}`
      statusClass = statusStr !== '' ? `status-${statusStr.replace(/[^a-z0-9_]/gi, '')}` : ''
      break
    }
    case 'agent_completed':
      msg = `agent ${stringify(data)} completed`
      break
    case 'agent_action': {
      const obj = isRecord(data) ? data : {}
      const tool = typeof obj.tool === 'string' ? obj.tool : ''
      const detail = typeof obj.detail === 'string' ? obj.detail : ''
      msg = `${tool}${detail !== '' ? `: ${detail}` : ''}`
      msgClass = 'feed-msg action'
      break
    }
    case 'approved':
      msg = 'approved'
      statusClass = 'status-awaiting_approval'
      break
    case 'revised':
      msg = `revisions: ${stringify(data)}`
      msgClass = 'feed-msg error'
      statusClass = 'status-revising'
      break
    case 'retry_scheduled':
      msg = `retry: ${stringify(data)}`
      statusClass = 'status-retrying'
      break
    case 'retry_exhausted':
      msg = 'retries exhausted'
      statusClass = 'status-failed'
      msgClass = 'feed-msg error'
      break
    case 'manual_retry':
      msg = 'manual retry'
      statusClass = 'status-retrying'
      break
    case 'ask_user':
      msg = 'question to agent'
      statusClass = 'status-awaiting_user_input'
      break
    case 'user_answered':
      msg = 'reply to user'
      statusClass = 'status-running'
      break
    case 'supervisor_decision': {
      const obj = isRecord(data) ? data : {}
      const autonomous = obj.can_execute_autonomously === true
      const reason = typeof obj.reason === 'string' ? obj.reason : ''
      msg = `supervisor: ${autonomous ? 'autonomous' : 'standard'}${reason !== '' ? ` — ${reason}` : ''}`
      msgClass = 'feed-msg supervisor'
      entryClass = 'supervisor'
      break
    }
    default:
      msg = event.type
  }

  return { msg, msgClass, statusClass, entryClass }
}

function extractStatusString(data: unknown): string {
  if (typeof data === 'string') return data

  if (isRecord(data) && 'status' in data) {
    const status = (data as { status: unknown }).status
    return typeof status === 'string' ? status : ''
  }

  return ''
}

function stringify(data: unknown): string {
  if (typeof data === 'string') return data

  if (data === null || data === undefined) return ''

  return String(data)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object'
}

function formatRelativeTime(tsMs: number): string {
  const diffSec = Math.max(0, Math.floor((Date.now() - tsMs) / 1000))

  if (diffSec < 60) return `${diffSec}s`
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m`
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h`

  return `${Math.floor(diffSec / 86400)}d`
}
