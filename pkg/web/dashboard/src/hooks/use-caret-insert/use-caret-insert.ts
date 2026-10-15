import { useLayoutEffect, useRef, type MutableRefObject } from 'react'

export type UseCaretInsertResult = {
  nodeRef: MutableRefObject<HTMLTextAreaElement | null>
  insertAtCaret: (text: string) => void
  // replaceRange — like insertAtCaret, but replaces an explicit [start, end) range instead of inserting at
  // the live caret. Used where the range to replace was computed earlier and may no longer match the
  // textarea's current (possibly stale, e.g. after a mouse click elsewhere) selection — the `@mention`
  // menu (T4.2) replaces the typed "@query" token this way. insertAtCaret is just replaceRange at a
  // collapsed [caret, caret) range — same caret-restoration path, no duplicated logic.
  replaceRange: (start: number, end: number, text: string) => void
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

  function replaceRange(start: number, end: number, text: string): void {
    const baseValue = valueRef.current
    const before = baseValue.slice(0, start)
    const after = baseValue.slice(end)
    pendingCaret.current = before.length + text.length
    onChangeRef.current(before + text + after)
  }

  function insertAtCaret(text: string): void {
    const caret = nodeRef.current?.selectionStart ?? valueRef.current.length
    replaceRange(caret, caret, text)
  }

  return { nodeRef, insertAtCaret, replaceRange }
}
