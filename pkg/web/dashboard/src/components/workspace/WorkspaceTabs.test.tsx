import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { WorkspaceTabs, type WorkspaceTabDescriptor } from './WorkspaceTabs'

const FEED: WorkspaceTabDescriptor = { id: 'feed', label: 'Feed' }
const APPROVAL: WorkspaceTabDescriptor = { id: 'detail', label: 'Approval', kind: 'approval', count: 2, glow: true }

describe('WorkspaceTabs', () => {
  it('renders a tablist with the Feed tab always present', () => {
    render(<WorkspaceTabs tabs={[FEED]} activeId="feed" onSelect={vi.fn()} />)
    expect(screen.getByRole('tablist')).toBeInTheDocument()
    const tab = screen.getByRole('tab', { name: 'Feed' })
    expect(tab).toHaveAttribute('aria-selected', 'true')
  })

  it('marks the attention tab with kind, count and glow', () => {
    render(<WorkspaceTabs tabs={[FEED, APPROVAL]} activeId="feed" onSelect={vi.fn()} />)
    const attn = screen.getByRole('tab', { name: /Approval/ })
    expect(attn).toHaveAttribute('data-kind', 'approval')
    expect(attn.className).toContain('glow')
    expect(attn).toHaveTextContent('· 2')
    // Feed активна, attention — нет.
    expect(attn).toHaveAttribute('aria-selected', 'false')
  })

  it('calls onSelect when a tab is clicked', () => {
    const onSelect = vi.fn()
    render(<WorkspaceTabs tabs={[FEED, APPROVAL]} activeId="feed" onSelect={onSelect} />)
    fireEvent.click(screen.getByRole('tab', { name: /Approval/ }))
    expect(onSelect).toHaveBeenCalledWith('detail')
  })

  it('moves selection with arrow keys (roving tabindex)', () => {
    const onSelect = vi.fn()
    render(<WorkspaceTabs tabs={[FEED, APPROVAL]} activeId="feed" onSelect={onSelect} />)
    const feed = screen.getByRole('tab', { name: 'Feed' })
    expect(feed).toHaveAttribute('tabindex', '0')
    fireEvent.keyDown(feed, { key: 'ArrowRight' })
    expect(onSelect).toHaveBeenCalledWith('detail')
  })

  it('hides the count when it is zero', () => {
    render(<WorkspaceTabs tabs={[FEED, { ...APPROVAL, count: 0 }]} activeId="feed" onSelect={vi.fn()} />)
    expect(screen.getByRole('tab', { name: /Approval/ })).not.toHaveTextContent('·')
  })
})
