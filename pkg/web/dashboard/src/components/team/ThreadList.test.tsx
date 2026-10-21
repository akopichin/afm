import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { ThreadList } from './ThreadList'
import type { MeshThread } from '../../api/mesh-client'

const threads: MeshThread[] = [
  { id: 'mt-1', title: 'Hand-off', scope: 'run', members: ['user', 'run:qa'], status: 'open' },
  { id: 'mt-2', scope: 'run', members: ['user', 'run:devops'], status: 'closed' },
]

describe('ThreadList', () => {
  test('shows a dot and New messages in the row accessible name', () => {
    render(<ThreadList threads={threads} participants={[]} unreadThreadIds={new Set(['mt-1'])} onSelect={vi.fn()} />)
    const row = screen.getByRole('button', { name: /Hand-off.*New messages/ })
    expect(row.querySelector('.team-thread-unread')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /DevOps.*new messages/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /Hand-off.*1 new message/ })).toBeNull()
  })
  afterEach(() => vi.useRealTimers())

  test('shows saved plain preview, sender, relative time with an accessible full timestamp, and empty copy', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-21T12:00:00Z'))
    const longTitle = 'Long release title '.repeat(12)
    const longPreview = '<script>alert("plain")</script> '.repeat(8)
    render(<ThreadList threads={[{ ...threads[0]!, title: longTitle }, threads[1]!]} participants={[
      { id: 'run:qa', kind: 'run_agent', scope: 'run', display_name: 'QA Engineer', reachable: true },
    ]} summaries={{ 'mt-1': {
      last_message: { id: 'm1', seq: 4, created_at: '2026-10-21T11:58:00Z', sender_id: 'run:qa', preview: longPreview },
      last_other_message_seq: 4, title_seq: 0,
    } }} onSelect={vi.fn()} />)
    const row = screen.getByRole('button', { name: new RegExp('Long release title') })
    expect(row).toHaveTextContent(`QA Engineer: ${longPreview.trim()}`)
    expect(row.querySelector('script')).toBeNull()
    expect(row.querySelector('.team-thread-title')).toHaveAttribute('title', longTitle)
    expect(row.querySelector('.team-thread-preview')).toHaveAttribute('title', `QA Engineer: ${longPreview}`)
    const timestamp = row.querySelector('time')
    expect(timestamp).toHaveAttribute('datetime', '2026-10-21T11:58:00Z')
    expect(timestamp).toHaveAttribute('aria-label', expect.stringContaining('2026'))
    expect(timestamp).toHaveTextContent('2m')
    expect(screen.getByText('No messages yet')).toBeInTheDocument()
    expect(screen.getByText('Closed')).toBeInTheDocument()
    act(() => vi.advanceTimersByTime(60_000))
    expect(timestamp).toHaveTextContent('3m')
  })
  test('empty state when there are no conversations', () => {
    render(<ThreadList threads={[]} participants={[]} onSelect={vi.fn()} />)
    expect(screen.getByText('No conversations yet')).toBeInTheDocument()
  })

  test('renders a title or the other member\'s name, and marks closed threads', () => {
    const participants = [{ id: 'run:devops', kind: 'run_agent', scope: 'run', display_name: 'DevOps', reachable: true }]
    render(<ThreadList threads={threads} participants={participants} onSelect={vi.fn()} />)
    expect(screen.getByText('Hand-off')).toBeInTheDocument()
    expect(screen.getByText('DevOps')).toBeInTheDocument()
    expect(screen.queryByText('mt-2')).toBeNull()
    expect(screen.getByText('Closed')).toBeInTheDocument()
  })

  test('distinguishes titled direct conversations with the same topic and role by participant', () => {
    const participants = [
      { id: 'run:qa', kind: 'run_agent', scope: 'run', display_name: 'QA Engineer', reachable: true },
      { id: 'run:devops', kind: 'run_agent', scope: 'run', display_name: 'DevOps Engineer', reachable: true },
    ]
    const conversations: MeshThread[] = [
      { ...threads[0]!, title: 'Release review' },
      { ...threads[1]!, title: 'Release review', status: 'open' },
    ]
    render(<ThreadList threads={conversations} participants={participants} onSelect={vi.fn()} />)

    const qaRow = screen.getByRole('button', { name: /Release review.*QA Engineer/ })
    const devopsRow = screen.getByRole('button', { name: /Release review.*DevOps Engineer/ })
    expect(qaRow.querySelector('.team-person-detail')).toHaveTextContent('QA Engineer · Run agent')
    expect(devopsRow.querySelector('.team-person-detail')).toHaveTextContent('DevOps Engineer · Run agent')
    expect(qaRow.querySelector('.team-person-detail')).toHaveAttribute('title', 'QA Engineer · Run agent')
    expect(devopsRow.querySelector('.team-person-detail')).toHaveAttribute('title', 'DevOps Engineer · Run agent')
  })

  test('clicking a row calls onSelect with that thread', () => {
    const onSelect = vi.fn()
    render(<ThreadList threads={threads} participants={[]} onSelect={onSelect} />)
    fireEvent.click(screen.getByText('Hand-off'))
    expect(onSelect).toHaveBeenCalledWith(threads[0])
  })

  test('a title-only roster refresh changes the label without replacing the focused row', () => {
    const member = { id: 'run:qa', kind: 'run_agent', scope: 'run', display_name: 'QA Engineer', reachable: true }
    const { rerender } = render(<ThreadList threads={[threads[0]!]} participants={[member]} onSelect={vi.fn()} />)
    const row = screen.getByRole('button', { name: /Hand-off/ })
    row.focus()
    rerender(<ThreadList threads={[{ ...threads[0]!, title: '' }]} participants={[member]} onSelect={vi.fn()} />)
    expect(screen.getByRole('button', { name: /QA Engineer/ })).toBe(row)
    expect(row).toHaveFocus()
    rerender(<ThreadList threads={[{ ...threads[0]!, title: 'New title' }]} participants={[member]} onSelect={vi.fn()} />)
    expect(screen.getByRole('button', { name: /New title/ })).toBe(row)
  })

  test('a group conversation shows the group symbol and its members by initials; a one-to-one shows the person', () => {
    const participants = [
      { id: 'run:qa', kind: 'run_agent', scope: 'run', display_name: 'QA Engineer', reachable: true },
      { id: 'run:devops', kind: 'run_agent', scope: 'run', display_name: 'DevOps', reachable: true },
    ]
    const group: MeshThread = { id: 'mt-3', title: 'Tests for CI', scope: 'run', members: ['user', 'run:qa', 'run:devops'], status: 'open' }
    const { container } = render(<ThreadList threads={[group, threads[0] as MeshThread]} participants={participants} onSelect={vi.fn()} />)
    const [groupRow, directRow] = Array.from(container.querySelectorAll('.team-thread-item'))
    expect(groupRow?.querySelector('.team-thread-symbol')).not.toBeNull()
    expect(groupRow).toHaveTextContent('QA · DO')
    expect(directRow?.querySelector('.mesh-avatar')).toHaveTextContent('QA')
  })
})
