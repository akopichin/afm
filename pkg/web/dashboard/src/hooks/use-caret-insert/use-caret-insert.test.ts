import { act, renderHook } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { useCaretInsert } from './use-caret-insert'

describe('useCaretInsert', () => {
  it('inserts at the current caret position without clobbering surrounding text', () => {
    const onChange = vi.fn()
    const { result } = renderHook(() => useCaretInsert('see  end', onChange))

    const el = { selectionStart: 4 } as HTMLTextAreaElement
    result.current.nodeRef.current = el

    act(() => {
      result.current.insertAtCaret('[AFM file: "/w/a.go"]')
    })

    expect(onChange).toHaveBeenCalledWith('see [AFM file: "/w/a.go"] end')
  })

  it('falls back to appending at the end when no node/caret is known', () => {
    const onChange = vi.fn()
    const { result } = renderHook(() => useCaretInsert('hello', onChange))

    act(() => {
      result.current.insertAtCaret(' world')
    })

    expect(onChange).toHaveBeenCalledWith('hello world')
  })
})
