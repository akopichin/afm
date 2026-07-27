import { act, fireEvent, render, screen } from '@testing-library/react'
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

    render(<StagesList stages={stages} selectedStageId="s2" onSelect={onSelect} agentSuggestEnabled={false} />)

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

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} agentSuggestEnabled={false} />)

    const item = screen.getByRole('listitem')
    expect(item).toHaveAttribute('data-attention', 'true')
    expect(item.querySelector('.dialog-badge')).toBeInTheDocument()
  })

  test('marks awaiting_approval stage with attention and an approval badge (not a dialog badge)', () => {
    const stages: Stage[] = [
      { id: 's1', name: 'Plan', status: 'awaiting_approval', updatedAt: '', interactive: false, autonomous: false },
    ]

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} agentSuggestEnabled={false} />)

    const item = screen.getByRole('listitem')
    expect(item).toHaveAttribute('data-attention', 'true')
    expect(item.querySelector('.approval-badge')).toBeInTheDocument()
    expect(item.querySelector('.dialog-badge')).not.toBeInTheDocument()
  })

  test('does not mark running stage with attention', () => {
    const stages: Stage[] = [
      { id: 's1', name: 'Run', status: 'running', updatedAt: '', interactive: false, autonomous: false },
    ]

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} agentSuggestEnabled={false} />)

    const item = screen.getByRole('listitem')
    expect(item).not.toHaveAttribute('data-attention', 'true')
  })

  test('does not render stage-name element when name is empty', () => {
    const stages: Stage[] = [
      { id: 's1', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false },
    ]

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} agentSuggestEnabled={false} />)

    expect(screen.getByRole('listitem').querySelector('.stage-name')).not.toBeInTheDocument()
  })

  test('shows the kebab menu only when agentSuggestEnabled and status is running or awaiting_approval', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false },
      { id: 'b', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false },
      { id: 'c', name: '', status: 'awaiting_approval', updatedAt: '', interactive: false, autonomous: false },
    ]
    const { rerender } = render(
      <StagesList stages={stages} selectedStageId={null} onSelect={() => {}} agentSuggestEnabled={true} />,
    )
    expect(screen.getAllByRole('button', { name: /more actions/i })).toHaveLength(2) // a и c, не b

    rerender(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} agentSuggestEnabled={false} />)
    expect(screen.queryAllByRole('button', { name: /more actions/i })).toHaveLength(0)
  })

  test('переход стадии в done навешивает one-shot класс just-done', async () => {
    const base: Stage[] = [
      { id: 's1', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false },
    ]
    const { container, rerender } = render(<StagesList stages={base} selectedStageId={null} onSelect={() => {}} agentSuggestEnabled={false} />)
    expect(container.querySelector('.stage-item.just-done')).toBeNull()

    const done: Stage[] = [{ ...base[0]!, status: 'done' }]
    rerender(<StagesList stages={done} selectedStageId={null} onSelect={() => {}} agentSuggestEnabled={false} />)
    expect(container.querySelector('.stage-item.just-done')).not.toBeNull()
  })

  test('just-done очищается через 700мс даже при промежуточном обновлении stages без нового перехода', () => {
    vi.useFakeTimers()
    try {
      const running: Stage[] = [{ id: 's1', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false }]
      const { container, rerender } = render(<StagesList stages={running} selectedStageId={null} onSelect={() => {}} agentSuggestEnabled={false} />)
      const done: Stage[] = [{ ...running[0]!, status: 'done' }]
      act(() => { rerender(<StagesList stages={done} selectedStageId={null} onSelect={() => {}} agentSuggestEnabled={false} />) })
      expect(container.querySelector('.stage-item.just-done')).not.toBeNull()
      // промежуточное обновление stages (новый массив, без нового перехода) через 300мс
      act(() => { vi.advanceTimersByTime(300); rerender(<StagesList stages={[{ ...done[0]! }]} selectedStageId={null} onSelect={() => {}} agentSuggestEnabled={false} />) })
      // к 700мс от перехода класс должен уйти
      act(() => { vi.advanceTimersByTime(500) })
      expect(container.querySelector('.stage-item.just-done')).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })
})
