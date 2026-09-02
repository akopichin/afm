import { act, fireEvent, render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import type { AfmEvent, LogEntry } from '../../types'
import { MaximizeProvider } from '../layout/Maximizable'
import { EventFeedPanel } from './EventFeedPanel'

describe('EventFeedPanel', () => {
  beforeEach(() => {
    window.localStorage.clear()
  })

  test('renders representative feed lines for known event types and falls back to type for unknown ones', () => {
    const events: AfmEvent[] = [
      { type: 'stage_status_changed', payload: 'running', stageId: 's1', timestamp: '2026-07-10T10:00:00Z' },
      { type: 'agent_action', payload: { tool: 'read_file', detail: 'src/x.ts' }, stageId: '', timestamp: '2026-07-10T10:00:01Z' },
      { type: 'custom_unknown_type', payload: null, stageId: '', timestamp: '2026-07-10T10:00:03Z' },
    ]

    const { container } = render(<EventFeedPanel events={events} logEntries={[]} />)

    const entries = container.querySelectorAll('.feed-entry')
    expect(entries).toHaveLength(3)

    expect(entries[0]?.textContent).toContain('→ running')
    expect(entries[1]?.textContent).toContain('read_file: src/x.ts')
    expect(entries[2]?.textContent).toContain('custom_unknown_type')
  })

  test('renders a stage badge only when event.stageId is not empty', () => {
    const events: AfmEvent[] = [
      { type: 'stage_status_changed', payload: 'running', stageId: 's1', timestamp: '2026-07-10T10:00:00Z' },
      { type: 'agent_action', payload: { tool: 'read_file' }, stageId: '', timestamp: '2026-07-10T10:00:01Z' },
    ]

    const { container } = render(<EventFeedPanel events={events} logEntries={[]} />)

    const entries = container.querySelectorAll('.feed-entry')
    expect(entries[0]?.querySelector('.feed-stage-badge')).toHaveTextContent('s1')
    expect(entries[1]?.querySelector('.feed-stage-badge')).not.toBeInTheDocument()
  })

  test('renders an empty feed without crashing when events is empty', () => {
    const { container } = render(<EventFeedPanel events={[]} logEntries={[]} />)

    expect(container.querySelectorAll('.feed-entry')).toHaveLength(0)
    expect(container.querySelector('#feed-content')).toBeInTheDocument()
  })

  test('shows a static gap-from-previous-event duration per row, not a live relative time', () => {
    const events: AfmEvent[] = [
      { type: 'stage_status_changed', payload: 'running', stageId: 's1', timestamp: '2026-07-10T10:00:00.000Z' },
      { type: 'agent_action', payload: { tool: 'read_file' }, stageId: '', timestamp: '2026-07-10T10:00:05.000Z' },
      { type: 'agent_action', payload: { tool: 'write_file' }, stageId: '', timestamp: '2026-07-10T10:01:35.000Z' },
    ]

    const { container } = render(<EventFeedPanel events={events} logEntries={[]} />)
    const times = Array.from(container.querySelectorAll('.feed-time')).map((el) => el.textContent)

    // Первая строка — нет предыдущего события в ленте.
    expect(times[0]).toBe('—')
    // Вторая строка: 10:00:05 − 10:00:00 = 5s.
    expect(times[1]).toBe('5s')
    // Третья строка: 10:01:35 − 10:00:05 = 90s = 1m.
    expect(times[2]).toBe('1m')
  })

  test('does not re-render feed-time on a timer tick (no more live ticking)', () => {
    vi.useFakeTimers({ now: new Date('2026-07-10T10:05:00.000Z') })

    const events: AfmEvent[] = [
      { type: 'agent_action', payload: {}, stageId: '', timestamp: '2026-07-10T10:00:00.000Z' },
      { type: 'agent_action', payload: {}, stageId: '', timestamp: '2026-07-10T10:00:10.000Z' },
    ]

    const { container } = render(<EventFeedPanel events={events} logEntries={[]} />)
    const before = container.querySelector('.feed-entry:last-child .feed-time')?.textContent

    act(() => {
      vi.advanceTimersByTime(60_000)
    })

    const after = container.querySelector('.feed-entry:last-child .feed-time')?.textContent
    expect(after).toBe(before)
    expect(after).toBe('10s')

    vi.useRealTimers()
  })

  test('renders script_output, hook_failed, and hook_resolved lines', () => {
    const events: AfmEvent[] = [
      { type: 'script_output', payload: { hook: 'before', line: 'setting up' }, stageId: 's1', timestamp: '2026-07-29T10:00:00Z' },
      { type: 'hook_failed', payload: { hook: 'before', error: 'exit 1' }, stageId: 's1', timestamp: '2026-07-29T10:00:01Z' },
      { type: 'hook_resolved', payload: { hook: 'before', resolution: 'skipped' }, stageId: 's1', timestamp: '2026-07-29T10:00:02Z' },
    ]

    const { container } = render(<EventFeedPanel events={events} logEntries={[]} />)
    const entries = container.querySelectorAll('.feed-entry')
    expect(entries).toHaveLength(3)

    expect(entries[0]?.textContent).toContain('before')
    expect(entries[0]?.textContent).toContain('setting up')
    expect(entries[1]?.textContent).toContain('before')
    expect(entries[1]?.textContent).toContain('exit 1')
    expect(entries[2]?.textContent).toContain('skipped')
  })

  test('renders auto_answered lines with the synthesized answer', () => {
    const events: AfmEvent[] = [
      { type: 'auto_answered', payload: { id: 'q1', phase: 'implementation', answer: 'Вариант B', from_options: true }, stageId: 's1', timestamp: '2026-08-07T10:00:00Z' },
    ]

    const { container } = render(<EventFeedPanel events={events} logEntries={[]} />)
    const entries = container.querySelectorAll('.feed-entry')
    expect(entries).toHaveLength(1)
    expect(entries[0]?.textContent).toContain('auto-answered')
    expect(entries[0]?.textContent).toContain('q1')
    expect(entries[0]?.textContent).toContain('Вариант B')
  })

  test('defaults to feed mode, with a toggle button to switch to the stage log', () => {
    const { container } = render(<EventFeedPanel events={[]} logEntries={[]} />)

    expect(container.querySelector('.panel-frame-header h3')).toHaveTextContent('Event feed')
    expect(screen.getByRole('button', { name: 'Switch to log' })).toBeInTheDocument()
  })

  test('clicking the toggle switches to log mode, rendering stage log entries and updating the panel title', () => {
    const logEntries: LogEntry[] = [{ timestamp: '10:00:00', message: 'first line', level: 'info' }]
    const { container } = render(<EventFeedPanel events={[]} logEntries={logEntries} />)

    fireEvent.click(screen.getByRole('button', { name: 'Switch to log' }))

    expect(container.querySelector('.panel-frame-header h3')).toHaveTextContent('Log')
    expect(container.querySelector('#log-content')).toHaveTextContent('first line')
    expect(screen.getByRole('button', { name: 'Switch to feed' })).toBeInTheDocument()
  })

  test('shows the log empty-state and hides log content when logEntries is empty', () => {
    const { container } = render(<EventFeedPanel events={[]} logEntries={[]} />)

    fireEvent.click(screen.getByRole('button', { name: 'Switch to log' }))

    expect(container.querySelector('#log-content')).toHaveClass('hidden')
    expect(container.querySelector('#log-empty')).not.toHaveClass('hidden')
    expect(container.querySelector('#log-empty')).toHaveTextContent('Log is empty')
  })

  test('log mode auto-scrolls to the bottom (stick-to-bottom, like the feed)', () => {
    // Лог должен сам прокручиваться к хвосту, как лента (фид) — раньше <pre>
    // лога не был привязан к useStickToBottom и не докручивался (баг: «прогресс
    // логов сам не прокручивается, как в фиде, надо листать»).
    window.localStorage.setItem('afm-feed-mode', 'log')

    const original = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'scrollHeight')
    Object.defineProperty(HTMLElement.prototype, 'scrollHeight', { configurable: true, get: () => 777 })

    try {
      const logEntries: LogEntry[] = [{ timestamp: '10:00:00', message: 'line 1', level: 'info' }]
      const { container } = render(<EventFeedPanel events={[]} logEntries={logEntries} />)

      const pre = container.querySelector('#log-content') as HTMLElement | null
      expect(pre).not.toBeNull()
      // Контейнер примонтировался с готовым контентом → stick-to-bottom сразу
      // докручивает вниз (scrollTop == scrollHeight).
      expect(pre?.scrollTop).toBe(777)
    } finally {
      if (original) Object.defineProperty(HTMLElement.prototype, 'scrollHeight', original)
      else Reflect.deleteProperty(HTMLElement.prototype, 'scrollHeight')
    }
  })

  test('remembers the chosen mode across remounts via localStorage', () => {
    const first = render(<EventFeedPanel events={[]} logEntries={[]} />)
    fireEvent.click(screen.getByRole('button', { name: 'Switch to log' }))
    first.unmount()

    const { container } = render(<EventFeedPanel events={[]} logEntries={[]} />)
    expect(container.querySelector('.panel-frame-header h3')).toHaveTextContent('Log')
  })

  test('maximize still works while in log mode, moving log content into the fullscreen overlay', () => {
    const logEntries: LogEntry[] = [{ timestamp: '10:00:00', message: 'line', level: 'info' }]
    render(
      <MaximizeProvider>
        <EventFeedPanel events={[]} logEntries={logEntries} />
      </MaximizeProvider>,
    )

    fireEvent.click(screen.getByRole('button', { name: 'Switch to log' }))
    fireEvent.click(screen.getByRole('button', { name: 'Expand' }))

    expect(document.querySelector('.maximize-overlay #log-content')).not.toBeNull()
    expect(document.querySelector('.maximize-overlay #log-content')).toHaveTextContent('line')
  })

  // Finding #12: several same-type live events accepted in the same millisecond
  // used to share the React key `${timestamp}-${type}` → a duplicate-key warning
  // and unstable DOM reuse. Keys must be unique (occurrence-disambiguated).
  test('same-timestamp same-type events get unique keys (no duplicate-key warning)', () => {
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const ts = '2026-07-10T10:00:00.000Z'
    const events: AfmEvent[] = [
      { type: 'agent_action', payload: { tool: 'read_file', detail: 'a' }, stageId: 's1', timestamp: ts },
      { type: 'agent_action', payload: { tool: 'read_file', detail: 'b' }, stageId: 's1', timestamp: ts },
      { type: 'agent_action', payload: { tool: 'read_file', detail: 'c' }, stageId: 's1', timestamp: ts },
    ]

    const { container } = render(<EventFeedPanel events={events} logEntries={[]} />)

    expect(container.querySelectorAll('.feed-entry')).toHaveLength(3)
    const sawDupKeyWarning = errSpy.mock.calls.some((call) =>
      call.some((arg) => typeof arg === 'string' && arg.includes('same key')),
    )
    expect(sawDupKeyWarning).toBe(false)
    errSpy.mockRestore()
  })
})
