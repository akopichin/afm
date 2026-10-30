import { render, screen } from '@testing-library/react'
import { describe, it, expect } from 'vitest'
import { SideAgentHistory } from './SideAgentHistory'
import type { SideAgentEvent } from '../../types/side-agent'

const ev = (seq: number, type: SideAgentEvent['type'], data: unknown): SideAgentEvent =>
  ({ schemaVersion: 1, seq, conversationId: 'c1', turnId: 't1', timestamp: '2026-10-01T10:00:00Z', type, data }) as SideAgentEvent

describe('SideAgentHistory', () => {
  it('shows the empty hint when there are no renderable events', () => {
    render(<SideAgentHistory events={[]} />)
    expect(screen.getByText('Ask the assistant anything about this run.')).toBeInTheDocument()
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
    expect(groups[0]?.querySelector('.feed-history-card')).toHaveTextContent('what is 2+2?')
    expect(groups[1]?.querySelector('.feed-actor')).toHaveTextContent('Assistant')
    expect(groups[1]?.querySelector('.mesh-avatar')).toHaveTextContent('AS')
    expect(groups[1]?.querySelector('.feed-group-head time')).not.toBeNull()
    // agent markdown rendered as a heading, not raw text
    expect(container.querySelector('h2')?.textContent).toContain('It is 4')
  })

  it('renders each Mesh sender in a separate left bubble as escaped plain text', () => {
    const { container } = render(<SideAgentHistory events={[
      ev(1, 'turn_accepted', { message: '*hello*', clientMessageId: 'mesh:m1', sourceMessageId: 'm1', sourceSenderId: 'run:qa', sourceSenderName: 'QA' }),
      ev(2, 'turn_accepted', { message: '<script>alert(1)</script>', clientMessageId: 'mesh:m2', sourceMessageId: 'm2', sourceSenderId: 'run:dev' }),
    ]} />)
    const groups = container.querySelectorAll('.feed-group')
    expect(groups).toHaveLength(2)
    expect([...groups].map((g) => g.querySelector('.feed-actor')?.textContent)).toEqual(['From QA', 'From run:dev'])
    expect([...groups].every((g) => g.classList.contains('feed-left'))).toBe(true)
    expect(container.querySelector('script')).toBeNull()
    expect(groups[0]).toHaveTextContent('*hello*')
    expect(groups[1]).toHaveTextContent('<script>alert(1)</script>')
  })

  it('labels the system author "Assistant", never "Flow"', () => {
    const { container } = render(
      <SideAgentHistory events={[ev(1, 'turn_failed', { code: 'agent_error', message: 'boom' })]} />,
    )
    const labels = Array.from(container.querySelectorAll('.feed-actor')).map((el) => el.textContent)
    expect(labels).toContain('Assistant')
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
