import { isNearBottom } from '../use-stick-to-bottom/use-stick-to-bottom'

const FEED_SCROLL_SELECTOR = '.feed-scroll'
// What the Feed renderers mark as one message or row: the unit whose top the reader keeps in place.
const FEED_ITEM_SELECTOR = '[data-feed-item], [data-message-id]'

export type ReadingAnchor = {
  scroller: HTMLElement
  element: Element
  top: number
}

// childNearest — the child that covers the line, or in a gap between children the closest one.
function childNearest(parent: Element, line: number): Element | null {
  let nearest: Element | null = null
  let nearestDistance = Infinity
  for (const child of Array.from(parent.children)) {
    const { top, bottom, height } = child.getBoundingClientRect()
    // Hidden nodes and decorative leaves (an avatar) are not what the reader is looking at.
    if (height === 0 || (child.childElementCount === 0 && !child.matches(FEED_ITEM_SELECTOR))) continue
    const distance = Math.max(top - line, line - bottom, 0)
    if (distance < nearestDistance) {
      nearest = child
      nearestDistance = distance
    }
  }
  return nearest
}

// itemNearest — the Feed item closest to the line, whatever group it sits in: a container covering the line may
// hold its messages farther away than a neighbouring group's message.
function itemNearest(scroller: Element, line: number): Element | null {
  let nearest: Element | null = null
  let nearestDistance = Infinity
  for (const item of Array.from(scroller.querySelectorAll(FEED_ITEM_SELECTOR))) {
    // An item that holds other items is a container, not the unit the reader looks at.
    if (item.querySelector(FEED_ITEM_SELECTOR) !== null) continue
    const { top, bottom, height } = item.getBoundingClientRect()
    if (height === 0) continue
    const distance = Math.max(top - line, line - bottom, 0)
    if (distance < nearestDistance) {
      nearest = item
      nearestDistance = distance
    }
  }
  return nearest
}

// captureReadingAnchor — the message in the middle of the Feed and where it is on screen: the reader is looking
// there, and the text re-wrapped by a width change moves everything else around it. A Feed stuck to the bottom
// has none: auto-scroll keeps its end in view.
export function captureReadingAnchor(): ReadingAnchor | null {
  const scroller = document.querySelector<HTMLElement>(FEED_SCROLL_SELECTOR)
  if (scroller === null || scroller.clientHeight === 0 || isNearBottom(scroller)) return null
  const middle = scroller.getBoundingClientRect().top + scroller.clientHeight / 2
  let element = itemNearest(scroller, middle)
  // A message taller than the Feed is too coarse to hold: the text being read sits deeper.
  while (element !== null && element.getBoundingClientRect().height > scroller.clientHeight) {
    const deeper = childNearest(element, middle)
    if (deeper === null) break
    element = deeper
  }
  return element === null ? null : { scroller, element, top: element.getBoundingClientRect().top }
}

// HeldAnchor — the message held on entering focus and where it really ended up (the Feed may clamp at its start).
export type HeldAnchor = { anchor: ReadingAnchor; shownTop: number }

export function holdAnchor(anchor: ReadingAnchor): HeldAnchor {
  return { anchor, shownTop: anchor.element.getBoundingClientRect().top }
}

// anchorForLeaving — the message held on entering, if the reader has not scrolled since: it goes back to its
// original place. Otherwise the message in the middle now, as with any other change.
export function anchorForLeaving(held: HeldAnchor | null): ReadingAnchor | null {
  const unmoved = held !== null && held.anchor.element.isConnected && Math.abs(held.anchor.element.getBoundingClientRect().top - held.shownTop) <= 2
  return unmoved ? held.anchor : captureReadingAnchor()
}

// restoreReadingAnchor — scrolls the Feed so the message is back where it was on screen, even when the layout
// moved the Feed or re-wrapped its text.
export function restoreReadingAnchor({ scroller, element, top }: ReadingAnchor): void {
  if (!element.isConnected) return
  scroller.scrollTop += element.getBoundingClientRect().top - top
}
