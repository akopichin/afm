// What the feed asked of its virtual list, recorded by the Virtuoso stand-in of the test setup: every scrollToIndex
// location and the initial position of every mounted list. A test may set `move` to run on each location and
// move the scroller with a real scroll event, as the list does.
export const listCalls = { scrolls: [] as unknown[], mounts: [] as unknown[], move: null as ((location: unknown) => void) | null }
// Non-enumerable: the recorded locations are still compared as a plain array.
Object.defineProperty(listCalls.scrolls, 'push', {
  value(this: unknown[], ...locations: unknown[]): number {
    const length = Array.prototype.push.apply(this, locations)
    for (const location of locations) listCalls.move?.(location)
    return length
  },
})

export const resetListCalls = (): void => {
  listCalls.scrolls.length = 0
  listCalls.mounts.length = 0
  listCalls.move = null
}

export const moveScroller = (scroller: HTMLElement, top: number): void => {
  scroller.scrollTop = top
  scroller.dispatchEvent(new Event('scroll'))
}

export const lastScroll = (): unknown => listCalls.scrolls[listCalls.scrolls.length - 1]
export const lastMount = (): unknown => listCalls.mounts[listCalls.mounts.length - 1]
