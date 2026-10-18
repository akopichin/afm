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
    render(<ThreadList threads={[]} onSelect={vi.fn()} />)
    expect(screen.getByText('No conversations yet')).toBeInTheDocument()
  })

  test('renders a title or falls back to the thread id, and marks closed threads', () => {
    render(<ThreadList threads={threads} onSelect={vi.fn()} />)
    expect(screen.getByText('Hand-off')).toBeInTheDocument()
    expect(screen.getByText('mt-2')).toBeInTheDocument()
    expect(screen.getByText('Closed')).toBeInTheDocument()
  })

  test('clicking a row calls onSelect with that thread', () => {
    const onSelect = vi.fn()
    render(<ThreadList threads={threads} onSelect={onSelect} />)
    fireEvent.click(screen.getByText('Hand-off'))
    expect(onSelect).toHaveBeenCalledWith(threads[0])
  })
})
