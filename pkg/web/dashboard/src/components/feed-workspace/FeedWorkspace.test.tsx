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
      ev('dialog_answer', { phase: 'planning', id: 'q1', title: 'reply to user' }, 's1', '2026-07-10T10:00:01Z'),
    ]
    const { container } = render(<FeedWorkspace events={events} logEntries={[]} stageId={null} />)
    const groups = container.querySelectorAll('.feed-group')
    expect(groups).toHaveLength(2)
    expect(groups[0]).toHaveClass('feed-left')
    expect(within(groups[0] as HTMLElement).getByText('s1')).toBeInTheDocument()
    expect(groups[1]).toHaveClass('feed-right')
    expect(container.textContent).toContain('read_file: x.ts')
    expect(container.textContent).toContain('reply to user')
  })

  it('shows an empty feed without crashing', () => {
    const { container } = render(<FeedWorkspace events={[]} logEntries={[]} stageId={null} />)
    expect(container.querySelector('#feed-content')).toBeInTheDocument()
    expect(container.querySelectorAll('.feed-group')).toHaveLength(0)
  })

  it('defaults to feed mode and switches to log via the segmented control', () => {
    const logEntries: LogEntry[] = [{ message: '10:00:00 hello', timestamp: '' } as LogEntry]
    render(<FeedWorkspace events={[]} logEntries={logEntries} stageId={null} />)
    // Feed выбран по умолчанию.
    expect(screen.getByRole('button', { name: 'Feed' })).toHaveAttribute('aria-pressed', 'true')
    fireEvent.click(screen.getByRole('button', { name: 'Log' }))
    expect(screen.getByRole('button', { name: 'Log' })).toHaveAttribute('aria-pressed', 'true')
    expect(document.getElementById('log-content')).toHaveTextContent('hello')
  })

  it('shows the log empty-state when there are no log entries', () => {
    render(<FeedWorkspace events={[]} logEntries={[]} stageId={null} />)
    fireEvent.click(screen.getByRole('button', { name: 'Log' }))
    expect(document.getElementById('log-empty')).not.toHaveClass('hidden')
  })

  it('remembers the chosen mode across remounts (afm-feed-mode)', () => {
    const { unmount } = render(<FeedWorkspace events={[]} logEntries={[]} stageId={null} />)
    fireEvent.click(screen.getByRole('button', { name: 'Log' }))
    unmount()
    render(<FeedWorkspace events={[]} logEntries={[]} stageId={null} />)
    expect(screen.getByRole('button', { name: 'Log' })).toHaveAttribute('aria-pressed', 'true')
  })

  // --- Фильтрация по стадии (scope This stage | All) ---

  // События двух стадий + одно flow-level, для проверки фильтра.
  const twoStagesEvents = (): AfmEvent[] => [
    ev('agent_action', { tool: 'read_file', detail: 'a.ts' }, 's1', '2026-07-10T10:00:00Z'),
    ev('agent_action', { tool: 'read_file', detail: 'b.ts' }, 's2', '2026-07-10T10:00:01Z'),
  ]

  it('по умолчанию (scope=stage) показывает события только переданного stageId', () => {
    const { container } = render(
      <FeedWorkspace events={twoStagesEvents()} logEntries={[]} stageId="s1" />,
    )
    expect(container.textContent).toContain('read_file: a.ts')
    expect(container.textContent).not.toContain('read_file: b.ts')
  })

  it('flow-level событие (stageId==="") видно при выбранной стадии', () => {
    const events = [
      ev('agent_action', { tool: 'read_file', detail: 'a.ts' }, 's1', '2026-07-10T10:00:00Z'),
      ev('stage_status_changed', 'running', '', '2026-07-10T10:00:01Z'),
      ev('agent_action', { tool: 'read_file', detail: 'b.ts' }, 's2', '2026-07-10T10:00:02Z'),
    ]
    const { container } = render(<FeedWorkspace events={events} logEntries={[]} stageId="s1" />)
    expect(container.textContent).toContain('read_file: a.ts')
    expect(container.textContent).toContain('→ running') // flow-level всегда видно
    expect(container.textContent).not.toContain('read_file: b.ts')
  })

  it('переключение на All показывает события всех стадий, обратно на This stage — снова фильтрует', () => {
    const { container } = render(
      <FeedWorkspace events={twoStagesEvents()} logEntries={[]} stageId="s1" />,
    )
    // По умолчанию — только s1.
    expect(container.textContent).not.toContain('read_file: b.ts')

    fireEvent.click(screen.getByRole('button', { name: 'All' }))
    expect(container.textContent).toContain('read_file: a.ts')
    expect(container.textContent).toContain('read_file: b.ts')

    fireEvent.click(screen.getByRole('button', { name: 'This stage' }))
    expect(container.textContent).toContain('read_file: a.ts')
    expect(container.textContent).not.toContain('read_file: b.ts')
  })

  it('stageId===null → тумблер скрыт и показываются все события', () => {
    const { container } = render(
      <FeedWorkspace events={twoStagesEvents()} logEntries={[]} stageId={null} />,
    )
    expect(screen.queryByRole('button', { name: 'This stage' })).not.toBeInTheDocument()
    expect(container.textContent).toContain('read_file: a.ts')
    expect(container.textContent).toContain('read_file: b.ts')
  })

  it('тумблер scope скрыт в режиме Log', () => {
    render(<FeedWorkspace events={twoStagesEvents()} logEntries={[]} stageId="s1" />)
    // В Feed тумблер виден.
    expect(screen.getByRole('button', { name: 'This stage' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Log' }))
    expect(screen.queryByRole('button', { name: 'This stage' })).not.toBeInTheDocument()
  })

  it('empty-state зависит от эффективного scope', () => {
    // scope=stage, stageId есть, но событий этой стадии нет → «for this stage».
    const other = [ev('agent_action', { tool: 'read_file', detail: 'b.ts' }, 's2', '2026-07-10T10:00:00Z')]
    const { rerender } = render(<FeedWorkspace events={other} logEntries={[]} stageId="s1" />)
    expect(screen.getByText('No events for this stage yet')).toBeInTheDocument()

    // scope=stage, но stageId=null → эффективный scope false → общий empty-state.
    rerender(<FeedWorkspace events={[]} logEntries={[]} stageId={null} />)
    expect(screen.getByText('No events yet')).toBeInTheDocument()
  })

  it('только flow-level событие → рендерится, не empty', () => {
    const events = [ev('stage_status_changed', 'running', '', '2026-07-10T10:00:00Z')]
    const { container } = render(<FeedWorkspace events={events} logEntries={[]} stageId="s1" />)
    expect(container.querySelectorAll('.feed-group')).toHaveLength(1)
    expect(screen.queryByText('No events for this stage yet')).not.toBeInTheDocument()
    expect(container.textContent).toContain('→ running')
  })
})
