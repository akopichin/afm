import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PasteableTextarea } from './PasteableTextarea'

function jsonResponse(data: unknown, ok = true): Response {
  return { ok, json: async () => data } as Response
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
