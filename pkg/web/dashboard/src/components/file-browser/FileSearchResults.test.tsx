import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import type { TreeEntry } from '../../api/files-client'
import { FileSearchResults } from './FileSearchResults'

const entry: TreeEntry = { name: 'workspace.go', path: 'pkg/server/workspace.go', kind: 'file', language: 'go', selectable: true }

describe('FileSearchResults', () => {
  test('loading and empty states', () => {
    const { rerender } = render(
      <FileSearchResults result={null} loading error={null} onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />,
    )
    expect(screen.getByText(/Searching/i)).toBeTruthy()
    rerender(
      <FileSearchResults result={{ entries: [], truncated: false }} loading={false} error={null} onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />,
    )
    expect(screen.getByText(/No files match/i)).toBeTruthy()
  })

  test('renders name + muted dir, truncation notice, and wires open/select', () => {
    const onOpenFile = vi.fn()
    const onToggleSelect = vi.fn()
    render(
      <FileSearchResults
        result={{ entries: [entry], truncated: true }}
        loading={false}
        error={null}
        onOpenFile={onOpenFile}
        onToggleSelect={onToggleSelect}
        isSelected={() => false}
        activePath={null}
      />,
    )
    expect(screen.getByText('workspace.go')).toBeTruthy()
    expect(screen.getByText('pkg/server/')).toBeTruthy() // muted parent dir
    expect(screen.getByText(/Some matches may be hidden/i)).toBeTruthy()

    // The openable part is a real <button> — keyboard-focusable, activatable
    // with Enter/Space (unlike the previous non-interactive div).
    const openBtn = screen.getByRole('button', { name: /workspace\.go/i })
    fireEvent.click(openBtn)
    expect(onOpenFile).toHaveBeenCalledWith(entry)

    // The checkbox aria-label carries the FULL path, not just the basename, so
    // identical basenames from different directories are distinguishable.
    fireEvent.click(screen.getByRole('checkbox', { name: /Select pkg\/server\/workspace\.go/i }))
    expect(onToggleSelect).toHaveBeenCalledWith(entry)
  })

  test('error state', () => {
    render(
      <FileSearchResults result={null} loading={false} error="boom" onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />,
    )
    expect(screen.getByText('boom')).toBeTruthy()
  })
})
