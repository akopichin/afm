import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import type { TreeEntry } from '../../api/files-client'
import { FileBrowserModal, type SelectedFile } from './FileBrowserModal'
import { FilesApiMock } from './test-support'

function renderModal(overrides: Partial<React.ComponentProps<typeof FileBrowserModal>> = {}) {
  const props: React.ComponentProps<typeof FileBrowserModal> = {
    mode: 'browse',
    selection: [],
    onToggleSelect: vi.fn(),
    onRemoveSelect: vi.fn(),
    onClose: vi.fn(),
    onSubmit: vi.fn(),
    ...overrides,
  }
  return render(<FileBrowserModal {...props} />)
}

describe('FileBrowserModal', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('lazy-loads roots and tree, then highlights the selected file (escaped, hljs class present)', async () => {
    const api = new FilesApiMock()
    api.setRoots([{ id: 'project', label: 'afm' }])
    api.setTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go' }])
    api.setContent('project', 'a.go', { language: 'go', content: 'package a' })
    api.install()

    const { container } = renderModal()

    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    fireEvent.click(await screen.findByText('a.go'))

    await waitFor(() => expect(container.querySelector('code.hljs.language-go')).not.toBeNull())
    expect(container.querySelector('code')?.textContent).toBe('package a')
    // root label always in the breadcrumb, next to the relative path
    expect(container.querySelector('.file-browser-breadcrumb')?.textContent).toBe('afm / a.go')
  })

  test('does not execute source as HTML for a file whose content looks like a script/img payload', async () => {
    const api = new FilesApiMock()
    api.setRoots([{ id: 'project', label: 'afm' }])
    api.setTree('project', '.', [{ name: 'x.ts', path: 'x.ts', kind: 'file', language: 'typescript' }])
    api.setContent('project', 'x.ts', { language: 'typescript', content: '<img src=x onerror="window.__pwned=1">' })
    api.install()
    const w = window as unknown as { __pwned?: number }
    delete w.__pwned

    const { container } = renderModal()

    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    fireEvent.click(await screen.findByText('x.ts'))

    await waitFor(() => expect(container.querySelector('code')).not.toBeNull())
    expect(container.querySelector('img')).toBeNull()
    expect(w.__pwned).toBeUndefined()
  })

  test('switches to the DIFF tab and fetches the diff for the active file', async () => {
    const api = new FilesApiMock()
    api.setRoots([{ id: 'project', label: 'afm' }])
    api.setTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go' }])
    api.setContent('project', 'a.go', { language: 'go', content: 'package a' })
    api.setDiff('project', 'a.go', { status: 'modified', diff: '--- a\n+++ b\n@@ -1 +1 @@\n-old\n+new\n' })
    api.install()

    renderModal()

    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    fireEvent.click(await screen.findByText('a.go'))

    fireEvent.click(screen.getByRole('tab', { name: /^diff$/i }))

    expect(await screen.findByText('+new')).toBeInTheDocument()
  })

  test('shows a dedicated banner when the diff is unavailable (409)', async () => {
    const api = new FilesApiMock()
    api.setRoots([{ id: 'project', label: 'afm' }])
    api.setTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go' }])
    api.setContent('project', 'a.go', { language: 'go', content: 'package a' })
    api.setDiffError('project', 'a.go', 'diff_unavailable', 409)
    api.install()

    renderModal()

    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    fireEvent.click(await screen.findByText('a.go'))
    fireEvent.click(screen.getByRole('tab', { name: /^diff$/i }))

    expect(await screen.findByText(/not available/i)).toBeInTheDocument()
  })

  test('checkbox click delegates to onToggleSelect with the root and the entry', async () => {
    const api = new FilesApiMock()
    api.setRoots([{ id: 'project', label: 'afm' }])
    api.setTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go' }])
    api.install()
    const onToggleSelect = vi.fn()

    renderModal({ onToggleSelect })

    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    fireEvent.click(await screen.findByRole('checkbox', { name: /a\.go/ }))

    expect(onToggleSelect).toHaveBeenCalledWith('project', expect.objectContaining({ path: 'a.go' } satisfies Partial<TreeEntry>))
  })

  test('renders a chip per selected file and calls onSubmit on the primary action', async () => {
    const api = new FilesApiMock()
    api.setRoots([{ id: 'project', label: 'afm' }])
    api.install()
    const selection: SelectedFile[] = [{ root: 'project', path: 'a.go', displayPath: 'a.go', reference: '[AFM file: "/w/afm/a.go"]' }]
    const onSubmit = vi.fn()

    renderModal({ mode: 'picker', selection, onSubmit })
    // Ждём, пока разрешится getRoots() (эффект на маунте) — иначе assert ниже
    // синхронно опережает setRoots из ещё не отработавшего промиса, и React
    // ругается "not wrapped in act(...)" на обновление состояния уже после
    // конца теста.
    await screen.findByRole('button', { name: 'afm' })

    expect(screen.getByText(/a\.go/)).toBeInTheDocument()
    const button = screen.getByRole('button', { name: /insert references/i })
    expect(button).not.toBeDisabled()
    fireEvent.click(button)
    expect(onSubmit).toHaveBeenCalled()
  })

  test('the primary action button is disabled with no selection, and labeled per mode', async () => {
    const api = new FilesApiMock()
    api.setRoots([{ id: 'project', label: 'afm' }])
    api.install()

    renderModal({ mode: 'browse', selection: [] })
    await screen.findByRole('button', { name: 'afm' })

    expect(screen.getByRole('button', { name: /copy references/i })).toBeDisabled()
  })

  test('a chip remove button calls onRemoveSelect with root and path', async () => {
    const api = new FilesApiMock()
    api.setRoots([{ id: 'project', label: 'afm' }])
    api.install()
    const selection: SelectedFile[] = [{ root: 'project', path: 'a.go', displayPath: 'a.go', reference: 'x' }]
    const onRemoveSelect = vi.fn()

    renderModal({ selection, onRemoveSelect })
    await screen.findByRole('button', { name: 'afm' })

    fireEvent.click(screen.getByRole('button', { name: /remove a\.go/i }))
    expect(onRemoveSelect).toHaveBeenCalledWith('project', 'a.go')
  })

  test('renders modifiedAt in the file header on initial open', async () => {
    const api = new FilesApiMock()
    api.setRoots([{ id: 'project', label: 'afm' }])
    api.setTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go' }])
    api.setContent('project', 'a.go', { language: 'go', content: 'package a', modifiedAt: '2026-01-01T12:00:00Z' })
    api.install()

    renderModal()

    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    fireEvent.click(await screen.findByText('a.go'))

    expect(await screen.findByText(/2026/)).toBeInTheDocument()
  })

  test('Reload sends If-None-Match with the etag captured from the last content load', async () => {
    const api = new FilesApiMock()
    api.setRoots([{ id: 'project', label: 'afm' }])
    api.setTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go' }])
    api.setContent('project', 'a.go', { language: 'go', content: 'package a', etag: '"etag-1"' })
    api.install()

    const { container } = renderModal()

    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    fireEvent.click(await screen.findByText('a.go'))
    await waitFor(() => expect(container.querySelector('code')?.textContent).toBe('package a'))

    fireEvent.click(screen.getByRole('button', { name: /reload/i }))

    await waitFor(() => {
      expect(api.fetchMock).toHaveBeenCalledWith(
        expect.stringContaining('/api/files/content?'),
        expect.objectContaining({ headers: { 'If-None-Match': '"etag-1"' } }),
      )
    })
  })

  test('a 304 Reload keeps the current content without a loading flicker', async () => {
    const api = new FilesApiMock()
    api.setRoots([{ id: 'project', label: 'afm' }])
    api.setTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go' }])
    api.setContent('project', 'a.go', { language: 'go', content: 'package a', etag: '"etag-1"' })
    api.install()

    const { container } = renderModal()

    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    fireEvent.click(await screen.findByText('a.go'))
    await waitFor(() => expect(container.querySelector('code')?.textContent).toBe('package a'))

    fireEvent.click(screen.getByRole('button', { name: /reload/i }))

    // 304 must not blank the pane while the reload is in flight or after it resolves.
    expect(screen.queryByText(/loading file/i)).toBeNull()
    expect(container.querySelector('code')?.textContent).toBe('package a')
    await waitFor(() => expect(screen.getByRole('button', { name: /^reload$/i })).not.toBeDisabled())
    expect(container.querySelector('code')?.textContent).toBe('package a')
  })

  test('a 200 Reload replaces content and updates modifiedAt', async () => {
    const api = new FilesApiMock()
    api.setRoots([{ id: 'project', label: 'afm' }])
    api.setTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go' }])
    api.setContent('project', 'a.go', { language: 'go', content: 'package a', etag: '"etag-1"', modifiedAt: '2026-01-01T00:00:00Z' })
    api.install()

    const { container } = renderModal()

    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    fireEvent.click(await screen.findByText('a.go'))
    await waitFor(() => expect(container.querySelector('code')?.textContent).toBe('package a'))

    // the file changes on disk before Reload is clicked — a different etag/content/modifiedAt
    api.setContent('project', 'a.go', { language: 'go', content: 'package b', etag: '"etag-2"', modifiedAt: '2026-02-02T00:00:00Z' })

    fireEvent.click(screen.getByRole('button', { name: /reload/i }))

    await waitFor(() => expect(container.querySelector('code')?.textContent).toBe('package b'))
    expect(await screen.findByText(/2026/)).toBeInTheDocument()
  })

  test('a Reload error is shown inline without destroying the currently-shown content', async () => {
    const api = new FilesApiMock()
    api.setRoots([{ id: 'project', label: 'afm' }])
    api.setTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go' }])
    api.setContent('project', 'a.go', { language: 'go', content: 'package a', etag: '"etag-1"' })
    api.install()

    const { container } = renderModal()

    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    fireEvent.click(await screen.findByText('a.go'))
    await waitFor(() => expect(container.querySelector('code')?.textContent).toBe('package a'))

    api.setContentError('project', 'a.go', 'read_failed', 500)

    fireEvent.click(screen.getByRole('button', { name: /reload/i }))

    expect(await screen.findByText(/read_failed/)).toBeInTheDocument()
    expect(container.querySelector('code')?.textContent).toBe('package a')
  })

  test('Escape closes the modal', async () => {
    const api = new FilesApiMock()
    api.setRoots([{ id: 'project', label: 'afm' }])
    api.install()
    const onClose = vi.fn()

    renderModal({ onClose })
    await screen.findByRole('button', { name: 'afm' })

    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).toHaveBeenCalled()
  })

  test('focus starts inside the modal and Tab wraps from the last to the first focusable element', async () => {
    const api = new FilesApiMock()
    api.setRoots([{ id: 'project', label: 'afm' }])
    api.install()

    const { container } = renderModal()
    await screen.findByRole('button', { name: 'afm' })

    const focusable = container.querySelectorAll<HTMLElement>('button, [tabindex]:not([tabindex="-1"])')
    expect(focusable.length).toBeGreaterThan(0)
    expect(container.contains(document.activeElement)).toBe(true)

    const last = focusable[focusable.length - 1] as HTMLElement
    last.focus()
    fireEvent.keyDown(last, { key: 'Tab' })
    expect(document.activeElement).toBe(focusable[0])
  })
})
