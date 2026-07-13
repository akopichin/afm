import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import type { Stage } from '../../types'
import { StagesList } from './StagesList'

describe('StagesList', () => {
  test('marks the selected stage active and calls onSelect on click', () => {
    const stages: Stage[] = [
      { id: 's1', name: 'Propose', status: 'done', updatedAt: '' },
      { id: 's2', name: 'Plan', status: 'running', updatedAt: '' },
    ]
    const onSelect = vi.fn()

    render(<StagesList stages={stages} selectedStageId="s2" onSelect={onSelect} />)

    const items = screen.getAllByRole('listitem')
    expect(items[0]).not.toHaveClass('active')
    expect(items[1]).toHaveClass('active')

    fireEvent.click(screen.getByText('Propose'))
    expect(onSelect).toHaveBeenCalledWith('s1')
  })
})
