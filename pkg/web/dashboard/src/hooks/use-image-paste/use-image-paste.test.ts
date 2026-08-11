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

  it('shows a size-specific error and does not call onChange when the upload is too large', async () => {
    mockUpload.mockRejectedValue(new AttachmentUploadError(413))
    const onChange = vi.fn()
    const { result } = renderHook(() => useImagePaste('s1', '', onChange))
    const event = makePasteEvent([makeImageItem()], 0)

    await act(async () => {
      await result.current.onPaste(event)
    })

    expect(onChange).not.toHaveBeenCalled()
    expect(result.current.uploadError).toBe('Image too large (max 10 MB)')
    expect(result.current.attachments).toHaveLength(0)
  })

  it('shows an unsupported-type error for a 415 response', async () => {
    mockUpload.mockRejectedValue(new AttachmentUploadError(415))
    const onChange = vi.fn()
    const { result } = renderHook(() => useImagePaste('s1', '', onChange))
    const event = makePasteEvent([makeImageItem()], 0)

    await act(async () => {
      await result.current.onPaste(event)
    })

    expect(result.current.uploadError).toBe('Unsupported image type')
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

  it('inserts two pasted images in order', async () => {
    mockUpload.mockResolvedValueOnce({ path: '/x/paste-1.png' }).mockResolvedValueOnce({ path: '/x/paste-2.png' })
    const onChange = vi.fn()
    const { result } = renderHook(() => useImagePaste('s1', '', onChange))
    const event = makePasteEvent([makeImageItem(), makeImageItem()], 0)

    await act(async () => {
      await result.current.onPaste(event)
    })

    expect(onChange).toHaveBeenLastCalledWith('[Screenshot: /x/paste-1.png]\n[Screenshot: /x/paste-2.png]\n')
  })
})
