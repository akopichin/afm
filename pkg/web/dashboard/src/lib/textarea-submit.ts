import type { KeyboardEvent } from 'react'

export function handleTextareaSubmit(
  event: KeyboardEvent<HTMLTextAreaElement>,
  onSubmit: () => void,
  canSubmit = true,
): boolean {
  if (event.defaultPrevented || event.key !== 'Enter' || event.shiftKey || event.altKey
    || event.nativeEvent.isComposing || event.nativeEvent.keyCode === 229) return false

  // Consume Enter even when unavailable or held down, so only Shift+Enter inserts a line.
  event.preventDefault()
  if (canSubmit && !event.repeat) onSubmit()
  return true
}
