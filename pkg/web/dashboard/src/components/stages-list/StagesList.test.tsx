import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import type { Stage } from '../../types'
import { StagesList } from './StagesList'

describe('StagesList', () => {
  test('marks the selected stage active and calls onSelect on click', () => {
    const stages: Stage[] = [
      { id: 's1', name: 'Propose', status: 'done', updatedAt: '', interactive: false, autonomous: false },
      { id: 's2', name: 'Plan', status: 'running', updatedAt: '', interactive: false, autonomous: false },
    ]
    const onSelect = vi.fn()

    render(<StagesList stages={stages} selectedStageId="s2" onSelect={onSelect} />)

    const items = screen.getAllByRole('listitem')
    expect(items[0]).not.toHaveClass('active')
    expect(items[1]).toHaveClass('active')

    fireEvent.click(screen.getByText('Propose'))
    expect(onSelect).toHaveBeenCalledWith('s1')
  })

  test('marks awaiting_user_input stage with attention and a dialog badge', () => {
    const stages: Stage[] = [
      { id: 's1', name: 'Propose', status: 'awaiting_user_input', updatedAt: '', interactive: false, autonomous: false },
    ]

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} />)

    const item = screen.getByRole('listitem')
    expect(item).toHaveAttribute('data-attention', 'true')
    expect(item.querySelector('.dialog-badge')).toBeInTheDocument()
  })

  test('marks awaiting_approval stage with attention but no dialog badge', () => {
    const stages: Stage[] = [
      { id: 's1', name: 'Plan', status: 'awaiting_approval', updatedAt: '', interactive: false, autonomous: false },
    ]

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} />)

    const item = screen.getByRole('listitem')
    expect(item).toHaveAttribute('data-attention', 'true')
    expect(item.querySelector('.dialog-badge')).not.toBeInTheDocument()
  })

  test('does not mark running stage with attention', () => {
    const stages: Stage[] = [
      { id: 's1', name: 'Run', status: 'running', updatedAt: '', interactive: false, autonomous: false },
    ]

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} />)

    const item = screen.getByRole('listitem')
    expect(item).not.toHaveAttribute('data-attention', 'true')
  })

  test('does not render stage-name element when name is empty', () => {
    const stages: Stage[] = [
      { id: 's1', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false },
    ]

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} />)

    expect(screen.getByRole('listitem').querySelector('.stage-name')).not.toBeInTheDocument()
  })
})
