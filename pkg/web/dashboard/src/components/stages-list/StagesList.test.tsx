import { act, fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import type { Stage } from '../../types'
import { StagesList } from './StagesList'

describe('StagesList', () => {
  test('marks the selected stage active and calls onSelect on click', () => {
    const stages: Stage[] = [
      { id: 's1', name: 'Propose', status: 'done', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
      { id: 's2', name: 'Plan', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
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
      { id: 's1', name: 'Propose', status: 'awaiting_user_input', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
    ]

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} />)

    const item = screen.getByRole('listitem')
    expect(item).toHaveAttribute('data-attention', 'true')
    expect(item.querySelector('.dialog-badge')).toBeInTheDocument()
  })

  test('marks awaiting_approval stage with attention and an approval badge (not a dialog badge)', () => {
    const stages: Stage[] = [
      { id: 's1', name: 'Plan', status: 'awaiting_approval', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
    ]

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} />)

    const item = screen.getByRole('listitem')
    expect(item).toHaveAttribute('data-attention', 'true')
    expect(item.querySelector('.approval-badge')).toBeInTheDocument()
    expect(item.querySelector('.dialog-badge')).not.toBeInTheDocument()
  })

  test('does not mark running stage with attention', () => {
    const stages: Stage[] = [
      { id: 's1', name: 'Run', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
    ]

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} />)

    const item = screen.getByRole('listitem')
    expect(item).not.toHaveAttribute('data-attention', 'true')
  })

  test('does not render stage-name element when name is empty', () => {
    const stages: Stage[] = [
      { id: 's1', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
    ]

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} />)

    expect(screen.getByRole('listitem').querySelector('.stage-name')).not.toBeInTheDocument()
  })

  test('shows the kebab menu only when status is running or awaiting_approval', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
      { id: 'b', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
      { id: 'c', name: '', status: 'awaiting_approval', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} />)
    expect(screen.getAllByRole('button', { name: /more actions/i })).toHaveLength(2) // a и c, не b
  })

  test('CRITICAL: kebab menu portals to document.body so the scrollable #stages-panel cannot clip it', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} />)

    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))

    const menuItem = screen.getByText('Add note for agent')
    const menu = menuItem.closest('ul')
    expect(menu).not.toBeNull()
    // #stages-panel has overflow-y: auto (layout.css) — any descendant that opens
    // below the visible viewport gets clipped. The menu must live outside it.
    expect(document.getElementById('stages-panel')?.contains(menu)).toBe(false)
    expect(document.body.contains(menu)).toBe(true)
  })

  test('clicking outside the open kebab menu closes it', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} />)

    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    expect(screen.getByText('Add note for agent')).toBeInTheDocument()

    fireEvent.mouseDown(document.body)
    expect(screen.queryByText('Add note for agent')).not.toBeInTheDocument()
  })

  test('scrolling the page closes the open kebab menu', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} />)

    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    expect(screen.getByText('Add note for agent')).toBeInTheDocument()

    fireEvent.scroll(window)
    expect(screen.queryByText('Add note for agent')).not.toBeInTheDocument()
  })

  test('переход стадии в done навешивает one-shot класс just-done', async () => {
    const base: Stage[] = [
      { id: 's1', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
    ]
    const { container, rerender } = render(<StagesList stages={base} selectedStageId={null} onSelect={() => {}} />)
    expect(container.querySelector('.stage-item.just-done')).toBeNull()

    const done: Stage[] = [{ ...base[0]!, status: 'done' }]
    rerender(<StagesList stages={done} selectedStageId={null} onSelect={() => {}} />)
    expect(container.querySelector('.stage-item.just-done')).not.toBeNull()
  })

  test('just-done очищается через 700мс даже при промежуточном обновлении stages без нового перехода', () => {
    vi.useFakeTimers()
    try {
      const running: Stage[] = [{ id: 's1', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' }]
      const { container, rerender } = render(<StagesList stages={running} selectedStageId={null} onSelect={() => {}} />)
      const done: Stage[] = [{ ...running[0]!, status: 'done' }]
      act(() => { rerender(<StagesList stages={done} selectedStageId={null} onSelect={() => {}} />) })
      expect(container.querySelector('.stage-item.just-done')).not.toBeNull()
      // промежуточное обновление stages (новый массив, без нового перехода) через 300мс
      act(() => { vi.advanceTimersByTime(300); rerender(<StagesList stages={[{ ...done[0]! }]} selectedStageId={null} onSelect={() => {}} />) })
      // к 700мс от перехода класс должен уйти
      act(() => { vi.advanceTimersByTime(500) })
      expect(container.querySelector('.stage-item.just-done')).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })
})
