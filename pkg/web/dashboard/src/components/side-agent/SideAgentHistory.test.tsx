import { render, screen } from '@testing-library/react'
import { describe, it, expect } from 'vitest'
import { SideAgentHistory } from './SideAgentHistory'
import type { SideAgentEvent } from '../../types/side-agent'

const ev = (seq: number, type: SideAgentEvent['type'], data: unknown): SideAgentEvent =>
  ({ schemaVersion: 1, seq, conversationId: 'c1', turnId: 't1', timestamp: '2026-10-01T10:00:00Z', type, data }) as SideAgentEvent

describe('SideAgentHistory', () => {
  it('shows the empty hint when there are no renderable events', () => {
    render(<SideAgentHistory events={[]} />)
    expect(screen.getByText(/ask the agent/i)).toBeInTheDocument()
  })

  it('renders user message on the right and agent markdown on the left', () => {
    const { container } = render(
      <SideAgentHistory
        events={[
          ev(1, 'turn_accepted', { message: 'what is 2+2?', clientMessageId: 'm1' }),
          ev(2, 'assistant_text', { text: '## It is 4' }),
        ]}
      />,
    )
    const groups = container.querySelectorAll('.feed-group')
    expect(groups).toHaveLength(2)
    expect(groups[0]).toHaveClass('feed-right')
    expect(groups[1]).toHaveClass('feed-left')
    // agent markdown rendered as a heading, not raw text
    expect(container.querySelector('h2')?.textContent).toContain('It is 4')
  })

  it('labels the system author "Side agent", never "Flow"', () => {
    const { container } = render(
      <SideAgentHistory events={[ev(1, 'turn_failed', { code: 'agent_error', message: 'boom' })]} />,
    )
    const labels = Array.from(container.querySelectorAll('.feed-actor')).map((el) => el.textContent)
    expect(labels).toContain('Side agent')
    expect(labels).not.toContain('Flow')
  })

  it('does not turn [AFM image: …] into an <img> (no stage artifacts)', () => {
    const { container } = render(
      <SideAgentHistory events={[ev(1, 'assistant_text', { text: 'see [AFM image: chart.png]' })]} />,
    )
    expect(container.querySelector('img.feed-image')).toBeNull()
    expect(container.textContent).toContain('[AFM image: chart.png]')
  })
})
