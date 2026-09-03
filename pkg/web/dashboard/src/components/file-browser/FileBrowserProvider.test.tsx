import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { FileBrowserProvider, useFileBrowser } from './FileBrowserProvider'
import { FilesApiMock, errorResponse, jsonResponse } from './test-support'

function BrowseHarness() {
  const { openBrowser } = useFileBrowser()
  return (
    <button type="button" onClick={openBrowser}>
      Open browser
    </button>
  )
}

function PickHarness({ onInsert }: { onInsert: (refs: string[]) => void }) {
  const { pickFiles } = useFileBrowser()
  return (
    <button type="button" onClick={() => pickFiles(onInsert)}>
      Pick files
    </button>
  )
}

describe('FileBrowserProvider', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('openBrowser opens the modal in browse mode', async () => {
    new FilesApiMock().install()
    render(
      <FileBrowserProvider flowName="flow1" startedAt="t1">
        <BrowseHarness />
      </FileBrowserProvider>,
    )

    fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))

    expect(await screen.findByRole('dialog', { name: /browse project files/i })).toBeInTheDocument()
  })

  test('useFileBrowser throws when used outside the provider', () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    expect(() => render(<BrowseHarness />)).toThrow(/useFileBrowser/)
    consoleError.mockRestore()
  })

  test('pickFiles → select a file, click Insert references → onInsert receives the collected reference and the modal closes', async () => {
    const api = new FilesApiMock()
    api.setRoots([{ id: 'project', label: 'afm' }])
    api.setTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go' }])
    api.setReference('project', 'a.go', '[AFM file: "/w/afm/a.go"]')
    api.setContent('project', 'a.go', { language: 'go', content: 'package a' })
    api.install()

    const inserted: string[][] = []
    render(
      <FileBrowserProvider flowName="flow1" startedAt="t1">
        <PickHarness onInsert={(refs) => inserted.push(refs)} />
      </FileBrowserProvider>,
    )

    fireEvent.click(screen.getByRole('button', { name: 'Pick files' }))
    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    fireEvent.click(await screen.findByRole('checkbox', { name: /a\.go/ }))

    const insertButton = await screen.findByRole('button', { name: /insert references/i })
    await waitFor(() => expect(insertButton).not.toBeDisabled())
    fireEvent.click(insertButton)

    expect(inserted).toEqual([['[AFM file: "/w/afm/a.go"]']])
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  test('selection is cleared when the run changes (flowName/startedAt)', async () => {
    const api = new FilesApiMock()
    api.setRoots([{ id: 'project', label: 'afm' }])
    api.setTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go' }])
    api.setReference('project', 'a.go', '[AFM file: "/w/afm/a.go"]')
    api.install()

    const { rerender } = render(
      <FileBrowserProvider flowName="flow1" startedAt="t1">
        <BrowseHarness />
      </FileBrowserProvider>,
    )

    fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    fireEvent.click(await screen.findByRole('checkbox', { name: /a\.go/ }))
    await waitFor(() => expect(screen.getByRole('button', { name: /copy references/i })).not.toBeDisabled())

    // New run starts — the flow footer would report a different startedAt.
    rerender(
      <FileBrowserProvider flowName="flow1" startedAt="t2">
        <BrowseHarness />
      </FileBrowserProvider>,
    )

    // The run change also closes the modal (freshly opened next time, empty selection).
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
    await screen.findByRole('button', { name: 'afm' })
    expect(screen.getByRole('button', { name: /copy references/i })).toBeDisabled()
  })

  test('removing a chip via onRemoveSelect drops it from the selection without refetching', async () => {
    const api = new FilesApiMock()
    api.setRoots([{ id: 'project', label: 'afm' }])
    api.setTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go' }])
    api.setReference('project', 'a.go', '[AFM file: "/w/afm/a.go"]')
    api.install()

    render(
      <FileBrowserProvider flowName="flow1" startedAt="t1">
        <BrowseHarness />
      </FileBrowserProvider>,
    )

    fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    fireEvent.click(await screen.findByRole('checkbox', { name: /a\.go/ }))
    await waitFor(() => expect(screen.getByRole('button', { name: /copy references/i })).not.toBeDisabled())

    fireEvent.click(screen.getByRole('button', { name: /remove afm\/a\.go/i }))

    expect(screen.getByRole('button', { name: /copy references/i })).toBeDisabled()
  })

  // Finding 5: capabilities.file_browser=false must gate the provider itself,
  // not just the header button — a defensive no-op in case something still
  // calls openBrowser/pickFiles while disabled (both should be unreachable in
  // practice once callers gate on `enabled`, see AttachFileButton/OpenFileBrowserButton).
  test('enabled=false: openBrowser and pickFiles are no-ops — the modal never opens and the files API is never called', () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch')
    const onInsert = vi.fn()

    render(
      <FileBrowserProvider flowName="flow1" startedAt="t1" enabled={false}>
        <BrowseHarness />
        <PickHarness onInsert={onInsert} />
      </FileBrowserProvider>,
    )

    fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
    fireEvent.click(screen.getByRole('button', { name: 'Pick files' }))

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(fetchSpy).not.toHaveBeenCalled()
  })

  // Finding 7: a pending getReference must not resurrect a stale selection
  // once the run it belongs to is gone — the generation guard is keyed off
  // exactly the flowName/startedAt change that already clears `selection`.
  test('stale getReference: resolving after a run change does not resurrect the selection', async () => {
    let resolveReference: ((response: Response) => void) | undefined
    let referenceCalls = 0
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const rawUrl = typeof input === 'string' ? input : (input as Request).url
      const url = new URL(rawUrl, 'http://localhost')
      if (url.pathname === '/api/files/roots') {
        return jsonResponse({ roots: [{ id: 'project', label: 'afm', kind: 'project', mount_read_only: false }] })
      }
      if (url.pathname === '/api/files/tree') {
        return jsonResponse({ entries: [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go', selectable: true }], next_cursor: '' })
      }
      if (url.pathname === '/api/files/reference') {
        referenceCalls += 1
        return new Promise<Response>((resolve) => {
          resolveReference = resolve
        })
      }
      return errorResponse('not_found', 404)
    })

    const { rerender } = render(
      <FileBrowserProvider flowName="flow1" startedAt="t1">
        <BrowseHarness />
      </FileBrowserProvider>,
    )

    fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    fireEvent.click(await screen.findByRole('checkbox', { name: /a\.go/ }))
    expect(referenceCalls).toBe(1)

    // The run changes WHILE the getReference call for a.go is still in flight
    // (the flow footer would report a new startedAt for a fresh run).
    rerender(
      <FileBrowserProvider flowName="flow1" startedAt="t2">
        <BrowseHarness />
      </FileBrowserProvider>,
    )

    // Now the stale request finally resolves.
    await act(async () => {
      resolveReference?.(jsonResponse({ path: 'a.go', display_path: 'afm/a.go', reference: '[AFM file: "/w/afm/a.go"]' }))
      await Promise.resolve()
    })

    // Reopen the browser for the new run — selection must still be empty,
    // not resurrected by the stale resolve.
    fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
    await screen.findByRole('button', { name: 'afm' })
    expect(screen.getByRole('button', { name: /copy references/i })).toBeDisabled()
  })

  test('stale getReference: resolving after picker submit does not resurrect the selection in the next pick session', async () => {
    let resolveSlowReference: ((response: Response) => void) | undefined
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const rawUrl = typeof input === 'string' ? input : (input as Request).url
      const url = new URL(rawUrl, 'http://localhost')
      if (url.pathname === '/api/files/roots') {
        return jsonResponse({ roots: [{ id: 'project', label: 'afm', kind: 'project', mount_read_only: false }] })
      }
      if (url.pathname === '/api/files/tree') {
        return jsonResponse({
          entries: [
            { name: 'a.go', path: 'a.go', kind: 'file', language: 'go', selectable: true },
            { name: 'b.go', path: 'b.go', kind: 'file', language: 'go', selectable: true },
          ],
          next_cursor: '',
        })
      }
      if (url.pathname === '/api/files/reference') {
        const path = url.searchParams.get('path')
        if (path === 'a.go') {
          return new Promise<Response>((resolve) => {
            resolveSlowReference = resolve
          })
        }
        return jsonResponse({ path, display_path: `afm/${path}`, reference: `[AFM file: "/w/afm/${path}"]` })
      }
      return errorResponse('not_found', 404)
    })

    const inserted: string[][] = []
    render(
      <FileBrowserProvider flowName="flow1" startedAt="t1">
        <PickHarness onInsert={(refs) => inserted.push(refs)} />
      </FileBrowserProvider>,
    )

    fireEvent.click(screen.getByRole('button', { name: 'Pick files' }))
    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    // a.go's getReference stays pending; b.go's resolves immediately.
    fireEvent.click(await screen.findByRole('checkbox', { name: /a\.go/ }))
    fireEvent.click(await screen.findByRole('checkbox', { name: /b\.go/ }))

    const insertButton = await screen.findByRole('button', { name: /insert references/i })
    await waitFor(() => expect(insertButton).not.toBeDisabled())
    fireEvent.click(insertButton)

    // Only b.go's resolved reference was inserted — the picker session ended
    // (and its selection cleared) while a.go was still in flight.
    expect(inserted).toEqual([['[AFM file: "/w/afm/b.go"]']])

    // The stale a.go request resolves only now, after submit.
    await act(async () => {
      resolveSlowReference?.(jsonResponse({ path: 'a.go', display_path: 'afm/a.go', reference: '[AFM file: "/w/afm/a.go"]' }))
      await Promise.resolve()
    })

    // A brand new pick session must start with an empty selection, not with
    // a.go resurrected by the stale resolve from the previous session.
    fireEvent.click(screen.getByRole('button', { name: 'Pick files' }))
    await screen.findByRole('button', { name: 'afm' })
    expect(screen.getByRole('button', { name: /insert references/i })).toBeDisabled()
  })

  test('pending getReference: clicking the same file twice while the first request is in flight fires only one request', async () => {
    let referenceCalls = 0
    let resolveReference: ((response: Response) => void) | undefined
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const rawUrl = typeof input === 'string' ? input : (input as Request).url
      const url = new URL(rawUrl, 'http://localhost')
      if (url.pathname === '/api/files/roots') {
        return jsonResponse({ roots: [{ id: 'project', label: 'afm', kind: 'project', mount_read_only: false }] })
      }
      if (url.pathname === '/api/files/tree') {
        return jsonResponse({ entries: [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go', selectable: true }], next_cursor: '' })
      }
      if (url.pathname === '/api/files/reference') {
        referenceCalls += 1
        return new Promise<Response>((resolve) => {
          resolveReference = resolve
        })
      }
      return errorResponse('not_found', 404)
    })

    render(
      <FileBrowserProvider flowName="flow1" startedAt="t1">
        <BrowseHarness />
      </FileBrowserProvider>,
    )

    fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    const checkbox = await screen.findByRole('checkbox', { name: /a\.go/ })
    fireEvent.click(checkbox)
    fireEvent.click(checkbox)

    expect(referenceCalls).toBe(1)

    await act(async () => {
      resolveReference?.(jsonResponse({ path: 'a.go', display_path: 'afm/a.go', reference: '[AFM file: "/w/afm/a.go"]' }))
      await Promise.resolve()
    })
  })
})
