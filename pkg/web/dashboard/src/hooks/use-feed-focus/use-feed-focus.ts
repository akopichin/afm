import { useCallback, useEffect, useRef, useState } from 'react'
import { isEditableElement } from '../use-is-editing/use-is-editing'

export type FeedFocusOptions = {
  // available — on screen is the Feed, or the Plan / Question view, of a stage (not a helper, not another view).
  available: boolean
  // blocked — an overlay is open above the Feed: a modal, the Assistant, the Stages or People drawer.
  blocked: boolean
}

export type FeedFocus = {
  active: boolean
  toggle: () => void
}

// Focus mode keeps only the stage toolbar and the view of the stage (Feed column with its composer, Plan, Question).
const TOGGLE_SELECTOR = '.feed-focus-toggle'
const VIEW_SELECTOR = '.workspace-primary'

// isFieldTarget — the key went to a form field, where F is typed text or the control's own action.
function isFieldTarget(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false
  return isEditableElement(target) || target.closest('input, textarea, select, [role="textbox"]') !== null
}

// isFocusKey — the physical F key, so it works on any layout; modified or auto-repeated presses are not it.
function isFocusKey(e: KeyboardEvent): boolean {
  return e.code === 'KeyF' && !e.shiftKey && !e.ctrlKey && !e.altKey && !e.metaKey && !e.repeat && !isFieldTarget(e.target)
}

// useFeedFocus — the focus mode: F toggles it, Escape leaves it, and leaving the Feed / Plan / Question views ends it.
export function useFeedFocus({ available, blocked }: FeedFocusOptions): FeedFocus {
  const [active, setActive] = useState(false)
  // Another view or a helper ends the mode; back in the Feed the user turns it on again.
  if (active && !available) setActive(false)

  // Read at key time, so the listener is added once.
  const current = useRef({ active, available, blocked })
  current.current = { active, available, blocked }

  // The Feed list keeps the reader's place itself when the layout changes (it watches its own size).
  const change = useCallback((next: boolean) => setActive(next), [])

  useEffect(() => {
    // On window while bubbling: mention and attachment menus, modals and drawers handle the key first.
    function onKeyDown(e: KeyboardEvent): void {
      const { active, available, blocked } = current.current
      if (e.defaultPrevented || e.isComposing || e.keyCode === 229) return
      if (!available || blocked || document.querySelector('dialog[open]') !== null) return
      const exits = active && e.key === 'Escape'
      if (!exits && !isFocusKey(e)) return
      e.preventDefault()
      change(!active)
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [change])

  // Keyboard focus in an area focus mode hides (header, tabs, Stages, People) would be lost: hand it to the button.
  useEffect(() => {
    if (!active) return
    const focused = document.activeElement
    if (focused !== null && focused.closest(`${VIEW_SELECTOR}, ${TOGGLE_SELECTOR}`) !== null) return
    document.querySelector<HTMLElement>(TOGGLE_SELECTOR)?.focus()
  }, [active])

  const toggle = useCallback(() => change(!current.current.active), [change])
  return { active, toggle }
}
