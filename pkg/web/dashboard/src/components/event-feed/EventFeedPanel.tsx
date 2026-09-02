import { type ReactElement } from 'react'
import type { AfmEvent, LogEntry } from '../../types'
import { Maximizable } from '../layout/Maximizable'
import { PanelFrame } from '../panel-frame/PanelFrame'
import { useStickToBottom } from '../../hooks/use-stick-to-bottom'
import { useFeedMode } from '../../hooks/use-feed-mode'

type EventFeedPanelProps = {
  events: AfmEvent[]
  // Лог выбранной стадии — второй режим этой же панели (переключается кнопкой
  // в её шапке), см. useFeedMode. Раньше жил отдельной строкой в центральной
  // колонке (LogPanel), теперь всегда в правой панели, чтобы не отъедать место
  // у plan/dialog.
  logEntries: LogEntry[]
}

type FeedLine = {
  msg: string
  msgClass: string
  statusClass: string
  entryClass: string
}

// Правая панель: лента событий флоу. Форматирование записей (текст и классы бейджа)
// совпадает с addFeedEntry в текущем app.js. Время у строки — статичное: разница с
// предыдущим событием в ленте («сколько заняло время между операциями»), а не
// тикающее «N секунд назад» — тикает только elapsed в футере.
export function EventFeedPanel({ events, logEntries }: EventFeedPanelProps): ReactElement {
  // Автоскролл ленты к хвосту при появлении новых событий, пока пользователь
  // не уехал вверх сам. Кнопка «↓ к последнему» возвращается к актуальному.
  const feed = useStickToBottom<HTMLDivElement>()
  // Лог — второй режим этой же панели — тоже должен сам докручиваться к хвосту,
  // как лента: свой независимый стик-скролл на <pre>-контейнере (у него overflow-y
  // от .log-content, см. log-panel.css). Раньше <pre> не был ни к чему привязан и
  // не прокручивался — приходилось листать вручную.
  const log = useStickToBottom<HTMLPreElement>()
  const { mode, toggle } = useFeedMode()
  const hasLogEntries = logEntries.length > 0
  // Стабильные уникальные React-ключи (finding #12): прежний `${timestamp}-${type}`
  // совпадал у нескольких однотипных live-событий, принятых в одну миллисекунду
  // (agent_action и т.п.) → React duplicate-key warning и потенциально неверное
  // переиспользование DOM-узлов. Здесь ключи disambiguates по occurrence-счётчику,
  // детерминированно и стабильно при неизменном порядке ленты.
  const entryKeys = feedEntryKeys(events)

  return (
    <Maximizable id="feed">
      <PanelFrame
        title={mode === 'feed' ? 'Event feed' : 'Log'}
        maximizeId="feed"
        actions={
          <button
            type="button"
            className="icon-btn"
            aria-label={mode === 'feed' ? 'Switch to log' : 'Switch to feed'}
            title={mode === 'feed' ? 'Switch to log' : 'Switch to feed'}
            onClick={toggle}
          >
            {mode === 'feed' ? 'Log' : 'Feed'}
          </button>
        }
      >
        <aside id="feed-panel">
          {mode === 'feed' ? (
            <div id="feed-content" className="event-feed-scroll" ref={feed.ref}>
              {events.map((event, index) => {
                const ts = Date.parse(event.timestamp)
                const prevTs = index > 0 ? Date.parse(events[index - 1]?.timestamp ?? '') : NaN
                const line = toFeedLine(event)

                return (
                  <div className={`feed-entry${line.entryClass !== '' ? ` ${line.entryClass}` : ''}`} data-ts={Number.isNaN(ts) ? 0 : ts} key={entryKeys[index]}>
                    <span className="feed-time">{formatEventGap(ts, prevTs)}</span>
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
          ) : (
            <div className="section log-section">
              <pre id="log-content" ref={log.ref} className={`log-content${hasLogEntries ? '' : ' hidden'}`}>
                {logEntries.map((entry) => entry.message).join('\n')}
              </pre>
              {hasLogEntries && !log.stick && (
                <button type="button" className="jump-latest" onClick={log.jumpToBottom}>
                  ↓ latest
                </button>
              )}
              <div id="log-empty" className={`empty-hint${hasLogEntries ? ' hidden' : ''}`}>Log is empty</div>
            </div>
          )}
        </aside>
      </PanelFrame>
    </Maximizable>
  )
}

// feedEntryKeys строит уникальные стабильные React-ключи для ленты: базовый
// ключ — seq (если есть) либо `timestamp|type|stageId`, а при повторе того же
// базового ключа добавляется occurrence-суффикс `#N`. Детерминированно при
// неизменном порядке событий, так что ключи не «прыгают» между рендерами
// (finding #12).
function feedEntryKeys(events: AfmEvent[]): string[] {
  const counts = new Map<string, number>()
  return events.map((e) => {
    const base = e.seq !== undefined ? `seq:${e.seq}` : `${e.timestamp}|${e.type}|${e.stageId}`
    const n = counts.get(base) ?? 0
    counts.set(base, n + 1)
    return n === 0 ? base : `${base}#${n}`
  })
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
    case 'script_output': {
      const obj = isRecord(data) ? data : {}
      const hook = typeof obj.hook === 'string' ? obj.hook : ''
      const line = typeof obj.line === 'string' ? obj.line : ''
      msg = `[${hook}] ${line}`
      msgClass = 'feed-msg action'
      break
    }
    case 'hook_failed': {
      const obj = isRecord(data) ? data : {}
      const hook = typeof obj.hook === 'string' ? obj.hook : ''
      const error = typeof obj.error === 'string' ? obj.error : ''
      msg = `${hook}-hook failed: ${error}`
      msgClass = 'feed-msg error'
      statusClass = 'status-hook_failed'
      break
    }
    case 'hook_resolved': {
      const obj = isRecord(data) ? data : {}
      const hook = typeof obj.hook === 'string' ? obj.hook : ''
      const resolution = typeof obj.resolution === 'string' ? obj.resolution : ''
      msg = `${hook}-hook ${resolution}`
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
    case 'auto_answered': {
      const obj = isRecord(data) ? data : {}
      const id = typeof obj.id === 'string' ? obj.id : ''
      const answer = typeof obj.answer === 'string' ? obj.answer : ''
      msg = `auto-answered ${id}: ${answer}`
      msgClass = 'feed-msg action'
      break
    }
    case 'context_warning':
      msg = `context warning: ${stringify(data)}`
      msgClass = 'feed-msg warning'
      break
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

// Статичная длительность строки: разница между этим событием и предыдущим в
// ленте (не per-стадийно — по порядку отображения). Нет предыдущего (первая
// строка) или невалидный timestamp у любой из сторон — em dash.
function formatEventGap(tsMs: number, prevTsMs: number): string {
  if (Number.isNaN(tsMs) || Number.isNaN(prevTsMs)) return '—'

  const diffSec = Math.max(0, Math.floor((tsMs - prevTsMs) / 1000))

  if (diffSec < 60) return `${diffSec}s`
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m`
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h`

  return `${Math.floor(diffSec / 86400)}d`
}
