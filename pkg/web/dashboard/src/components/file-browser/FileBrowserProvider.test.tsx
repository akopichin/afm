import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import type { DraftComment } from './draft-comments'
import { renderDraftComments } from './draft-comments'
import { FileBrowserProvider, useFileBrowser } from './FileBrowserProvider'
import { SideAgentContext, type SideAgentContextValue } from '../side-agent/side-agent-context'
import { FilesApiMock, errorResponse, jsonResponse } from './test-support'

function BrowseHarness() {
  const { openBrowser } = useFileBrowser()
  return (
    <button type="button" onClick={openBrowser}>
      Open browser
    </button>
  )
}

function sideAgentValue(overrides: Partial<SideAgentContextValue> = {}): SideAgentContextValue {
  return {
    enabled: false,
    isOpen: false,
    open: vi.fn(),
    openWith: vi.fn(),
    unseenResult: false,
    active: false,
    ...overrides,
  }
}

// renderEnabled wraps FileBrowserProvider in an enabled SideAgentContext — supplied via the leaf module
// `side-agent-context.ts` that SideAgentContext/useSideAgentContext live in, specifically to avoid
// FileBrowserProvider needing to import the real SideAgentProvider (module cycle). A directly-supplied
// context value is the least invasive way to exercise the enabled path from this test file.
function renderEnabled(openWith: (quote: string) => void = vi.fn()) {
  return render(
    <SideAgentContext.Provider value={sideAgentValue({ enabled: true, openWith })}>
      <FileBrowserProvider flowName="flow1" startedAt="t1">
        <BrowseHarness />
      </FileBrowserProvider>
    </SideAgentContext.Provider>,
  )
}

function installOneFile(api: FilesApiMock = new FilesApiMock()): FilesApiMock {
  api.setRoots([{ id: 'project', label: 'afm' }])
  api.setTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go' }])
  api.setContent('project', 'a.go', { language: 'go', content: 'alpha one\nalpha two' })
  api.install()
  return api
}

function installTwoFiles(api: FilesApiMock = new FilesApiMock()): FilesApiMock {
  api.setRoots([{ id: 'project', label: 'afm' }])
  api.setTree('project', '.', [
    { name: 'a.go', path: 'a.go', kind: 'file', language: 'go' },
    { name: 'b.go', path: 'b.go', kind: 'file', language: 'go' },
  ])
  api.setContent('project', 'a.go', { language: 'go', content: 'alpha one\nalpha two' })
  api.setContent('project', 'b.go', { language: 'go', content: 'beta one\nbeta two' })
  api.install()
  return api
}

async function openBrowseFile(name: string): Promise<void> {
  fireEvent.click(await screen.findByText(name))
}

async function startNoPauseOnLine(line: number): Promise<void> {
  const el = await screen.findByTestId(`file-line-${line}`)
  fireEvent.click(el)
  fireEvent.click(screen.getByText('Without pause'))
}

async function typeAndSave(text: string): Promise<void> {
  // Scoped by placeholder, not role: the modal's own file-search input is also an (ambiguous) "textbox".
  const textbox = await screen.findByPlaceholderText(/comment on line/i)
  fireEvent.change(textbox, { target: { value: text } })
  fireEvent.click(screen.queryByText('Save') ?? screen.getByText('Update'))
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
        <PickHarness onInsert={() => {}} />
      </FileBrowserProvider>,
    )

    fireEvent.click(screen.getByRole('button', { name: 'Pick files' }))
    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    fireEvent.click(await screen.findByRole('checkbox', { name: /a\.go/ }))
    await waitFor(() => expect(screen.getByRole('button', { name: /insert references/i })).not.toBeDisabled())

    // New run starts — the flow footer would report a different startedAt.
    rerender(
      <FileBrowserProvider flowName="flow1" startedAt="t2">
        <PickHarness onInsert={() => {}} />
      </FileBrowserProvider>,
    )

    // The run change also closes the modal (freshly opened next time, empty selection).
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Pick files' }))
    await screen.findByRole('button', { name: 'afm' })
    expect(screen.getByRole('button', { name: /insert references/i })).toBeDisabled()
  })

  test('closing the overlay restores focus to the button that opened it (#5)', async () => {
    new FilesApiMock().install()
    render(
      <FileBrowserProvider flowName="flow1" startedAt="t1">
        <BrowseHarness />
      </FileBrowserProvider>,
    )

    const opener = screen.getByRole('button', { name: 'Open browser' })
    opener.focus()
    expect(document.activeElement).toBe(opener)

    fireEvent.click(opener)
    expect(await screen.findByRole('dialog', { name: /browse project files/i })).toBeInTheDocument()

    // Закрываем крестиком — фокус обязан вернуться на кнопку-открыватель, а не
    // «упасть» на body (иначе клавиатурный пользователь теряет место).
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(document.activeElement).toBe(opener)
  })

  test('picker: submitting Insert restores focus to the opener (#5)', async () => {
    const api = new FilesApiMock()
    api.setRoots([{ id: 'project', label: 'afm' }])
    api.setTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go' }])
    api.setReference('project', 'a.go', '[AFM file: "/w/afm/a.go"]')
    api.setContent('project', 'a.go', { language: 'go', content: 'package a' })
    api.install()

    render(
      <FileBrowserProvider flowName="flow1" startedAt="t1">
        <PickHarness onInsert={() => {}} />
      </FileBrowserProvider>,
    )

    const opener = screen.getByRole('button', { name: 'Pick files' })
    opener.focus()
    fireEvent.click(opener)
    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    fireEvent.click(await screen.findByRole('checkbox', { name: /a\.go/ }))
    const insertButton = await screen.findByRole('button', { name: /insert references/i })
    await waitFor(() => expect(insertButton).not.toBeDisabled())
    fireEvent.click(insertButton)

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(document.activeElement).toBe(opener)
  })

  test('removing a chip via onRemoveSelect drops it from the selection without refetching', async () => {
    const api = new FilesApiMock()
    api.setRoots([{ id: 'project', label: 'afm' }])
    api.setTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go' }])
    api.setReference('project', 'a.go', '[AFM file: "/w/afm/a.go"]')
    api.install()

    render(
      <FileBrowserProvider flowName="flow1" startedAt="t1">
        <PickHarness onInsert={() => {}} />
      </FileBrowserProvider>,
    )

    fireEvent.click(screen.getByRole('button', { name: 'Pick files' }))
    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    fireEvent.click(await screen.findByRole('checkbox', { name: /a\.go/ }))
    await waitFor(() => expect(screen.getByRole('button', { name: /insert references/i })).not.toBeDisabled())

    fireEvent.click(screen.getByRole('button', { name: /remove afm\/a\.go/i }))

    expect(screen.getByRole('button', { name: /insert references/i })).toBeDisabled()
  })

  // Finding 5: capabilities.file_browser=false must gate the provider itself, not just the header button —
  // a defensive no-op in case something still calls openBrowser/pickFiles while disabled.
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

  // A late getReference must not restore selection after the run changes.
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
        <PickHarness onInsert={() => {}} />
      </FileBrowserProvider>,
    )

    fireEvent.click(screen.getByRole('button', { name: 'Pick files' }))
    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    fireEvent.click(await screen.findByRole('checkbox', { name: /a\.go/ }))
    expect(referenceCalls).toBe(1)

    // The run changes WHILE the getReference call for a.go is still in flight
    // (the flow footer would report a new startedAt for a fresh run).
    rerender(
      <FileBrowserProvider flowName="flow1" startedAt="t2">
        <PickHarness onInsert={() => {}} />
      </FileBrowserProvider>,
    )

    // Now the stale request finally resolves.
    await act(async () => {
      resolveReference?.(jsonResponse({ path: 'a.go', display_path: 'afm/a.go', reference: '[AFM file: "/w/afm/a.go"]' }))
      await Promise.resolve()
    })

    // Reopen the picker for the new run — selection must still be empty,
    // not resurrected by the stale resolve.
    fireEvent.click(screen.getByRole('button', { name: 'Pick files' }))
    await screen.findByRole('button', { name: 'afm' })
    expect(screen.getByRole('button', { name: /insert references/i })).toBeDisabled()
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
        <PickHarness onInsert={() => {}} />
      </FileBrowserProvider>,
    )

    fireEvent.click(screen.getByRole('button', { name: 'Pick files' }))
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

  describe('no-pause drafts → assistant', () => {
    test('without any side-agent context (default disabled): no "Without pause" button — the feature is invisible', async () => {
      installOneFile()
      render(
        <FileBrowserProvider flowName="flow1" startedAt="t1">
          <BrowseHarness />
        </FileBrowserProvider>,
      )

      fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
      fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
      await openBrowseFile('a.go')
      fireEvent.click(await screen.findByTestId('file-line-1'))

      expect(screen.getByText('Pause')).toBeInTheDocument()
      expect(screen.getByText('Cancel')).toBeInTheDocument()
      expect(screen.queryByText('Without pause')).toBeNull()
    })

    test('with an enabled side-agent context: the "Without pause" button appears, and the mode starts inactive', async () => {
      installOneFile()
      renderEnabled()

      fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
      fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
      await openBrowseFile('a.go')
      fireEvent.click(await screen.findByTestId('file-line-1'))

      expect(screen.getByText('Pause')).toBeInTheDocument()
      expect(screen.getByText('Cancel')).toBeInTheDocument()
      expect(screen.getByText('Without pause')).toBeInTheDocument()
      // Mode is still inactive: the gate confirm is showing, not the editor.
      expect(screen.queryByPlaceholderText(/comment on line/i)).toBeNull()
    })

    test('start(): clicking "Without pause" flips active to true and opens the editor for the clicked line', async () => {
      installOneFile()
      renderEnabled()

      fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
      fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
      await openBrowseFile('a.go')
      await startNoPauseOnLine(1)

      expect(screen.queryByText(/pausing the flow/i)).toBeNull()
      expect(screen.getByPlaceholderText(/comment on line/i)).toBeInTheDocument()
      expect(screen.getByText('Comment on line 1')).toBeInTheDocument()
    })

    test('save(): drafts accumulate across files, and saving the same root/path/line again replaces rather than duplicates', async () => {
      installTwoFiles()
      const { container } = renderEnabled()

      fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
      fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
      await openBrowseFile('a.go')
      await startNoPauseOnLine(1)
      await typeAndSave('first')
      await waitFor(() => expect(container.querySelector('.file-browser-notes-bar')?.textContent).toContain('1 comment in 1 file'))

      // noPauseActive is already true session-wide — switching files and clicking a line opens the editor
      // directly, no gate confirm this time.
      await openBrowseFile('b.go')
      fireEvent.click(await screen.findByTestId('file-line-1'))
      await typeAndSave('second')
      await waitFor(() => expect(container.querySelector('.file-browser-notes-bar')?.textContent).toContain('2 comments in 2 files'))

      // Re-open a.go and overwrite the same line's draft — must replace, not add a third entry.
      await openBrowseFile('a.go')
      fireEvent.click(await screen.findByTestId('file-line-1'))
      await typeAndSave('first-edited')
      await waitFor(() => expect(container.querySelector('.file-browser-notes-bar')?.textContent).toContain('2 comments in 2 files'))
    })

    test('send(): renders the drafts to text, closes the modal, resets the session, and calls the assistant\'s openWith with that text', async () => {
      installTwoFiles()
      const openWith = vi.fn()
      renderEnabled(openWith)

      fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
      fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
      await openBrowseFile('a.go')
      await startNoPauseOnLine(1)
      await typeAndSave('first')
      await openBrowseFile('b.go')
      fireEvent.click(await screen.findByTestId('file-line-1'))
      await typeAndSave('second')

      fireEvent.click(screen.getByRole('button', { name: 'Send to assistant' }))

      const expectedDrafts: DraftComment[] = [
        { root: 'project', path: 'a.go', displayPath: 'afm/a.go', reference: '[AFM file: "project/a.go"]', line: 1, origLineText: 'alpha one', text: 'first' },
        { root: 'project', path: 'b.go', displayPath: 'afm/b.go', reference: '[AFM file: "project/b.go"]', line: 1, origLineText: 'beta one', text: 'second' },
      ]
      expect(openWith).toHaveBeenCalledTimes(1)
      expect(openWith).toHaveBeenCalledWith(renderDraftComments(expectedDrafts))

      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())

      // Reopening starts a clean session: drafts and the no-pause mode are both reset.
      fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
      fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
      await openBrowseFile('a.go')
      const line1 = await screen.findByTestId('file-line-1')
      expect(line1.className).not.toContain('has-comment')
      fireEvent.click(line1)
      expect(screen.getByText('Pause')).toBeInTheDocument()
      expect(screen.queryByPlaceholderText(/comment on line/i)).toBeNull()
    })

    test('clear(): drops drafts and turns the mode off, but does not close the modal', async () => {
      installOneFile()
      renderEnabled()

      fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
      fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
      await openBrowseFile('a.go')
      await startNoPauseOnLine(1)
      await typeAndSave('first')
      expect(await screen.findByText(/1 comment in 1 file/)).toBeInTheDocument()

      fireEvent.click(screen.getByRole('button', { name: 'Clear' }))

      expect(screen.getByRole('dialog')).toBeInTheDocument()
      expect(screen.queryByText(/comment in/)).toBeNull()

      // Mode is off again: the now comment-less line re-asks the pause gate instead of opening the editor.
      const line1 = screen.getByTestId('file-line-1')
      expect(line1.className).not.toContain('has-comment')
      fireEvent.click(line1)
      expect(screen.getByText('Pause')).toBeInTheDocument()
      expect(screen.queryByPlaceholderText(/comment on line/i)).toBeNull()
    })

    test('changing flowName/startedAt resets drafts and the no-pause mode (same effect that resets selection)', async () => {
      installTwoFiles()
      const { rerender } = renderEnabled()

      fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
      fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
      await openBrowseFile('a.go')
      await startNoPauseOnLine(1)
      await typeAndSave('first')
      expect(await screen.findByText(/1 comment in 1 file/)).toBeInTheDocument()

      // New run — the flow footer would report a different startedAt.
      rerender(
        <SideAgentContext.Provider value={sideAgentValue({ enabled: true })}>
          <FileBrowserProvider flowName="flow1" startedAt="t2">
            <BrowseHarness />
          </FileBrowserProvider>
        </SideAgentContext.Provider>,
      )

      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()

      fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
      fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
      await openBrowseFile('a.go')
      const line1 = await screen.findByTestId('file-line-1')
      expect(line1.className).not.toContain('has-comment')
      fireEvent.click(line1)
      expect(screen.getByText('Pause')).toBeInTheDocument()
      expect(screen.queryByPlaceholderText(/comment on line/i)).toBeNull()
    })
  })
})
