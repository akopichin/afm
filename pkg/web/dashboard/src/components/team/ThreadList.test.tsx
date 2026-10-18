import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { ThreadList } from './ThreadList'
import type { MeshThread } from '../../api/mesh-client'

const threads: MeshThread[] = [
  { id: 'mt-1', title: 'Hand-off', scope: 'run', members: ['user', 'run:qa'], status: 'open' },
  { id: 'mt-2', scope: 'run', members: ['user', 'run:devops'], status: 'closed' },
]

describe('ThreadList', () => {
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

  test('clicking a row calls onSelect with that thread', () => {
    const onSelect = vi.fn()
    render(<ThreadList threads={threads} participants={[]} onSelect={onSelect} />)
    fireEvent.click(screen.getByText('Hand-off'))
    expect(onSelect).toHaveBeenCalledWith(threads[0])
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
