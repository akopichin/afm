import { act, fireEvent, render as rtlRender, screen, waitFor, type RenderResult } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { CommentProvider } from '../comments/CommentProvider'
import { CommentRecipientsContext } from '../comments/comment-recipients'
import { SideAgentContext, type SideAgentContextValue } from '../side-agent/side-agent-context'
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

// Files внутри читают комментарии из run-scoped CommentProvider, как в App.
function render(ui: React.ReactElement): RenderResult {
  return rtlRender(ui, { wrapper: ({ children }) => <CommentProvider runId="r1">{children}</CommentProvider> })
}

// filesUnderRun — для теста смены рана: runId задаёт сам тест.
function filesUnderRun(runId: string, startedAt: string) {
  return (
    <CommentProvider runId={runId}>
      <FileBrowserProvider flowName="flow1" startedAt={startedAt}>
        <BrowseHarness />
      </FileBrowserProvider>
    </CommentProvider>
  )
}

function FilesHarness(): React.ReactElement {
  return (
    <FileBrowserProvider flowName="flow1" startedAt="t1">
      <BrowseHarness />
    </FileBrowserProvider>
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

async function commentOnLine(line: number, text: string): Promise<void> {
  fireEvent.click(await screen.findByRole('button', { name: `Comment on line ${line}` }))
  fireEvent.change(screen.getByPlaceholderText(`Comment on line ${line}...`), { target: { value: text } })
  fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))
}

async function openFiles(): Promise<void> {
  fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
  fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
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

  describe('file comments', () => {
    test('a line click opens a local comment immediately — no pause confirmation, no Without pause mode', async () => {
      installOneFile()
      render(<FilesHarness />)

      await openFiles()
      await openBrowseFile('a.go')
      fireEvent.click(await screen.findByRole('button', { name: 'Comment on line 1' }))

      expect(screen.getByPlaceholderText('Comment on line 1...')).toBeInTheDocument()
      expect(screen.queryByText(/pausing the flow/i)).toBeNull()
      expect(screen.queryByText('Without pause')).toBeNull()
      expect(screen.queryByText('Send to assistant')).toBeNull()
    })

    test('comments and unsaved text survive closing and reopening Files', async () => {
      installTwoFiles()
      render(<FilesHarness />)

      await openFiles()
      await openBrowseFile('a.go')
      await commentOnLine(1, 'saved on a')
      await openBrowseFile('b.go')
      fireEvent.click(await screen.findByRole('button', { name: 'Comment on line 2' }))
      fireEvent.change(screen.getByPlaceholderText('Comment on line 2...'), { target: { value: 'typed on b' } })

      fireEvent.click(screen.getByRole('button', { name: 'Close' }))
      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
      await openFiles()
      await openBrowseFile('b.go')
      expect(await screen.findByText('typed on b')).toBeInTheDocument()
      expect(screen.getByText('Draft')).toBeInTheDocument()
      await openBrowseFile('a.go')
      expect(await screen.findByText('saved on a')).toBeInTheDocument()
    })

    test('a new run closes Files and drops its comments', async () => {
      installOneFile()
      const { rerender } = rtlRender(filesUnderRun('r1', 't1'))
      await openFiles()
      await openBrowseFile('a.go')
      await commentOnLine(1, 'old run note')

      rerender(filesUnderRun('r2', 't2'))
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()

      await openFiles()
      await openBrowseFile('a.go')
      expect(await screen.findByTestId('file-line-1')).toBeInTheDocument()
      expect(screen.queryByText('old run note')).toBeNull()
    })
  })

  describe('sending file comments', () => {
    const assistant = (sendExactMessage: SideAgentContextValue['sendExactMessage']): SideAgentContextValue => ({
      enabled: true, isOpen: false, open: () => {}, openWith: () => {}, sendExactMessage, unseenResult: false, active: false,
    })

    function renderWithAssistant(sendExactMessage: SideAgentContextValue['sendExactMessage'] = () => Promise.resolve()) {
      return rtlRender(
        <SideAgentContext.Provider value={assistant(sendExactMessage)}>
          <CommentRecipientsContext.Provider value={{ runId: 'r1', participants: [], stages: [] }}>
            <CommentProvider runId="r1">
              <FilesHarness />
            </CommentProvider>
          </CommentRecipientsContext.Provider>
        </SideAgentContext.Provider>,
      )
    }

    async function twoComments(): Promise<void> {
      await openFiles()
      await openBrowseFile('a.go')
      await commentOnLine(2, 'note on a')
      await openBrowseFile('b.go')
      await commentOnLine(1, 'note on b')
    }

    test('the footer counts the comments and Send all is disabled without any', async () => {
      installTwoFiles()
      renderWithAssistant()
      await openFiles()
      expect(screen.getByRole('button', { name: 'Send all (0)' })).toBeDisabled()

      await openBrowseFile('a.go')
      await commentOnLine(1, 'x')
      expect(screen.getByRole('button', { name: 'Send all (1)' })).toBeEnabled()
    })

    test('Send all sends one message to the chosen agent; closing Files first still clears only that packet', async () => {
      installTwoFiles()
      let resolve!: () => void
      const sendExactMessage = vi.fn(() => new Promise<void>((r) => { resolve = r }))
      renderWithAssistant(sendExactMessage)
      await twoComments()

      fireEvent.click(screen.getByRole('button', { name: 'Send all (2)' }))
      const dialog = await screen.findByRole('dialog', { name: 'Send file comments' })
      expect(dialog).toHaveTextContent('2 comments in 2 files')
      const send = await screen.findByRole('button', { name: 'Send 2 comments' })
      await waitFor(() => expect(send).toBeEnabled())
      fireEvent.click(send)
      await waitFor(() => expect(sendExactMessage).toHaveBeenCalledTimes(1))

      // Files закрыт до ответа сервера.
      fireEvent.click(screen.getByRole('button', { name: 'Close send dialog' }))
      fireEvent.click(screen.getByRole('button', { name: 'Close' }))
      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
      await act(async () => resolve())

      await openFiles()
      expect(await screen.findByText('Accepted: 2 comments → Assistant (not read yet)')).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Send all (0)' })).toBeDisabled()
    })

    test('Escape closes only the send dialog and Files stays open; focus returns to the Send button', async () => {
      installTwoFiles()
      renderWithAssistant()
      await openFiles()
      await openBrowseFile('a.go')
      await commentOnLine(1, 'keep me')

      const sendButton = screen.getByRole('button', { name: 'Send comment on line 1' })
      sendButton.focus()
      fireEvent.click(sendButton)
      const dialog = await screen.findByRole('dialog', { name: 'Send file comments' })
      expect(dialog).toHaveTextContent('keep me')

      fireEvent.keyDown(document, { key: 'Escape' })

      await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Send file comments' })).toBeNull())
      expect(screen.getByRole('dialog', { name: /browse project files/i })).toBeInTheDocument()
      expect(sendButton).toHaveFocus()
      expect(screen.getByText('keep me')).toBeInTheDocument()
    })

    test('after a successful card Send focus stays inside Files, not on the page behind', async () => {
      installTwoFiles()
      renderWithAssistant()
      await openFiles()
      await openBrowseFile('a.go')
      await commentOnLine(1, 'only one')

      const card = screen.getByRole('button', { name: 'Send comment on line 1' })
      card.focus()
      fireEvent.click(card)
      const send = await screen.findByRole('button', { name: 'Send 1 comment' })
      await waitFor(() => expect(send).toBeEnabled())
      fireEvent.click(send)

      await screen.findByText('Accepted: 1 comment → Assistant (not read yet)')
      await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Send file comments' })).toBeNull())
      expect(document.activeElement).not.toBe(document.body)
      expect(screen.getByRole('dialog', { name: /browse project files/i }).contains(document.activeElement)).toBe(true)
    })

    test('after Send all of the last comment focus stays inside Files', async () => {
      installTwoFiles()
      renderWithAssistant()
      await openFiles()
      await openBrowseFile('a.go')
      await commentOnLine(1, 'only one')

      const all = screen.getByRole('button', { name: 'Send all (1)' })
      all.focus()
      fireEvent.click(all)
      const send = await screen.findByRole('button', { name: 'Send 1 comment' })
      await waitFor(() => expect(send).toBeEnabled())
      fireEvent.click(send)

      await screen.findByText('Accepted: 1 comment → Assistant (not read yet)')
      await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Send file comments' })).toBeNull())
      expect(document.activeElement).not.toBe(document.body)
      expect(screen.getByRole('dialog', { name: /browse project files/i }).contains(document.activeElement)).toBe(true)
    })

    test('Tab does not leave the send dialog while Files is under it', async () => {
      installTwoFiles()
      renderWithAssistant()
      await openFiles()
      await openBrowseFile('a.go')
      await commentOnLine(1, 'keep me')
      fireEvent.click(screen.getByRole('button', { name: 'Send comment on line 1' }))
      const dialog = await screen.findByRole('dialog', { name: 'Send file comments' })
      await waitFor(() => expect(screen.getByRole('button', { name: 'Send 1 comment' })).toBeEnabled())

      screen.getByRole('button', { name: 'Send 1 comment' }).focus()
      fireEvent.keyDown(document, { key: 'Tab' })

      expect(dialog.contains(document.activeElement)).toBe(true)
    })

    test('a failed send is reported in the footer and reopening the dialog retries it', async () => {
      installTwoFiles()
      const sendExactMessage = vi.fn().mockRejectedValueOnce(new Error('connection lost')).mockResolvedValue(undefined)
      renderWithAssistant(sendExactMessage)
      await openFiles()
      await openBrowseFile('a.go')
      await commentOnLine(1, 'retry me')

      fireEvent.click(screen.getByRole('button', { name: 'Send all (1)' }))
      const send = await screen.findByRole('button', { name: 'Send 1 comment' })
      await waitFor(() => expect(send).toBeEnabled())
      fireEvent.click(send)
      await screen.findByRole('alert')
      fireEvent.click(screen.getByRole('button', { name: 'Close send dialog' }))
      expect(screen.getByRole('status')).toHaveTextContent('Not sent: connection lost')

      fireEvent.click(screen.getByRole('button', { name: 'Send all (1)' }))
      fireEvent.click(await screen.findByRole('button', { name: 'Retry' }))

      await waitFor(() => expect(sendExactMessage).toHaveBeenCalledTimes(2))
      expect(sendExactMessage.mock.calls[1]).toEqual(sendExactMessage.mock.calls[0])
      expect(await screen.findByText('Accepted: 1 comment → Assistant (not read yet)')).toBeInTheDocument()
    })
  })

  describe('browser view across close/reopen', () => {
    function installTwoRoots(): FilesApiMock {
      const api = new FilesApiMock()
      api.setRoots([
        { id: 'project', label: 'afm' },
        { id: 'docs', label: 'docs' },
      ])
      api.setTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go' }])
      api.setTree('docs', '.', [{ name: 'guide.md', path: 'guide.md', kind: 'file', language: 'markdown' }])
      api.setContent('project', 'a.go', { language: 'go', content: 'alpha one\nalpha two' })
      api.setContent('docs', 'guide.md', { language: 'markdown', content: 'guide one\nguide two' })
      api.setDiff('docs', 'guide.md', { diff: '--- a\n+++ b\n' })
      api.install()
      return api
    }

    async function closeFiles(): Promise<void> {
      fireEvent.click(screen.getByRole('button', { name: 'Close' }))
      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    }

    test('reopening restores root, file, tab and scroll position', async () => {
      installTwoRoots()
      render(<FilesHarness />)
      fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
      fireEvent.click(await screen.findByRole('button', { name: 'docs' }))
      await openBrowseFile('guide.md')
      await screen.findByText('guide one')
      const pane = document.querySelector('.file-browser-pane') as HTMLElement
      pane.scrollTop = 120
      fireEvent.scroll(pane)
      await closeFiles()

      fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))

      expect(await screen.findByText('guide one')).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'docs' })).toHaveAttribute('aria-pressed', 'true')
      expect(document.querySelector('.file-browser-breadcrumb')?.textContent).toBe('docs / guide.md')
      expect((document.querySelector('.file-browser-pane') as HTMLElement).scrollTop).toBe(120)
    })

    test('the DIFF tab is restored too', async () => {
      installTwoRoots()
      render(<FilesHarness />)
      fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
      fireEvent.click(await screen.findByRole('button', { name: 'docs' }))
      await openBrowseFile('guide.md')
      fireEvent.click(await screen.findByRole('tab', { name: 'DIFF' }))
      await closeFiles()

      fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))

      expect(await screen.findByRole('tab', { name: 'DIFF' })).toHaveAttribute('aria-selected', 'true')
    })

    test('a slow roots response does not overwrite the restored root', async () => {
      const api = installTwoRoots()
      render(<FilesHarness />)
      fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
      fireEvent.click(await screen.findByRole('button', { name: 'docs' }))
      await closeFiles()

      const passThrough = api.fetchMock!.getMockImplementation()!
      let releaseRoots!: () => void
      const gate = new Promise<void>((resolve) => {
        releaseRoots = resolve
      })
      api.fetchMock!.mockImplementation(async (input, init) => {
        const url = new URL(typeof input === 'string' ? input : (input as Request).url, 'http://localhost')
        if (url.pathname === '/api/files/roots') await gate
        return passThrough(input, init)
      })
      fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
      releaseRoots()

      expect(await screen.findByRole('button', { name: 'docs' })).toHaveAttribute('aria-pressed', 'true')
    })

    test('a new run starts from an empty browser view', async () => {
      installTwoRoots()
      const { rerender } = rtlRender(filesUnderRun('r1', 't1'))
      fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
      fireEvent.click(await screen.findByRole('button', { name: 'docs' }))
      await closeFiles()

      rerender(filesUnderRun('r2', 't2'))
      fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))

      expect(await screen.findByRole('button', { name: 'afm' })).toHaveAttribute('aria-pressed', 'true')
    })
  })
})
