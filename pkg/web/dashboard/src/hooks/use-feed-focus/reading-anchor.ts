// What the Feed renderers mark as one message or row: the unit whose top the reader keeps in place.
const FEED_ITEM_SELECTOR = '[data-feed-item], [data-message-id]'

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

// readingElement — the element in the middle of the scroller: the reader is looking there, and the text re-wrapped
// by a width change moves everything else around it.
export function readingElement(scroller: HTMLElement): Element | null {
  if (scroller.clientHeight === 0) return null
  const middle = scroller.getBoundingClientRect().top + scroller.clientHeight / 2
  let element = itemNearest(scroller, middle)
  // A message taller than the Feed is too coarse to hold: the text being read sits deeper.
  while (element !== null && element.getBoundingClientRect().height > scroller.clientHeight) {
    const deeper = childNearest(element, middle)
    if (deeper === null) break
    element = deeper
  }
  return element
}
