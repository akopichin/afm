import { useLayoutEffect, useRef, type MutableRefObject } from 'react'

export type UseCaretInsertResult = {
  nodeRef: MutableRefObject<HTMLTextAreaElement | null>
  insertAtCaret: (text: string) => void
}

// Insert text at the current caret and restore it before paint; intended for one synchronous insertion.
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
