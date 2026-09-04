import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { ChangedFilesList } from './ChangedFilesList'
import type { ChangeList } from '../../api/files-client'

const list: ChangeList = {
  entries: [
    { name: 'a.go', path: 'a.go', kind: 'file', status: 'modified', selectable: true },
    { name: 'gone.go', path: 'dir/gone.go', kind: 'file', status: 'deleted', selectable: false },
  ],
  truncated: false,
}

describe('ChangedFilesList', () => {
  it('renders status badges and the directory prefix', () => {
    render(<ChangedFilesList result={list} loading={false} error={null} onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)
    expect(screen.getByText('M')).toBeInTheDocument()
    expect(screen.getByText('D')).toBeInTheDocument()
    expect(screen.getByText('dir/')).toBeInTheDocument()
  })

  it('opens a selectable file on click', () => {
    const onOpen = vi.fn()
    render(<ChangedFilesList result={list} loading={false} error={null} onOpenFile={onOpen} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)
    fireEvent.click(screen.getByRole('button', { name: /a\.go/ }))
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ path: 'a.go' }))
  })

  it('does not render an open button or checkbox for a deleted (non-selectable) row', () => {
    render(<ChangedFilesList result={list} loading={false} error={null} onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)
    expect(screen.queryByRole('button', { name: /gone\.go/ })).toBeNull()
    expect(screen.queryByRole('checkbox', { name: /gone\.go/ })).toBeNull()
    expect(screen.getByText(/Deleted or unavailable/i)).toBeInTheDocument()
  })

  it('exposes the change status to assistive tech, not only via badge color', () => {
    render(<ChangedFilesList result={list} loading={false} error={null} onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)
    // The selectable row's button accessible name carries the status word
    // (the visible badge letter is aria-hidden, so color/letter alone would
    // leave a screen-reader user unable to tell added from modified/deleted).
    expect(screen.getByRole('button', { name: /Modified: a\.go/ })).toBeInTheDocument()
    // The deleted (non-button) row voices its status too.
    expect(screen.getByText(/Deleted:/)).toBeInTheDocument()
  })

  it('shows loading, empty, error and truncation states', () => {
    const { rerender } = render(<ChangedFilesList result={null} loading error={null} onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)
    expect(screen.getByText('Loading changes…')).toBeInTheDocument()

    rerender(<ChangedFilesList result={{ entries: [], truncated: false }} loading={false} error={null} onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)
    expect(screen.getByText('No changes')).toBeInTheDocument()

    rerender(<ChangedFilesList result={null} loading={false} error="Failed to load changes: read_failed" onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)
    expect(screen.getByText(/Failed to load changes/)).toBeInTheDocument()

    rerender(<ChangedFilesList result={{ entries: list.entries, truncated: true }} loading={false} error={null} onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)
    expect(screen.getByText('Some changes are not shown')).toBeInTheDocument()
  })
})
