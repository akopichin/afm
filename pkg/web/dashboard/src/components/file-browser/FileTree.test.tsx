import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { FileTree } from './FileTree'
import { FilesApiMock } from './test-support'

describe('FileTree', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('lazy-loads the top-level entries of the given root on mount', async () => {
    const api = new FilesApiMock()
    api.setRoots([{ id: 'project', label: 'afm' }])
    api.setTree('project', '.', [
      { name: 'src', path: 'src', kind: 'directory' },
      { name: 'a.go', path: 'a.go', kind: 'file', language: 'go' },
    ])
    api.install()

    render(<FileTree root="project" onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)

    expect(await screen.findByText('a.go')).toBeInTheDocument()
    expect(screen.getByText('src')).toBeInTheDocument()
  })

  test('expanding a directory lazily fetches and renders its children, indented', async () => {
    const api = new FilesApiMock()
    api.setTree('project', '.', [{ name: 'src', path: 'src', kind: 'directory' }])
    api.setTree('project', 'src', [{ name: 'main.go', path: 'src/main.go', kind: 'file', language: 'go' }])
    api.install()

    render(<FileTree root="project" onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)

    const dirRow = await screen.findByText('src')
    expect(screen.queryByText('main.go')).not.toBeInTheDocument()

    fireEvent.click(dirRow)

    expect(await screen.findByText('main.go')).toBeInTheDocument()
  })

  test('paginates a directory listing via "Load more" / next_cursor', async () => {
    const api = new FilesApiMock()
    api.setTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go' }], { nextCursor: 'page2' })
    api.setTree('project', '.', [{ name: 'b.go', path: 'b.go', kind: 'file', language: 'go' }], { cursor: 'page2' })
    api.install()

    render(<FileTree root="project" onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)

    await screen.findByText('a.go')
    expect(screen.queryByText('b.go')).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /load more/i }))

    expect(await screen.findByText('b.go')).toBeInTheDocument()
    // a.go is still there — pagination appends, it doesn't replace
    expect(screen.getByText('a.go')).toBeInTheDocument()
  })

  test('clicking a file row opens a preview via onOpenFile, not a checkbox toggle', async () => {
    const api = new FilesApiMock()
    api.setTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go' }])
    api.install()
    const onOpenFile = vi.fn()

    render(<FileTree root="project" onOpenFile={onOpenFile} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)

    fireEvent.click(await screen.findByText('a.go'))

    expect(onOpenFile).toHaveBeenCalledWith(expect.objectContaining({ path: 'a.go' }))
  })

  test('clicking the checkbox selects without opening a preview', async () => {
    const api = new FilesApiMock()
    api.setTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go' }])
    api.install()
    const onOpenFile = vi.fn()
    const onToggleSelect = vi.fn()

    render(<FileTree root="project" onOpenFile={onOpenFile} onToggleSelect={onToggleSelect} isSelected={() => false} activePath={null} />)

    fireEvent.click(await screen.findByRole('checkbox', { name: /a\.go/ }))

    expect(onToggleSelect).toHaveBeenCalledWith(expect.objectContaining({ path: 'a.go' }))
    expect(onOpenFile).not.toHaveBeenCalled()
  })

  test('a symlink entry has no checkbox and is not clickable to preview', async () => {
    const api = new FilesApiMock()
    api.setTree('project', '.', [{ name: 'link', path: 'link', kind: 'symlink', selectable: false }])
    api.install()
    const onOpenFile = vi.fn()

    render(<FileTree root="project" onOpenFile={onOpenFile} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)

    const row = await screen.findByText('link')
    const rowEl = row.closest('[data-kind="symlink"]') as HTMLElement
    expect(within(rowEl).queryByRole('checkbox')).not.toBeInTheDocument()

    fireEvent.click(row)
    expect(onOpenFile).not.toHaveBeenCalled()
  })

  test('resets and reloads when the root changes', async () => {
    const api = new FilesApiMock()
    api.setTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go' }])
    api.setTree('other', '.', [{ name: 'b.py', path: 'b.py', kind: 'file', language: 'python' }])
    api.install()

    const { rerender } = render(<FileTree root="project" onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)
    await screen.findByText('a.go')

    rerender(<FileTree root="other" onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)

    expect(await screen.findByText('b.py')).toBeInTheDocument()
    expect(screen.queryByText('a.go')).not.toBeInTheDocument()
  })

  test('keyboard: ArrowDown moves focus to the next row, Enter opens a file', async () => {
    const api = new FilesApiMock()
    api.setTree('project', '.', [
      { name: 'a.go', path: 'a.go', kind: 'file', language: 'go' },
      { name: 'b.go', path: 'b.go', kind: 'file', language: 'go' },
    ])
    api.install()
    const onOpenFile = vi.fn()

    render(<FileTree root="project" onOpenFile={onOpenFile} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)

    const first = await screen.findByText('a.go')
    const firstRow = first.closest('[role="treeitem"]') as HTMLElement
    act(() => firstRow.focus())

    fireEvent.keyDown(firstRow, { key: 'ArrowDown' })
    const secondRow = screen.getByText('b.go').closest('[role="treeitem"]') as HTMLElement
    expect(secondRow).toHaveFocus()

    fireEvent.keyDown(secondRow, { key: 'Enter' })
    expect(onOpenFile).toHaveBeenCalledWith(expect.objectContaining({ path: 'b.go' }))
  })
})
