import { useLayoutEffect, useRef, type MutableRefObject } from 'react'

export type UseCaretInsertResult = {
  nodeRef: MutableRefObject<HTMLTextAreaElement | null>
  insertAtCaret: (text: string) => void
}

// Shared caret-splice mechanism, factored out of use-image-paste.ts's pattern:
// insert `text` at the textarea's current caret position via the controlled
// value/onChange pair, then restore the caret to just after the inserted text
// once React re-renders with the new value (useLayoutEffect fires before
// paint, so there's no visible caret jump). use-image-paste keeps its own
// copy of this pattern rather than using this hook — it threads an explicit
// caret across a sequence of async uploads (each image's insertion point
// depends on where the previous one landed), which doesn't fit this hook's
// "read the caret right now" shape. This hook is for the simpler case: a
// single, synchronous insertion — used by PasteableTextarea's "Attach project
// file" button (allowFileReferences, Task 14).
export function useCaretInsert(value: string, onChange: (value: string) => void): UseCaretInsertResult {
  const nodeRef = useRef<HTMLTextAreaElement | null>(null)
  const valueRef = useRef(value)
  valueRef.current = value
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange
  const pendingCaret = useRef<number | null>(null)

  useLayoutEffect(() => {
    if (pendingCaret.current === null) return
    const el = nodeRef.current
    if (el !== null) {
      el.selectionStart = pendingCaret.current
      el.selectionEnd = pendingCaret.current
    }
    pendingCaret.current = null
  }, [value])

  function insertAtCaret(text: string): void {
    const baseValue = valueRef.current
    const caret = nodeRef.current?.selectionStart ?? baseValue.length
    const before = baseValue.slice(0, caret)
    const after = baseValue.slice(caret)
    pendingCaret.current = before.length + text.length
    onChangeRef.current(before + text + after)
  }

  return { nodeRef, insertAtCaret }
}
