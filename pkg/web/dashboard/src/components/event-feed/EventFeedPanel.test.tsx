import { render } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
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
})
