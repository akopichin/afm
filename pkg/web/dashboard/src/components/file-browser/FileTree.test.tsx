import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { FileTree } from './FileTree'
import { errorResponse, FilesApiMock, jsonResponse } from './test-support'

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

  test('root-switch race: a late response for a superseded root is dropped, not shown under the new root', async () => {
    let resolveA!: (r: Response) => void
    let resolveB!: (r: Response) => void
    const responseA = new Promise<Response>((resolve) => {
      resolveA = resolve
    })
    const responseB = new Promise<Response>((resolve) => {
      resolveB = resolve
    })

    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const rawUrl = typeof input === 'string' ? input : (input as Request).url
      const url = new URL(rawUrl, 'http://localhost')
      if (url.pathname !== '/api/files/tree') return jsonResponse({ entries: [], next_cursor: '' })
      const root = url.searchParams.get('root') ?? ''
      if (root === 'A') return responseA
      if (root === 'B') return responseB
      return jsonResponse({ entries: [], next_cursor: '' })
    })

    const onOpenFile = vi.fn()
    const { rerender } = render(
      <FileTree root="A" onOpenFile={onOpenFile} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />,
    )

    // Переключаем root ДО того, как ответ для A успел прилететь — запрос для A
    // всё ещё летит, пока рендерится уже B.
    rerender(<FileTree root="B" onOpenFile={onOpenFile} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)

    // B отвечает первым.
    resolveB(jsonResponse({ entries: [{ name: 'b.py', path: 'b.py', kind: 'file', selectable: true }], next_cursor: '' }))
    expect(await screen.findByText('b.py')).toBeInTheDocument()

    // A — устаревший root — отвечает ПОЗЖЕ. Его ответ не должен применяться.
    resolveA(jsonResponse({ entries: [{ name: 'a.go', path: 'a.go', kind: 'file', selectable: true }], next_cursor: '' }))
    await act(async () => {
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(screen.queryByText('a.go')).not.toBeInTheDocument()
    expect(screen.getByText('b.py')).toBeInTheDocument()

    // Открытие всё ещё соответствует актуальному (B) дереву, а не примешанным путям A.
    fireEvent.click(screen.getByText('b.py'))
    expect(onOpenFile).toHaveBeenCalledWith(expect.objectContaining({ path: 'b.py' }))
  })

  test('a "Load more" failure keeps already-loaded entries and offers Retry that resumes from the same cursor', async () => {
    let page2Attempts = 0
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const rawUrl = typeof input === 'string' ? input : (input as Request).url
      const url = new URL(rawUrl, 'http://localhost')
      if (url.pathname !== '/api/files/tree') return jsonResponse({ entries: [], next_cursor: '' })
      const cursor = url.searchParams.get('cursor') ?? ''
      if (cursor === '') {
        return jsonResponse({ entries: [{ name: 'a.go', path: 'a.go', kind: 'file', selectable: true }], next_cursor: 'page2' })
      }
      page2Attempts += 1
      if (page2Attempts === 1) return errorResponse('read_failed', 500)
      return jsonResponse({ entries: [{ name: 'b.go', path: 'b.go', kind: 'file', selectable: true }], next_cursor: '' })
    })

    render(<FileTree root="project" onOpenFile={vi.fn()} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)

    await screen.findByText('a.go')
    fireEvent.click(screen.getByRole('button', { name: /load more/i }))

    const retryButton = await screen.findByRole('button', { name: /retry/i })
    // Уже загруженные записи никуда не делись после ошибки пагинации.
    expect(screen.getByText('a.go')).toBeInTheDocument()

    fireEvent.click(retryButton)

    expect(await screen.findByText('b.go')).toBeInTheDocument()
    expect(page2Attempts).toBe(2)
  })

  test('a non-selectable file entry (special file) does not attempt to open on click', async () => {
    const api = new FilesApiMock()
    api.setTree('project', '.', [{ name: 'pipe', path: 'pipe', kind: 'file', selectable: false }])
    api.install()
    const onOpenFile = vi.fn()

    render(<FileTree root="project" onOpenFile={onOpenFile} onToggleSelect={vi.fn()} isSelected={() => false} activePath={null} />)

    fireEvent.click(await screen.findByText('pipe'))
    expect(onOpenFile).not.toHaveBeenCalled()
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
