import type { ReactElement } from 'react'
import type { Stage } from '../../types'

type FooterProps = {
  stages: Stage[]
  startedAt: string
  elapsedMs: number
}

// Футер: прогресс (доля done), время старта и elapsed. elapsed приходит уже готовым
// от useElapsed (тик каждую секунду). id progress-fill/progress-text/started-at/elapsed
// сохранены для тем.
export function Footer({ stages, startedAt, elapsedMs }: FooterProps): ReactElement {
  const total = stages.length
  const done = stages.filter((stage) => stage.status === 'done').length
  const pct = total > 0 ? Math.round((done / total) * 100) : 0

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
        <span id="elapsed">{startedAt === '' ? '--' : formatDuration(elapsedMs)}</span>
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
