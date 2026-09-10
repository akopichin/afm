import { render, screen, fireEvent, within } from '@testing-library/react'
import { beforeEach, describe, it, expect } from 'vitest'
import type { AfmEvent, LogEntry } from '../../types'
import { FeedWorkspace } from './FeedWorkspace'

const ev = (type: string, payload: unknown, stageId: string, timestamp: string): AfmEvent =>
  ({ type, payload, stageId, timestamp }) as AfmEvent

describe('FeedWorkspace', () => {
  beforeEach(() => window.localStorage.clear())

  it('renders messenger groups with stage badge and side alignment', () => {
    const events = [
      ev('agent_action', { tool: 'read_file', detail: 'x.ts' }, 's1', '2026-07-10T10:00:00Z'),
      ev('user_answered', {}, 's1', '2026-07-10T10:00:01Z'),
    ]
    const { container } = render(<FeedWorkspace events={events} logEntries={[]} />)
    const groups = container.querySelectorAll('.feed-group')
    expect(groups).toHaveLength(2)
    expect(groups[0]).toHaveClass('feed-left')
    expect(within(groups[0] as HTMLElement).getByText('s1')).toBeInTheDocument()
    expect(groups[1]).toHaveClass('feed-right')
    expect(container.textContent).toContain('read_file: x.ts')
    expect(container.textContent).toContain('reply to user')
  })

  it('shows an empty feed without crashing', () => {
    const { container } = render(<FeedWorkspace events={[]} logEntries={[]} />)
    expect(container.querySelector('#feed-content')).toBeInTheDocument()
    expect(container.querySelectorAll('.feed-group')).toHaveLength(0)
  })

  it('defaults to feed mode and switches to log via the segmented control', () => {
    const logEntries: LogEntry[] = [{ message: '10:00:00 hello', timestamp: '' } as LogEntry]
    render(<FeedWorkspace events={[]} logEntries={logEntries} />)
    // Feed выбран по умолчанию.
    expect(screen.getByRole('button', { name: 'Feed' })).toHaveAttribute('aria-pressed', 'true')
    fireEvent.click(screen.getByRole('button', { name: 'Log' }))
    expect(screen.getByRole('button', { name: 'Log' })).toHaveAttribute('aria-pressed', 'true')
    expect(document.getElementById('log-content')).toHaveTextContent('hello')
  })

  it('shows the log empty-state when there are no log entries', () => {
    render(<FeedWorkspace events={[]} logEntries={[]} />)
    fireEvent.click(screen.getByRole('button', { name: 'Log' }))
    expect(document.getElementById('log-empty')).not.toHaveClass('hidden')
  })

  it('remembers the chosen mode across remounts (afm-feed-mode)', () => {
    const { unmount } = render(<FeedWorkspace events={[]} logEntries={[]} />)
    fireEvent.click(screen.getByRole('button', { name: 'Log' }))
    unmount()
    render(<FeedWorkspace events={[]} logEntries={[]} />)
    expect(screen.getByRole('button', { name: 'Log' })).toHaveAttribute('aria-pressed', 'true')
  })
})
