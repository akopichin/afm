import { act, render } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import type { AfmEvent } from '../../types'
import { EventFeedPanel } from './EventFeedPanel'

describe('EventFeedPanel', () => {
  test('renders representative feed lines for known event types and falls back to type for unknown ones', () => {
    const events: AfmEvent[] = [
      { type: 'stage_status_changed', payload: 'running', stageId: 's1', timestamp: '2026-07-10T10:00:00Z' },
      { type: 'agent_action', payload: { tool: 'read_file', detail: 'src/x.ts' }, stageId: '', timestamp: '2026-07-10T10:00:01Z' },
      {
        type: 'supervisor_decision',
        payload: { can_execute_autonomously: true, reason: 'looks safe' },
        stageId: 's2',
        timestamp: '2026-07-10T10:00:02Z',
      },
      { type: 'custom_unknown_type', payload: null, stageId: '', timestamp: '2026-07-10T10:00:03Z' },
    ]

    const { container } = render(<EventFeedPanel events={events} />)

    const entries = container.querySelectorAll('.feed-entry')
    expect(entries).toHaveLength(4)

    expect(entries[0]?.textContent).toContain('→ running')
    expect(entries[1]?.textContent).toContain('read_file: src/x.ts')
    expect(entries[2]?.textContent).toContain('supervisor: autonomous — looks safe')
    expect(entries[2]).toHaveClass('supervisor')
    expect(entries[3]?.textContent).toContain('custom_unknown_type')
  })

  test('renders a stage badge only when event.stageId is not empty', () => {
    const events: AfmEvent[] = [
      { type: 'stage_status_changed', payload: 'running', stageId: 's1', timestamp: '2026-07-10T10:00:00Z' },
      { type: 'agent_action', payload: { tool: 'read_file' }, stageId: '', timestamp: '2026-07-10T10:00:01Z' },
    ]

    const { container } = render(<EventFeedPanel events={events} />)

    const entries = container.querySelectorAll('.feed-entry')
    expect(entries[0]?.querySelector('.feed-stage-badge')).toHaveTextContent('s1')
    expect(entries[1]?.querySelector('.feed-stage-badge')).not.toBeInTheDocument()
  })

  test('renders an empty feed without crashing when events is empty', () => {
    const { container } = render(<EventFeedPanel events={[]} />)

    expect(container.querySelectorAll('.feed-entry')).toHaveLength(0)
    expect(container.querySelector('#feed-content')).toBeInTheDocument()
  })

  test('shows a static gap-from-previous-event duration per row, not a live relative time', () => {
    const events: AfmEvent[] = [
      { type: 'stage_status_changed', payload: 'running', stageId: 's1', timestamp: '2026-07-10T10:00:00.000Z' },
      { type: 'agent_action', payload: { tool: 'read_file' }, stageId: '', timestamp: '2026-07-10T10:00:05.000Z' },
      { type: 'agent_action', payload: { tool: 'write_file' }, stageId: '', timestamp: '2026-07-10T10:01:35.000Z' },
    ]

    const { container } = render(<EventFeedPanel events={events} />)
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

    const { container } = render(<EventFeedPanel events={events} />)
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

    const { container } = render(<EventFeedPanel events={events} />)
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

    const { container } = render(<EventFeedPanel events={events} />)
    const entries = container.querySelectorAll('.feed-entry')
    expect(entries).toHaveLength(1)
    expect(entries[0]?.textContent).toContain('auto-answered')
    expect(entries[0]?.textContent).toContain('q1')
    expect(entries[0]?.textContent).toContain('Вариант B')
  })
})
