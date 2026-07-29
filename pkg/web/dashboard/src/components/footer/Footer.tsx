import type { ReactElement } from 'react'
import type { Stage } from '../../types'

type FooterProps = {
  stages: Stage[]
  startedAt: string
  elapsedMs: number
  // Idle — суммарное время, когда хоть одна стадия ждала пользователя
  // (awaiting_user_input/awaiting_approval/failed). Backoff — суммарное
  // время автоматического retry-backoff (retrying), без участия
  // пользователя. Оба — из useStatusDuration (см. app/App.tsx).
  idleMs: number
  backoffMs: number
}

// Футер: прогресс (доля done), время старта, elapsed/idle/backoff. Все три
// счётчика приходят уже готовыми (тик — забота вызывающих хуков). id
// progress-fill/progress-text/started-at/elapsed/idle/backoff сохранены для тем.
export function Footer({ stages, startedAt, elapsedMs, idleMs, backoffMs }: FooterProps): ReactElement {
  const total = stages.length
  const done = stages.filter((stage) => stage.status === 'done').length
  const pct = total > 0 ? Math.round((done / total) * 100) : 0
  const hasStarted = startedAt !== ''

  return (
    <footer id="footer">
      <div className="footer-item">
        <span className="footer-label">Progress:</span>
        <div className="progress-bar">
          <div id="progress-fill" className="progress-fill" style={{ width: `${pct}%` }} />
        </div>
        <span id="progress-text">{`${done} / ${total}`}</span>
      </div>
      <div className="footer-item">
        <span className="footer-label">Started:</span>
        <span id="started-at">{formatClock(startedAt)}</span>
      </div>
      <div className="footer-item">
        <span className="footer-label">Elapsed:</span>
        <span id="elapsed">{hasStarted ? formatDuration(elapsedMs) : '--'}</span>
      </div>
      <div className="footer-item">
        <span className="footer-label">Idle:</span>
        <span id="idle">{hasStarted ? formatDuration(idleMs) : '--'}</span>
      </div>
      <div className="footer-item">
        <span className="footer-label">Backoff:</span>
        <span id="backoff">{hasStarted ? formatDuration(backoffMs) : '--'}</span>
      </div>
    </footer>
  )
}

function formatClock(startedAt: string): string {
  if (startedAt === '') return '--'

  const parsed = new Date(startedAt)
  if (Number.isNaN(parsed.getTime())) return '--'

  return formatTime(parsed)
}

function formatTime(date: Date): string {
  return [date.getHours(), date.getMinutes(), date.getSeconds()].map(pad).join(':')
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
