import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PasteableTextarea } from './PasteableTextarea'
import { FileBrowserProvider } from '../file-browser'
import { FilesApiMock } from '../file-browser/test-support'

function jsonResponse(data: unknown, ok = true): Response {
  return { ok, json: async () => data } as Response
}

function installFileBrowserApi(): void {
  const api = new FilesApiMock()
  api.setRoots([{ id: 'project', label: 'afm' }])
  api.setTree('project', '.', [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go' }])
  api.setReference('project', 'a.go', '[AFM file: "/w/a.go"]')
  api.install()
}

function makeImageItem(): DataTransferItem {
  const file = new File([new Uint8Array([1, 2, 3])], 'paste.png', { type: 'image/png' })
  return { kind: 'file', type: 'image/png', getAsFile: () => file } as unknown as DataTransferItem
}

describe('PasteableTextarea', () => {
  beforeEach(() => {
    vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:mock-url')
    vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('renders the current value and calls onChange on typing', () => {
    const onChange = vi.fn()
    render(<PasteableTextarea stageId="s1" value="hi" onChange={onChange} />)

    const textarea = screen.getByRole('textbox') as HTMLTextAreaElement
    expect(textarea.value).toBe('hi')

    fireEvent.change(textarea, { target: { value: 'hi there' } })
    expect(onChange).toHaveBeenCalledWith('hi there')
  })

  it('passes className/placeholder through to the inner textarea', () => {
    render(<PasteableTextarea stageId="s1" value="" onChange={vi.fn()} className="dialog-custom" placeholder="Or type…" />)

    const textarea = screen.getByPlaceholderText('Or type…')
    expect(textarea).toHaveClass('dialog-custom')
  })

  it('pasting a clipboard image uploads it, shows a thumbnail, and inserts a Screenshot reference', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse({ path: '/afm/run/s1/attachments/paste-1.png' }))
    const onChange = vi.fn()
    render(<PasteableTextarea stageId="s1" value="" onChange={onChange} />)

    fireEvent.paste(screen.getByRole('textbox'), { clipboardData: { items: [makeImageItem()] } })

    await waitFor(() => expect(onChange).toHaveBeenCalledWith('[Screenshot: /afm/run/s1/attachments/paste-1.png]\n'))
    expect(screen.getByAltText('Pasted screenshot')).toBeInTheDocument()
  })

  it('removing an attachment strips its Screenshot reference from the value', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse({ path: '/x/paste-1.png' }))
    let value = ''
    const onChange = vi.fn((next: string) => {
      value = next
    })
    const { rerender } = render(<PasteableTextarea stageId="s1" value={value} onChange={onChange} />)

    fireEvent.paste(screen.getByRole('textbox'), { clipboardData: { items: [makeImageItem()] } })
    await waitFor(() => expect(onChange).toHaveBeenCalled())
    rerender(<PasteableTextarea stageId="s1" value={value} onChange={onChange} />)

    expect(screen.getByAltText('Pasted screenshot')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /remove pasted image/i }))
    expect(value).toBe('')
  })

  it('pasting plain text does not upload or alter the value', () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch')
    const onChange = vi.fn()
    render(<PasteableTextarea stageId="s1" value="hello" onChange={onChange} />)

    fireEvent.paste(screen.getByRole('textbox'), {
      clipboardData: { items: [{ kind: 'string', type: 'text/plain', getAsFile: () => null }] },
    })

    expect(fetchSpy).not.toHaveBeenCalled()
    expect(onChange).not.toHaveBeenCalled()
  })

  it('does not render an attach button when allowFileReferences is omitted, and works without a FileBrowserProvider', () => {
    const onChange = vi.fn()
    render(<PasteableTextarea stageId="s1" value="hi" onChange={onChange} />)

    expect(screen.queryByRole('button', { name: /attach project file/i })).toBeNull()
  })

  it('allowFileReferences: renders an Attach project file button and inserts picked references at the caret without clobbering existing text', async () => {
    installFileBrowserApi()
    const onChange = vi.fn()
    render(
      <FileBrowserProvider flowName="flow1" startedAt="t1">
        <PasteableTextarea stageId="s1" value="see  end" onChange={onChange} allowFileReferences />
      </FileBrowserProvider>,
    )

    const textarea = screen.getByRole('textbox') as HTMLTextAreaElement
    textarea.setSelectionRange(4, 4)

    fireEvent.click(screen.getByRole('button', { name: /attach project file/i }))
    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    fireEvent.click(await screen.findByRole('checkbox', { name: /a\.go/ }))

    const insertButton = await screen.findByRole('button', { name: /insert references/i })
    await waitFor(() => expect(insertButton).not.toBeDisabled())
    fireEvent.click(insertButton)

    expect(onChange).toHaveBeenCalledWith('see [AFM file: "/w/a.go"] end')
  })

  it('stale-picker guard: does not insert into a textarea that unmounted while the picker was still open, and warns instead', async () => {
    installFileBrowserApi()
    const onChange = vi.fn()
    const alertSpy = vi.spyOn(window, 'alert').mockImplementation(() => {})

    function Harness({ show }: { show: boolean }) {
      return (
        <FileBrowserProvider flowName="flow1" startedAt="t1">
          {show && <PasteableTextarea stageId="s1" value="x" onChange={onChange} allowFileReferences />}
        </FileBrowserProvider>
      )
    }

    const { rerender } = render(<Harness show={true} />)

    fireEvent.click(screen.getByRole('button', { name: /attach project file/i }))
    fireEvent.click(await screen.findByRole('button', { name: 'afm' }))
    fireEvent.click(await screen.findByRole('checkbox', { name: /a\.go/ }))

    // The user navigated away from this comment (e.g. switched stage/question)
    // while the picker modal was still open — the target textarea unmounts,
    // but the provider (and hence the still-open modal) stays mounted.
    rerender(<Harness show={false} />)

    const insertButton = await screen.findByRole('button', { name: /insert references/i })
    await waitFor(() => expect(insertButton).not.toBeDisabled())
    fireEvent.click(insertButton)

    expect(onChange).not.toHaveBeenCalled()
    expect(alertSpy).toHaveBeenCalledWith('Target comment is no longer available')
  })

  // Kept last deliberately: vi.spyOn(..., 'get').mockRestore() on this jsdom/
  // tinyspy version does not fully restore the accessor on
  // window.HTMLTextAreaElement.prototype — any later test in this file that
  // reads scrollHeight (useAutoGrowTextarea does, on every render) hits a
  // corrupted getter that recurses into itself and blows the call stack.
  // AgentNoteModal.test.tsx's equivalent test avoids this only by accident
  // (it happens to already be the last test in that file).
  it('grows the textarea to fit its content, same as a plain textarea would', () => {
    const scrollHeightSpy = vi
      .spyOn(window.HTMLTextAreaElement.prototype, 'scrollHeight', 'get')
      .mockReturnValue(180)

    const { rerender } = render(<PasteableTextarea stageId="s1" value="" onChange={vi.fn()} />)
    rerender(<PasteableTextarea stageId="s1" value="a longer note" onChange={vi.fn()} />)

    const textarea = screen.getByRole('textbox') as HTMLTextAreaElement
    expect(textarea.style.height).toBe('180px')

    scrollHeightSpy.mockRestore()
  })
})
