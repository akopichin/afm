import { useState, type KeyboardEvent } from 'react'
import { detectMentionTrigger, type MentionTrigger } from './mention-match'

export type UseMentionMenuOptions<T> = {
  // matchesFor — candidates for the text typed after `@`, in the order the menu shows them.
  matchesFor: (query: string) => T[]
  // textFor — what replaces `@query` in the draft (without the `@` and trailing space).
  textFor: (item: T) => string
  // replaceRange — splices the draft (useCaretInsert's replaceRange).
  replaceRange: (start: number, end: number, text: string) => void
  // onPicked — called after an item was inserted, e.g. to remember the chosen recipient.
  onPicked?: (item: T) => void
}

export type UseMentionMenuResult<T> = {
  // query — the text typed after `@` while a mention is being typed, else null.
  query: string | null
  matches: T[]
  open: boolean
  highlightIndex: number
  setHighlightIndex: (index: number) => void
  // onInput — call from the textarea's onChange with the new value and caret position.
  onInput: (value: string, caret: number) => void
  // onKeyDown — Up/Down/Enter/Escape while the menu is open; returns true when it handled the key.
  onKeyDown: (e: KeyboardEvent<HTMLTextAreaElement>) => boolean
  pick: (item: T) => void
}

// useMentionMenu — the one `@`-mention state machine for every composer (feed, Team conversation, side
// agent): detects the trigger at the caret, tracks the highlighted match and inserts the picked item.
export function useMentionMenu<T>({ matchesFor, textFor, replaceRange, onPicked }: UseMentionMenuOptions<T>): UseMentionMenuResult<T> {
  const [mention, setMention] = useState<MentionTrigger | null>(null)
  const [highlightIndex, setHighlightIndex] = useState(0)
  const matches = mention !== null ? matchesFor(mention.query) : []
  const open = matches.length > 0

  function pick(item: T): void {
    if (mention === null) return
    replaceRange(mention.start, mention.start + 1 + mention.query.length, `@${textFor(item)} `)
    setMention(null)
    onPicked?.(item)
  }

  function onInput(value: string, caret: number): void {
    setMention(detectMentionTrigger(value, caret))
    setHighlightIndex(0)
  }

  function onKeyDown(e: KeyboardEvent<HTMLTextAreaElement>): boolean {
    if (!open) return false
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        setHighlightIndex((highlightIndex + 1) % matches.length)
        return true
      case 'ArrowUp':
        e.preventDefault()
        setHighlightIndex((highlightIndex - 1 + matches.length) % matches.length)
        return true
      case 'Enter': {
        if (e.nativeEvent.isComposing) return false
        e.preventDefault()
        const item = matches[highlightIndex]
        if (item !== undefined) pick(item)
        return true
      }
      case 'Escape':
        // Only closes the menu: stopPropagation keeps a surrounding modal from closing on the same press.
        e.preventDefault()
        e.stopPropagation()
        setMention(null)
        return true
      default:
        return false
    }
  }

  return { query: mention?.query ?? null, matches, open, highlightIndex, setHighlightIndex, onInput, onKeyDown, pick }
}
