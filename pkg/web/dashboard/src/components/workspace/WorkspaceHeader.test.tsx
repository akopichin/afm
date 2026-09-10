import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { WorkspaceHeader } from './WorkspaceHeader'
import type { Stage } from '../../types'

function stage(overrides: Partial<Stage> = {}): Stage {
  return {
    id: 's1', name: 'Build', status: 'awaiting_approval', updatedAt: '', interactive: false,
    autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false,
    isScript: false, pausedFrom: '', preNote: '', buttons: [], ...overrides,
  }
}

describe('WorkspaceHeader', () => {
  it('shows the stage name and status', () => {
    render(<WorkspaceHeader stage={stage()} connected={false} />)
    expect(document.getElementById('detail-title')).toHaveTextContent('Build')
    expect(document.getElementById('detail-status')).toHaveTextContent('Awaiting approval')
  })

  it('shows the thinking indicator only while running and connected', () => {
    const { rerender } = render(<WorkspaceHeader stage={stage({ status: 'running' })} connected={false} />)
    expect(screen.queryByText('thinking')).toBeNull()
    rerender(<WorkspaceHeader stage={stage({ status: 'running' })} connected />)
    expect(screen.getByText('thinking')).toBeInTheDocument()
  })

  it('renders no attention-nav by default', () => {
    render(<WorkspaceHeader stage={stage()} connected={false} />)
    expect(screen.queryByRole('group', { name: /needing attention/i })).toBeNull()
  })

  it('renders prev/next nav with position and wires the callbacks', () => {
    const onPrev = vi.fn()
    const onNext = vi.fn()
    render(<WorkspaceHeader stage={stage()} connected={false} attentionNav={{ pos: 2, total: 3, onPrev, onNext }} />)
    expect(screen.getByText('2 / 3')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Previous item' }))
    fireEvent.click(screen.getByRole('button', { name: 'Next item' }))
    expect(onPrev).toHaveBeenCalledOnce()
    expect(onNext).toHaveBeenCalledOnce()
  })
})
