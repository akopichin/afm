import { act, renderHook } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ClipboardEvent } from 'react'
import { AttachmentUploadError, uploadAttachment } from '../../api/run-client'
import { useImagePaste } from './use-image-paste'

vi.mock('../../api/run-client', async () => {
  const actual = await vi.importActual<typeof import('../../api/run-client')>('../../api/run-client')
  return { ...actual, uploadAttachment: vi.fn() }
})

const mockUpload = uploadAttachment as unknown as ReturnType<typeof vi.fn>

function makeImageItem(type = 'image/png'): DataTransferItem {
  const file = new File([new Uint8Array([1, 2, 3])], 'paste.png', { type })
  return { kind: 'file', type, getAsFile: () => file } as unknown as DataTransferItem
}

function makeTextItem(): DataTransferItem {
  return { kind: 'string', type: 'text/plain', getAsFile: () => null } as unknown as DataTransferItem
}

function makePasteEvent(items: DataTransferItem[], selectionStart = 0): ClipboardEvent<HTMLTextAreaElement> {
  return {
    clipboardData: { items } as unknown,
    currentTarget: { selectionStart } as unknown as HTMLTextAreaElement,
    preventDefault: vi.fn(),
  } as unknown as ClipboardEvent<HTMLTextAreaElement>
}

describe('useImagePaste', () => {
  beforeEach(() => {
    mockUpload.mockReset()
    vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:mock-url')
    vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
  })

  it('ignores a paste with only text items', () => {
    const onChange = vi.fn()
    const { result } = renderHook(() => useImagePaste('s1', '', onChange))
    const event = makePasteEvent([makeTextItem()])

    act(() => {
      result.current.onPaste(event)
    })

    expect(event.preventDefault).not.toHaveBeenCalled()
    expect(onChange).not.toHaveBeenCalled()
    expect(mockUpload).not.toHaveBeenCalled()
  })

  it('uploads a pasted image and inserts a Screenshot reference at the caret', async () => {
    mockUpload.mockResolvedValue({ path: '/afm/run/s1/attachments/paste-1.png' })
    const onChange = vi.fn()
    const { result } = renderHook(() => useImagePaste('s1', 'hello', onChange))
    const event = makePasteEvent([makeImageItem()], 5)

    await act(async () => {
      await result.current.onPaste(event)
    })

    expect(event.preventDefault).toHaveBeenCalled()
    expect(onChange).toHaveBeenCalledWith('hello[Screenshot: /afm/run/s1/attachments/paste-1.png]\n')
  })

  it('keeps a failed (too large) attachment as a retryable chip, does not call onChange', async () => {
    mockUpload.mockRejectedValue(new AttachmentUploadError(413))
    const onChange = vi.fn()
    const { result } = renderHook(() => useImagePaste('s1', '', onChange))
    const event = makePasteEvent([makeImageItem()], 0)

    await act(async () => {
      await result.current.onPaste(event)
    })

    expect(onChange).not.toHaveBeenCalled()
    // Чип НЕ исчезает — остаётся с failed + причиной (Finding #6c).
    expect(result.current.attachments).toHaveLength(1)
    expect(result.current.attachments[0]?.failed).toBe(true)
    expect(result.current.attachments[0]?.errorMsg).toBe('Image too large (max 10 MB)')
  })

  it('reports an unsupported-type reason on the failed chip for a 415 response', async () => {
    mockUpload.mockRejectedValue(new AttachmentUploadError(415))
    const onChange = vi.fn()
    const { result } = renderHook(() => useImagePaste('s1', '', onChange))
    const event = makePasteEvent([makeImageItem()], 0)

    await act(async () => {
      await result.current.onPaste(event)
    })

    expect(result.current.attachments[0]?.errorMsg).toBe('Unsupported image type')
  })

  it('retryAttachment re-uploads the same file and inserts the reference on success', async () => {
    mockUpload.mockRejectedValueOnce(new AttachmentUploadError(0))
    const onChange = vi.fn()
    const { result } = renderHook(() => useImagePaste('s1', '', onChange))
    const event = makePasteEvent([makeImageItem()], 0)

    await act(async () => {
      await result.current.onPaste(event)
    })
    expect(result.current.attachments[0]?.failed).toBe(true)

    // Второй раз загрузка удаётся.
    mockUpload.mockResolvedValueOnce({ path: '/x/retry-1.png' })
    await act(async () => {
      result.current.retryAttachment(result.current.attachments[0]!.id)
      await Promise.resolve()
    })

    expect(onChange).toHaveBeenCalledWith('[Screenshot: /x/retry-1.png]\n')
    expect(result.current.attachments[0]?.failed).toBe(false)
  })

  it('removeAttachment strips exactly the inserted substring for a resolved attachment', async () => {
    mockUpload.mockResolvedValue({ path: '/x/paste-1.png' })
    let value = ''
    const onChange = vi.fn((next: string) => {
      value = next
    })
    const { result, rerender } = renderHook(({ v }) => useImagePaste('s1', v, onChange), {
      initialProps: { v: value },
    })
    const event = makePasteEvent([makeImageItem()], 0)

    await act(async () => {
      await result.current.onPaste(event)
    })
    rerender({ v: value })

    expect(value).toBe('[Screenshot: /x/paste-1.png]\n')
    expect(result.current.attachments).toHaveLength(1)

    act(() => {
      result.current.removeAttachment(result.current.attachments[0]!.id)
    })

    expect(value).toBe('')
  })

  it('removing an in-flight attachment prevents its text from being inserted once the upload resolves', async () => {
    let resolveUpload: (value: { path: string }) => void = () => {}
    mockUpload.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveUpload = resolve
        }),
    )
    const onChange = vi.fn()
    const { result } = renderHook(() => useImagePaste('s1', '', onChange))
    const event = makePasteEvent([makeImageItem()], 0)

    let pastePromise: Promise<void> | undefined
    act(() => {
      pastePromise = result.current.onPaste(event) as unknown as Promise<void>
    })
    expect(result.current.attachments).toHaveLength(1)

    act(() => {
      result.current.removeAttachment(result.current.attachments[0]!.id)
    })
    expect(result.current.attachments).toHaveLength(0)

    await act(async () => {
      resolveUpload({ path: '/x/paste-1.png' })
      await pastePromise
    })

    expect(onChange).not.toHaveBeenCalled()
  })

  it('inserts two pasted images in order, incorporating the parent re-render between them', async () => {
    let resolveFirst: (value: { path: string }) => void = () => {}
    let resolveSecond: (value: { path: string }) => void = () => {}
    mockUpload
      .mockImplementationOnce(() => new Promise((resolve) => { resolveFirst = resolve }))
      .mockImplementationOnce(() => new Promise((resolve) => { resolveSecond = resolve }))

    let value = ''
    const onChange = vi.fn((next: string) => {
      value = next
    })
    const { result, rerender } = renderHook(({ v }) => useImagePaste('s1', v, onChange), {
      initialProps: { v: value },
    })
    const event = makePasteEvent([makeImageItem(), makeImageItem()], 0)

    let pastePromise: Promise<void> | undefined
    act(() => {
      pastePromise = result.current.onPaste(event) as unknown as Promise<void>
    })

    await act(async () => {
      resolveFirst({ path: '/x/paste-1.png' })
      await Promise.resolve()
      await Promise.resolve()
    })
    rerender({ v: value })

    await act(async () => {
      resolveSecond({ path: '/x/paste-2.png' })
      await pastePromise
    })

    expect(value).toBe('[Screenshot: /x/paste-1.png]\n[Screenshot: /x/paste-2.png]\n')
  })

  it('preserves concurrent edits made while upload is pending', async () => {
    let resolveUpload: (value: { path: string }) => void = () => {}
    mockUpload.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveUpload = resolve
        }),
    )
    let value = 'initial'
    const onChange = vi.fn((next: string) => {
      value = next
    })
    const { result, rerender } = renderHook(({ v }) => useImagePaste('s1', v, onChange), {
      initialProps: { v: value },
    })
    const event = makePasteEvent([makeImageItem()], 7)

    // Start the paste (upload pending)
    let pastePromise: Promise<void> | undefined
    act(() => {
      pastePromise = result.current.onPaste(event) as unknown as Promise<void>
    })

    // While upload is still pending, parent updates the value (user types)
    await act(async () => {
      value = 'initial caption'
      rerender({ v: value })
    })

    // Resolve the upload
    await act(async () => {
      resolveUpload({ path: '/x/paste-1.png' })
      await pastePromise
    })

    // Final value should include both the concurrent edit and the screenshot
    expect(value).toBe('initial[Screenshot: /x/paste-1.png]\n caption')
  })
})
