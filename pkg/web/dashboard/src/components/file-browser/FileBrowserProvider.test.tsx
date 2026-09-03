import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { FileBrowserProvider, useFileBrowser } from './FileBrowserProvider'
import { FilesApiMock } from './test-support'

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
})
