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
})
