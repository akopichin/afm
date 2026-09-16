import { render, screen, fireEvent, within } from '@testing-library/react'
import { beforeEach, describe, it, expect, vi } from 'vitest'
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

  // --- Навигация по diалог-элементам (dialog_question/dialog_answer) ---

  it('navigable-элемент (dialog_question) рендерится кнопкой и вызывает onOpenDialog с координатами вопроса', () => {
    const events = [
      ev('dialog_question', { phase: 'planning', id: 'q1', title: 'what next?' }, 's1', '2026-07-10T10:00:00Z'),
    ]
    const onOpenDialog = vi.fn()
    render(<FeedWorkspace events={events} logEntries={[]} stageId="s1" onOpenDialog={onOpenDialog} />)
    const button = screen.getByRole('button', { name: /what next\?/ })
    expect(button.tagName).toBe('BUTTON')
    expect(button).toHaveClass('feed-item-navigable')
    fireEvent.click(button)
    expect(onOpenDialog).toHaveBeenCalledWith('s1', 'planning', 'q1')
  })

  it('navigable-элемент (dialog_answer) рендерится кнопкой и вызывает onOpenDialog с координатами вопроса', () => {
    const events = [
      ev('dialog_answer', { phase: 'implementation', id: 'q2', title: 'reply text' }, 's1', '2026-07-10T10:00:00Z'),
    ]
    const onOpenDialog = vi.fn()
    render(<FeedWorkspace events={events} logEntries={[]} stageId="s1" onOpenDialog={onOpenDialog} />)
    const button = screen.getByRole('button', { name: /reply text/ })
    fireEvent.click(button)
    expect(onOpenDialog).toHaveBeenCalledWith('s1', 'implementation', 'q2')
  })

  it('не-navigable элемент (agent_action) НЕ рендерится кнопкой', () => {
    const events = [ev('agent_action', { tool: 'read_file', detail: 'x.ts' }, 's1', '2026-07-10T10:00:00Z')]
    const onOpenDialog = vi.fn()
    render(<FeedWorkspace events={events} logEntries={[]} stageId="s1" onOpenDialog={onOpenDialog} />)
    expect(screen.queryByRole('button', { name: /read_file/ })).not.toBeInTheDocument()
    expect(screen.getByText('read_file: x.ts').closest('.feed-item')?.tagName).toBe('DIV')
  })

  it('без onOpenDialog navigable-элемент рендерится как обычный div, без падения', () => {
    const events = [
      ev('dialog_question', { phase: 'planning', id: 'q1', title: 'what next?' }, 's1', '2026-07-10T10:00:00Z'),
    ]
    expect(() => render(<FeedWorkspace events={events} logEntries={[]} stageId="s1" />)).not.toThrow()
    expect(screen.queryByRole('button', { name: /what next\?/ })).not.toBeInTheDocument()
    expect(screen.getByText('what next?').closest('.feed-item')?.tagName).toBe('DIV')
  })

  describe('note composer', () => {
    it('renders the composer in feed mode when noteTarget is set', () => {
      render(<FeedWorkspace events={[]} logEntries={[]} stageId="s1" noteTarget="s1" onSendNote={vi.fn()} />)
      expect(screen.getByRole('button', { name: /send note to agent/i })).toBeInTheDocument()
    })

    it('does not render the composer when noteTarget is null', () => {
      render(<FeedWorkspace events={[]} logEntries={[]} stageId="s1" noteTarget={null} onSendNote={vi.fn()} />)
      expect(screen.queryByRole('button', { name: /send note to agent/i })).not.toBeInTheDocument()
    })

    it('does not render the composer in log mode even with a noteTarget', () => {
      render(<FeedWorkspace events={[]} logEntries={[]} stageId="s1" noteTarget="s1" onSendNote={vi.fn()} />)
      fireEvent.click(screen.getByRole('button', { name: 'Log' }))
      expect(screen.queryByRole('button', { name: /send note to agent/i })).not.toBeInTheDocument()
    })

    it('delivers the typed note to onSendNote for the target stage', () => {
      const onSendNote = vi.fn().mockResolvedValue(undefined)
      render(<FeedWorkspace events={[]} logEntries={[]} stageId="s1" noteTarget="s1" onSendNote={onSendNote} />)
      fireEvent.change(screen.getByRole('textbox'), { target: { value: 'учти 500' } })
      fireEvent.click(screen.getByRole('button', { name: /send note to agent/i }))
      expect(onSendNote).toHaveBeenCalledWith('s1', 'учти 500')
    })
  })
})
