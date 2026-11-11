import { createRef } from 'react'
import { afterEach, beforeEach, describe, it, expect, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import { Virtuoso, VirtuosoMockContext, type VirtuosoHandle } from 'react-virtuoso'
import type { AfmEvent } from '../../types'
import type { MeshMessage } from '../../api/mesh-client'
import { buildFeedTimeline, toFeedItems } from './feed-view-model'
import { buildVirtualFeedRows } from './feed-virtual-model'
import { RowFrame } from './FeedRowView'
import { VirtualFeedList, type FeedListHandle } from './VirtualFeedList'
import { followReducer } from './follow-policy'
import { lastScroll, listCalls, moveScroller, resetListCalls } from '../../test/list-spy'

const text = (n: number): AfmEvent =>
  ({ type: 'agent_action', payload: { tool: 'text', detail: `message ${n}` }, stageId: 's1', timestamp: new Date(Date.UTC(2026, 6, 10, 10, 0, n % 60)).toISOString(), seq: n }) as AfmEvent

const rowsOf = (count: number) => buildVirtualFeedRows(buildFeedTimeline(toFeedItems(Array.from({ length: count }, (_, i) => text(i + 1))), [], 'all'), new Set()).rows

function renderList(count: number, props: Partial<Parameters<typeof VirtualFeedList>[0]> = {}, geometry = { viewportHeight: 400, itemHeight: 20 }) {
  const rows = rowsOf(count)
  return render(
    <VirtuosoMockContext.Provider value={geometry}>
      <VirtualFeedList
        rows={rows}
        renderRow={(row, isFirst) => <RowFrame row={row} isFirst={isFirst}>{row.key}</RowFrame>}
        following
        onFollow={() => {}}
        start={null}
        footer={null}
        footerKey=""
        overlay={null}
        {...props}
      />
    </VirtuosoMockContext.Provider>,
  )
}

describe('VirtualFeedList', () => {
  it('mounts far fewer rows than the history holds, and the scroller is the feed root', () => {
    const { container } = renderList(5000)
    const mounted = container.querySelectorAll('[data-virtual-row-key]').length
    expect(mounted).toBeGreaterThan(0)
    expect(mounted).toBeLessThan(80)
    const scroller = container.querySelector('#feed-content')
    expect(scroller).toHaveClass('feed-scroll')
    expect(container.querySelector('.feed-scroll-frame')?.firstElementChild).toBe(scroller)
  })

  it('is a list of list items, and every row is the item itself', () => {
    const { container } = renderList(30)
    expect(screen.getByRole('list', { name: 'Feed events' })).toBeInTheDocument()
    const items = screen.getAllByRole('listitem')
    expect(items.length).toBeGreaterThan(0)
    for (const item of items) expect(item.firstElementChild).toHaveAttribute('data-virtual-row-key')
    expect(container.querySelector('li')).toBeNull()
  })

  it('keeps the footer under the rows and the overlay in the frame, outside the scroller', () => {
    const { container } = renderList(30, { footer: <div className="feed-working">@qa is working</div>, overlay: <button type="button">overlay</button> })
    const scroller = container.querySelector('#feed-content') as HTMLElement
    expect(scroller.querySelector('.feed-scroll-footer .feed-working')).not.toBeNull()
    expect(scroller.contains(screen.getByText('overlay'))).toBe(false)
    expect(container.querySelector('.feed-scroll-frame')?.contains(screen.getByText('overlay'))).toBe(true)
  })

  const stand = (scroller: HTMLElement, scrollHeight: number, scrollTop: number): void => {
    Object.defineProperty(scroller, 'scrollHeight', { value: scrollHeight, configurable: true })
    Object.defineProperty(scroller, 'clientHeight', { value: 400, configurable: true })
    Object.defineProperty(scroller, 'scrollTop', { value: scrollTop, writable: true, configurable: true })
  }

  it('tells a scroll after a wheel-up gesture from a scroll without one', () => {
    const onFollow = vi.fn()
    const { container } = renderList(30, { onFollow })
    const scroller = container.querySelector('#feed-content') as HTMLElement
    stand(scroller, 2000, 1000)
    moveScroller(scroller, 1000)
    scroller.dispatchEvent(new WheelEvent('wheel', { deltaY: -50 }))
    moveScroller(scroller, 500)
    expect(onFollow).toHaveBeenLastCalledWith({ type: 'scroll', nearBottom: false, user: 'up' })
    moveScroller(scroller, 400)
    expect(onFollow).toHaveBeenLastCalledWith({ type: 'scroll', nearBottom: false, user: 'up' })
  })

  it('does not attribute a scroll without a gesture to the user, in either direction', () => {
    const onFollow = vi.fn()
    const { container } = renderList(30, { onFollow })
    const scroller = container.querySelector('#feed-content') as HTMLElement
    stand(scroller, 2000, 1000)
    moveScroller(scroller, 500)
    expect(onFollow).toHaveBeenLastCalledWith({ type: 'scroll', nearBottom: false, user: null })
    moveScroller(scroller, 1590)
    expect(onFollow).toHaveBeenLastCalledWith({ type: 'scroll', nearBottom: true, user: null })
  })

  it('a wheel gesture counts for 500 ms only: a slow render between it and the scroll event makes the scroll a non-gesture', () => {
    const onFollow = vi.fn()
    const { container } = renderList(30, { onFollow })
    const scroller = container.querySelector('#feed-content') as HTMLElement
    stand(scroller, 2000, 1000)
    moveScroller(scroller, 1000)
    const now = vi.spyOn(performance, 'now')
    try {
      now.mockReturnValue(1000)
      scroller.dispatchEvent(new WheelEvent('wheel', { deltaY: -50 }))
      now.mockReturnValue(1499)
      moveScroller(scroller, 600)
      expect(onFollow).toHaveBeenLastCalledWith(expect.objectContaining({ user: 'up' }))
      now.mockReturnValue(1501)
      moveScroller(scroller, 500)
      expect(onFollow).toHaveBeenLastCalledWith(expect.objectContaining({ user: null }))
    } finally { now.mockRestore() }
  })

  it('an explicit mock context wins over the default one of the test setup', () => {
    const { container } = renderList(5000, {}, { viewportHeight: 100, itemHeight: 20 })
    expect(container.querySelectorAll('[data-virtual-row-key]').length).toBeLessThan(30)
  })
})

// A reader who is not following: the geometry of jsdom is replaced by a fixed one (20px rows, a 200px scroller
// scrolled by `layout.offset`), so that the anchor of the list is observable.
describe('VirtualFeedList anchor and follow', () => {
  const layout = { keys: [] as string[], offset: 0 }
  const keysOf = (rows: ReturnType<typeof rowsOf>) => rows.map((row) => row.key)
  let scrollBy: ReturnType<typeof vi.spyOn>
  let rectOf: ReturnType<typeof vi.spyOn>

  beforeEach(() => {
    resetListCalls()
    scrollBy = vi.spyOn(Element.prototype, 'scrollBy')
    Object.defineProperty(HTMLElement.prototype, 'clientHeight', { configurable: true, get() { return (this as HTMLElement).id === 'feed-content' ? 200 : 0 } })
    rectOf = vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
      // A feed item inside a row stands where its row does.
      const key = this.getAttribute('data-virtual-row-key') ?? (this.hasAttribute('data-feed-item') ? this.closest('[data-virtual-row-key]')?.getAttribute('data-virtual-row-key') ?? null : null)
      const top = this.id === 'feed-content' ? 0 : key === null ? 0 : layout.keys.indexOf(key) * 20 - layout.offset
      const height = this.id === 'feed-content' ? 200 : key === null ? 0 : 20
      return { top, bottom: top + height, height, left: 0, right: 0, width: 0, x: 0, y: top, toJSON: () => ({}) }
    })
  })
  afterEach(() => {
    vi.useRealTimers()
    scrollBy.mockRestore()
    rectOf.mockRestore()
    delete (HTMLElement.prototype as { clientHeight?: number }).clientHeight
  })

  function mount(rows: ReturnType<typeof rowsOf>, props: Partial<Parameters<typeof VirtualFeedList>[0]> = {}) {
    layout.keys = keysOf(rows)
    const ui = (next: ReturnType<typeof rowsOf>, more: Partial<Parameters<typeof VirtualFeedList>[0]> = {}) => (
      <VirtuosoMockContext.Provider value={{ viewportHeight: 400, itemHeight: 20 }}>
        <VirtualFeedList
          rows={next}
          renderRow={(row, isFirst) => <RowFrame row={row} isFirst={isFirst}><button type="button">{row.key}</button></RowFrame>}
          following={false}
          onFollow={() => {}}
          start={null}
          footer={null}
          footerKey=""
          overlay={null}
          {...props}
          {...more}
        />
      </VirtuosoMockContext.Provider>
    )
    const view = render(ui(rows))
    return {
      ...view,
      scroller: view.container.querySelector('#feed-content') as HTMLElement,
      update(next: ReturnType<typeof rowsOf>, more: Partial<Parameters<typeof VirtualFeedList>[0]> = {}) {
        layout.keys = keysOf(next)
        view.rerender(ui(next, more))
      },
    }
  }
  // The anchor is read on the frame after a scroll event.
  const readAnchor = async (scroller: HTMLElement): Promise<void> => {
    fireEvent.scroll(scroller)
    await act(() => new Promise<void>((done) => requestAnimationFrame(() => done())))
  }
  const without = (rows: ReturnType<typeof rowsOf>, ...keys: string[]) => rows.filter((row) => !keys.includes(row.key))

  it('names the first visible row and its offset as the anchor', async () => {
    layout.offset = 110
    const handle = createRef<FeedListHandle>()
    const view = mount(rowsOf(30), { ref: handle })
    await readAnchor(view.scroller)
    expect(handle.current?.anchor()).toEqual({ key: 'seq:6', offset: -10 })
  })

  it('does not take a sliver of a row at the top edge for the anchor', async () => {
    // seq:3 shows 6px of itself above seq:4: the reader is looking at seq:4.
    layout.offset = 54
    const handle = createRef<FeedListHandle>()
    const view = mount(rowsOf(30), { ref: handle })
    await readAnchor(view.scroller)
    expect(handle.current?.anchor()).toEqual({ key: 'seq:4', offset: 6 })
  })

  it('events evicted behind head rows that stay put move the list to the next surviving event, not to the head', async () => {
    layout.offset = 54
    const rows = rowsOf(30)
    const view = mount(rows)
    await readAnchor(view.scroller)
    vi.useFakeTimers()
    resetListCalls()
    const afterFirst = without(rows, 'seq:4')
    view.update(afterFirst)
    expect(lastScroll()).toEqual({ index: 3, align: 'start', offset: -0, behavior: 'auto' })
    vi.advanceTimersByTime(160)
    resetListCalls()
    view.update(without(afterFirst, 'seq:5'))
    expect(lastScroll()).toEqual({ index: 3, align: 'start', offset: -0, behavior: 'auto' })
  })

  it('puts the anchor row back where it was when rows above it are merged away', async () => {
    layout.offset = 110
    const rows = rowsOf(30)
    const view = mount(rows)
    await readAnchor(view.scroller)
    resetListCalls()
    view.update(without(rows, 'seq:3'))
    // seq:6 is now row 4; its top was 10px above the scroller's top.
    expect(lastScroll()).toEqual({ index: 4, align: 'start', offset: 10, behavior: 'auto' })
  })

  it('corrects the residual shift once the sizes settle', async () => {
    layout.offset = 110
    const rows = rowsOf(30)
    const view = mount(rows)
    await readAnchor(view.scroller)
    vi.useFakeTimers()
    view.update(without(rows, 'seq:3'))
    // The row now sits 20px higher than before, at the same scroll offset.
    vi.advanceTimersByTime(160)
    expect(scrollBy).toHaveBeenCalledWith({ top: -20, behavior: 'auto' })
  })

  it.each([
    ['a wheel down', (scroller: HTMLElement) => fireEvent.wheel(scroller, { deltaY: 80 })],
    ['a wheel up', (scroller: HTMLElement) => fireEvent.wheel(scroller, { deltaY: -80 })],
    ['a touch', (scroller: HTMLElement) => fireEvent.touchMove(scroller)],
    ['a navigation key', (scroller: HTMLElement) => fireEvent.keyDown(scroller, { key: 'PageDown' })],
    ['dragging the scrollbar', (scroller: HTMLElement) => fireEvent.pointerDown(scroller)],
  ])('%s cancels the pending corrections, so they do not pull the reader back', async (_name, gesture) => {
    layout.offset = 110
    const rows = rowsOf(30)
    const view = mount(rows)
    await readAnchor(view.scroller)
    vi.useFakeTimers()
    view.update(without(rows, 'seq:3'))
    // Without the gesture the same correction does happen (see the test above).
    gesture(view.scroller)
    vi.advanceTimersByTime(2000)
    expect(scrollBy).not.toHaveBeenCalled()
  })

  it('a held row wins over the first visible one and keeps its place', () => {
    layout.offset = 110
    const handle = createRef<FeedListHandle>()
    const rows = rowsOf(30)
    const view = mount(rows, { ref: handle })
    // seq:8 is row 7: 30px below the scroller's top.
    act(() => handle.current?.hold('seq:8'))
    resetListCalls()
    view.update(without(rows, 'seq:3'))
    expect(lastScroll()).toEqual({ index: 6, align: 'start', offset: -30, behavior: 'auto' })
  })

  it('an anchor evicted twice in a row moves to the next surviving row each time, never to the head of the list', async () => {
    layout.offset = 110
    const rows = rowsOf(30)
    const view = mount(rows)
    await readAnchor(view.scroller)
    vi.useFakeTimers()
    const afterFirst = without(rows, 'seq:6')
    view.update(afterFirst)
    expect(lastScroll()).toEqual({ index: 5, align: 'start', offset: -0, behavior: 'auto' })
    // The list settles with seq:7 at the top; no scroll event has re-read the anchor since.
    vi.advanceTimersByTime(160)
    resetListCalls()
    view.update(without(afterFirst, 'seq:7'))
    expect(lastScroll()).toEqual({ index: 5, align: 'start', offset: -0, behavior: 'auto' })
  })

  it('an anchor that is not among the previous keys leaves the position alone', async () => {
    layout.offset = 110
    const rows = rowsOf(30)
    const view = mount(rows)
    await readAnchor(view.scroller)
    view.update(without(rows, 'seq:6'))
    // The captured anchor (seq:6) is gone from the previous keys too.
    resetListCalls()
    view.update(without(rows, 'seq:6', 'seq:7'))
    expect(listCalls.scrolls.every((call) => (call as { index: number }).index !== 0)).toBe(true)
  })

  it('a reader who collapsed a series back to the bottom resumes following once the sizes settle', () => {
    vi.useFakeTimers()
    const onFollow = vi.fn()
    const handle = createRef<FeedListHandle>()
    const rows = rowsOf(30)
    const view = mount(rows, { ref: handle, onFollow })
    act(() => handle.current?.hold('seq:8'))
    act(() => handle.current?.watchBottom())
    view.update(without(rows, 'seq:3'), { onFollow })
    // After the shrink the list stands at its end; no scroll event is fired by that.
    Object.defineProperty(view.scroller, 'scrollHeight', { value: 1000, configurable: true })
    Object.defineProperty(view.scroller, 'scrollTop', { value: 800, writable: true, configurable: true })
    vi.advanceTimersByTime(160)
    expect(onFollow).toHaveBeenCalledWith({ type: 'collapsed', nearBottom: true })
  })

  it('a reader who collapsed a series stays resumed even when the pending corrections were cancelled', () => {
    vi.useFakeTimers()
    const onFollow = vi.fn()
    const handle = createRef<FeedListHandle>()
    const view = mount(rowsOf(30), { ref: handle, onFollow })
    act(() => handle.current?.watchBottom())
    // Nothing reschedules the settle timers here: the list simply ends up at the bottom without a scroll event.
    Object.defineProperty(view.scroller, 'scrollHeight', { value: 1000, configurable: true })
    Object.defineProperty(view.scroller, 'scrollTop', { value: 800, writable: true, configurable: true })
    vi.advanceTimersByTime(400)
    expect(onFollow).toHaveBeenCalledWith({ type: 'collapsed', nearBottom: true })
  })

  it('watching the bottom after a collapse stops on a reader gesture and after its window', () => {
    vi.useFakeTimers()
    const onFollow = vi.fn()
    const handle = createRef<FeedListHandle>()
    const view = mount(rowsOf(30), { ref: handle, onFollow })
    act(() => handle.current?.watchBottom())
    fireEvent.wheel(view.scroller, { deltaY: -80 })
    Object.defineProperty(view.scroller, 'scrollHeight', { value: 1000, configurable: true })
    Object.defineProperty(view.scroller, 'scrollTop', { value: 800, writable: true, configurable: true })
    vi.advanceTimersByTime(100)
    expect(onFollow).not.toHaveBeenCalled()
    act(() => handle.current?.watchBottom())
    vi.advanceTimersByTime(5000)
    Object.defineProperty(view.scroller, 'scrollTop', { value: 800, writable: true, configurable: true })
    onFollow.mockClear()
    vi.advanceTimersByTime(100)
    expect(onFollow).not.toHaveBeenCalled()
  })

  it('a reader who expanded a series away from the bottom keeps reading: no follow event after the sizes settle', () => {
    vi.useFakeTimers()
    const onFollow = vi.fn()
    const handle = createRef<FeedListHandle>()
    const rows = rowsOf(30)
    const view = mount(rows, { ref: handle, onFollow })
    act(() => handle.current?.hold('seq:8'))
    view.update(without(rows, 'seq:3'), { onFollow })
    Object.defineProperty(view.scroller, 'scrollHeight', { value: 1331, configurable: true })
    Object.defineProperty(view.scroller, 'scrollTop', { value: 800, writable: true, configurable: true })
    vi.advanceTimersByTime(2000)
    expect(onFollow).not.toHaveBeenCalled()
  })

  it('an expansion that leaves less than 40px to the bottom keeps follow off: the settle timers never resume it', () => {
    vi.useFakeTimers()
    const onFollow = vi.fn()
    const handle = createRef<FeedListHandle>()
    const rows = rowsOf(30)
    const view = mount(rows, { ref: handle, onFollow })
    act(() => handle.current?.hold('seq:8'))
    view.update(without(rows, 'seq:3'), { onFollow })
    Object.defineProperty(view.scroller, 'scrollHeight', { value: 1030, configurable: true })
    Object.defineProperty(view.scroller, 'scrollTop', { value: 800, writable: true, configurable: true })
    vi.advanceTimersByTime(2000)
    expect(onFollow).not.toHaveBeenCalled()
  })

  it('an expansion after a collapse stops watching the bottom', () => {
    vi.useFakeTimers()
    const onFollow = vi.fn()
    const handle = createRef<FeedListHandle>()
    const view = mount(rowsOf(30), { ref: handle, onFollow })
    act(() => handle.current?.hold('seq:8'))
    act(() => handle.current?.watchBottom())
    act(() => handle.current?.hold('seq:9'))
    Object.defineProperty(view.scroller, 'scrollHeight', { value: 1000, configurable: true })
    Object.defineProperty(view.scroller, 'scrollTop', { value: 800, writable: true, configurable: true })
    vi.advanceTimersByTime(400)
    expect(onFollow).not.toHaveBeenCalled()
  })

  it('a hidden list (zero size) neither follows nor corrects when the pending settle timers fire', () => {
    vi.useFakeTimers()
    const onFollow = vi.fn()
    const handle = createRef<FeedListHandle>()
    const rows = rowsOf(30)
    const view = mount(rows, { ref: handle, onFollow })
    act(() => handle.current?.hold('seq:8'))
    view.update(without(rows, 'seq:3'), { onFollow })
    // Team is opened over the feed: display:none gives the scroller zero geometry.
    Object.defineProperty(view.scroller, 'clientHeight', { value: 0, configurable: true })
    Object.defineProperty(view.scroller, 'scrollHeight', { value: 0, configurable: true })
    scrollBy.mockClear()
    vi.advanceTimersByTime(2000)
    expect(onFollow).not.toHaveBeenCalled()
    expect(scrollBy).not.toHaveBeenCalled()
  })

  it('a hidden list ignores the scroll event and the bottom watcher', () => {
    vi.useFakeTimers()
    const onFollow = vi.fn()
    const handle = createRef<FeedListHandle>()
    const view = mount(rowsOf(30), { ref: handle, onFollow })
    act(() => handle.current?.watchBottom())
    Object.defineProperty(view.scroller, 'clientHeight', { value: 0, configurable: true })
    Object.defineProperty(view.scroller, 'scrollHeight', { value: 0, configurable: true })
    fireEvent.scroll(view.scroller)
    vi.advanceTimersByTime(400)
    expect(onFollow).not.toHaveBeenCalled()
  })

  it('the bottom watcher survives the composition update that cancels the settle corrections', () => {
    vi.useFakeTimers()
    const onFollow = vi.fn()
    const handle = createRef<FeedListHandle>()
    const rows = rowsOf(30)
    const view = mount(rows, { ref: handle, onFollow })
    act(() => handle.current?.hold('seq:8'))
    act(() => handle.current?.watchBottom())
    // restoreAnchor runs here and cancels the corrections it had scheduled before.
    view.update(without(rows, 'seq:3'), { onFollow })
    view.update(without(rows, 'seq:3', 'seq:4'), { onFollow })
    Object.defineProperty(view.scroller, 'scrollHeight', { value: 1000, configurable: true })
    Object.defineProperty(view.scroller, 'scrollTop', { value: 800, writable: true, configurable: true })
    vi.advanceTimersByTime(100)
    expect(onFollow).toHaveBeenCalledWith({ type: 'collapsed', nearBottom: true })
  })

  it('the bottom watcher gives up after its 2 s window even if the list settles within 40px later', () => {
    vi.useFakeTimers()
    const onFollow = vi.fn()
    const handle = createRef<FeedListHandle>()
    const view = mount(rowsOf(30), { ref: handle, onFollow })
    const now = vi.spyOn(performance, 'now')
    try {
      now.mockReturnValue(10_000)
      act(() => handle.current?.watchBottom())
      // 331px from the bottom for the whole window.
      Object.defineProperty(view.scroller, 'scrollHeight', { value: 1331, configurable: true })
      Object.defineProperty(view.scroller, 'scrollTop', { value: 800, writable: true, configurable: true })
      now.mockReturnValue(10_000 + 1999)
      vi.advanceTimersByTime(100)
      expect(onFollow).not.toHaveBeenCalled()
      now.mockReturnValue(10_000 + 2001)
      vi.advanceTimersByTime(100)
      Object.defineProperty(view.scroller, 'scrollHeight', { value: 1000, configurable: true })
      vi.advanceTimersByTime(100)
      expect(onFollow).not.toHaveBeenCalled()
    } finally { now.mockRestore() }
  })

  // follow after every event the list reported, from a reader who is not following.
  const followAfter = (onFollow: ReturnType<typeof vi.fn>, from = false): boolean =>
    onFollow.mock.calls.reduce((following, [event]) => followReducer(following, event), from)
  const place = (scroller: HTMLElement, scrollHeight: number, scrollTop: number, onFollow: ReturnType<typeof vi.fn>): void => {
    Object.defineProperty(scroller, 'scrollHeight', { value: scrollHeight, configurable: true })
    Object.defineProperty(scroller, 'scrollTop', { value: scrollTop, writable: true, configurable: true })
    fireEvent.scroll(scroller)
    onFollow.mockClear()
  }
  // The list answers scrollBy the way a browser does: the scroller moves and fires a scroll event.
  const scrollByMoves = (): void => {
    scrollBy.mockImplementation(function (this: unknown, ...args: unknown[]) {
      const el = this as HTMLElement
      moveScroller(el, el.scrollTop + ((args[0] as ScrollToOptions).top ?? 0))
    })
  }

  it('programmatic scrolls near the bottom never resume follow: anchor restore and settle corrections fire real scroll events', () => {
    vi.useFakeTimers()
    const onFollow = vi.fn()
    const handle = createRef<FeedListHandle>()
    const rows = rowsOf(30)
    const view = mount(rows, { ref: handle, onFollow })
    // An expansion left 30px to the bottom.
    // 30px from the end of a 230px content; the rows stay mounted, as the stand-in windows them by scrollTop.
    act(() => handle.current?.hold('seq:8'))
    place(view.scroller, 230, 0, onFollow)
    scrollByMoves()
    listCalls.move = () => moveScroller(view.scroller, 10)
    view.update(without(rows, 'seq:3'), { onFollow })
    vi.advanceTimersByTime(1400)
    expect(scrollBy).toHaveBeenCalled()
    expect(onFollow.mock.calls.length).toBeGreaterThan(1)
    expect(onFollow.mock.calls.every(([event]) => event.type !== 'scroll' || event.user === null)).toBe(true)
    expect(followAfter(onFollow)).toBe(false)
  })

  it('head eviction and size adjustments near the bottom, with appends streaming, never resume follow', () => {
    vi.useFakeTimers()
    const onFollow = vi.fn()
    const rows = rowsOf(30)
    const view = mount(rows, { onFollow })
    place(view.scroller, 1030, 800, onFollow)
    for (let step = 0; step < 3; step++) {
      view.update(rowsOf(31 + step).slice(step + 1), { onFollow })
      fireEvent.scroll(view.scroller)
      vi.advanceTimersByTime(200)
    }
    moveScroller(view.scroller, 805)
    expect(followAfter(onFollow)).toBe(false)
  })

  it('a keyboard expansion of a series at the tail does not count as a scroll gesture', () => {
    vi.useFakeTimers()
    const onFollow = vi.fn()
    const handle = createRef<FeedListHandle>()
    const rows = rowsOf(30)
    const view = mount(rows, { ref: handle, onFollow })
    const button = view.scroller.querySelector('button') as HTMLElement
    act(() => handle.current?.hold('seq:8'))
    place(view.scroller, 230, 0, onFollow)
    scrollByMoves()
    listCalls.move = () => moveScroller(view.scroller, 10)
    fireEvent.keyDown(button, { key: ' ' })
    view.update(without(rows, 'seq:3'), { onFollow })
    vi.advanceTimersByTime(1400)
    expect(followAfter(onFollow)).toBe(false)
  })

  it('a scroll after the list moved itself is not attributed to an earlier gesture', () => {
    vi.useFakeTimers()
    const onFollow = vi.fn()
    const handle = createRef<FeedListHandle>()
    const rows = rowsOf(30)
    const view = mount(rows, { ref: handle, onFollow })
    place(view.scroller, 2000, 1000, onFollow)
    fireEvent.wheel(view.scroller, { deltaY: -50 })
    listCalls.move = () => moveScroller(view.scroller, 400)
    act(() => handle.current?.scrollToRow('seq:3', 'start'))
    expect(onFollow).toHaveBeenLastCalledWith({ type: 'scroll', nearBottom: false, user: null })
  })

  it('a collapse that settled at the bottom resumes follow even with appends between the click and the settle', () => {
    vi.useFakeTimers()
    const onFollow = vi.fn()
    const handle = createRef<FeedListHandle>()
    const rows = rowsOf(30)
    const view = mount(rows, { ref: handle, onFollow })
    // At click time the expanded series still stands 331px below the viewport and no scroll event follows the collapse.
    place(view.scroller, 1331, 800, onFollow)
    act(() => handle.current?.hold('seq:8'))
    act(() => handle.current?.watchBottom())
    expect(onFollow).not.toHaveBeenCalled()
    view.update(rowsOf(31), { onFollow })
    Object.defineProperty(view.scroller, 'scrollHeight', { value: 1000, configurable: true })
    vi.advanceTimersByTime(100)
    expect(followAfter(onFollow)).toBe(true)
  })

  it('navigating to a row cancels the bottom watch of an earlier collapse', () => {
    vi.useFakeTimers()
    const onFollow = vi.fn()
    const handle = createRef<FeedListHandle>()
    const view = mount(rowsOf(30), { ref: handle, onFollow })
    act(() => handle.current?.watchBottom())
    act(() => handle.current?.scrollToRow('seq:25', 'center'))
    Object.defineProperty(view.scroller, 'scrollHeight', { value: 1000, configurable: true })
    Object.defineProperty(view.scroller, 'scrollTop', { value: 800, writable: true, configurable: true })
    vi.advanceTimersByTime(400)
    expect(onFollow).not.toHaveBeenCalled()
  })

  it('a collapse right after a reader gesture still resumes follow: only input newer than the collapse cancels the watch', () => {
    vi.useFakeTimers()
    const onFollow = vi.fn()
    const handle = createRef<FeedListHandle>()
    const view = mount(rowsOf(30), { ref: handle, onFollow })
    place(view.scroller, 1331, 800, onFollow)
    fireEvent.wheel(view.scroller, { deltaY: -50 })
    act(() => handle.current?.watchBottom())
    // The collapsed content ends at the bottom; no scroll event follows.
    Object.defineProperty(view.scroller, 'scrollHeight', { value: 1000, configurable: true })
    vi.advanceTimersByTime(100)
    expect(followAfter(onFollow)).toBe(true)
  })

  it('a gesture after the collapse still cancels its bottom watch', () => {
    vi.useFakeTimers()
    const onFollow = vi.fn()
    const handle = createRef<FeedListHandle>()
    const view = mount(rowsOf(30), { ref: handle, onFollow })
    place(view.scroller, 1331, 800, onFollow)
    act(() => handle.current?.watchBottom())
    fireEvent.wheel(view.scroller, { deltaY: -50 })
    Object.defineProperty(view.scroller, 'scrollHeight', { value: 1000, configurable: true })
    vi.advanceTimersByTime(100)
    expect(onFollow).not.toHaveBeenCalled()
  })

  it('a tail request waits out a nudge up, and its scroll event is not attributed to that nudge: follow is left alone', () => {
    const onFollow = vi.fn()
    const now = vi.spyOn(performance, 'now').mockReturnValue(1000)
    try {
      const view = mount(rowsOf(30), { onFollow, following: true })
      place(view.scroller, 1030, 820, onFollow)
      fireEvent.wheel(view.scroller, { deltaY: -50 })
      resetListCalls()
      listCalls.move = () => moveScroller(view.scroller, 400)
      view.update(rowsOf(30), { onFollow, following: true, footerKey: 'working' })
      expect(listCalls.scrolls).toEqual([])
      now.mockReturnValue(1600)
      view.update(rowsOf(30), { onFollow, following: true, footerKey: 'working again' })
      expect(lastScroll()).toEqual({ index: 'LAST', align: 'end', behavior: 'auto' })
      expect(onFollow).toHaveBeenLastCalledWith({ type: 'scroll', nearBottom: false, user: null })
      expect(followAfter(onFollow, true)).toBe(true)
    } finally { now.mockRestore() }
  })

  it('an anchor restore during a PageDown waits for its end: the scroll that reaches the bottom stays the reader\'s and resumes follow', async () => {
    layout.offset = 54
    const onFollow = vi.fn()
    const rows = rowsOf(30)
    const view = mount(rows, { onFollow })
    await readAnchor(view.scroller)
    vi.useFakeTimers()
    const now = vi.spyOn(performance, 'now').mockReturnValue(1000)
    try {
      place(view.scroller, 1030, 650, onFollow)
      fireEvent.keyDown(view.scroller, { key: 'PageDown' })
      moveScroller(view.scroller, 700)
      resetListCalls()
      // An event evicted behind rows that stay put: the anchor would be restored, but the PageDown is still moving.
      view.update(without(rows, 'seq:4'), { onFollow })
      expect(listCalls.scrolls).toEqual([])
      moveScroller(view.scroller, 820)
      expect(onFollow).toHaveBeenLastCalledWith({ type: 'scroll', nearBottom: true, user: 'down' })
      expect(followAfter(onFollow)).toBe(true)
      view.update(without(rows, 'seq:4'), { onFollow, following: true })
      now.mockReturnValue(1600)
      vi.advanceTimersByTime(600)
      // The restore waited for the end of the gesture, which handed the list to the tail.
      expect(listCalls.scrolls).toEqual([{ index: 'LAST', align: 'end', behavior: 'auto' }])
    } finally { now.mockRestore() }
  })

  it('a held row is put back at once even during a gesture: an expand or a collapse is the reader\'s own, and it drops a deferred restore', async () => {
    layout.offset = 110
    const handle = createRef<FeedListHandle>()
    const rows = rowsOf(30)
    const view = mount(rows, { ref: handle })
    await readAnchor(view.scroller)
    vi.useFakeTimers()
    const now = vi.spyOn(performance, 'now').mockReturnValue(1000)
    try {
      fireEvent.wheel(view.scroller, { deltaY: -50 })
      resetListCalls()
      view.update(without(rows, 'seq:2'))
      expect(listCalls.scrolls).toEqual([])
      // seq:8 is row 6 now: 10px below the scroller's top.
      act(() => handle.current?.hold('seq:8'))
      view.update(without(rows, 'seq:2', 'seq:3'))
      const held = { index: 5, align: 'start', offset: -10, behavior: 'auto' }
      expect(listCalls.scrolls).toEqual([held])
      now.mockReturnValue(1600)
      vi.advanceTimersByTime(600)
      expect(listCalls.scrolls).toEqual([held])
    } finally { now.mockRestore() }
  })

  it('an anchor restore deferred by a gesture still runs after it, to the row that replaces the evicted anchor', async () => {
    layout.offset = 54
    const onFollow = vi.fn()
    const rows = rowsOf(30)
    const view = mount(rows, { onFollow })
    await readAnchor(view.scroller)
    vi.useFakeTimers()
    const now = vi.spyOn(performance, 'now').mockReturnValue(1000)
    try {
      onFollow.mockClear()
      fireEvent.wheel(view.scroller, { deltaY: -50 })
      resetListCalls()
      view.update(without(rows, 'seq:4'), { onFollow })
      expect(listCalls.scrolls).toEqual([])
      now.mockReturnValue(1400)
      vi.advanceTimersByTime(500)
      expect(listCalls.scrolls).toEqual([])
      now.mockReturnValue(1600)
      vi.advanceTimersByTime(500)
      expect(listCalls.scrolls).toEqual([{ index: 3, align: 'start', offset: -0, behavior: 'auto' }])
      expect(followAfter(onFollow)).toBe(false)
    } finally { now.mockRestore() }
  })

  it('a second eviction during the gesture does not lose a deferred restore whose anchor was evicted by the first one', async () => {
    layout.offset = 54
    const rows = rowsOf(30)
    const view = mount(rows)
    await readAnchor(view.scroller)
    vi.useFakeTimers()
    const now = vi.spyOn(performance, 'now').mockReturnValue(1000)
    try {
      fireEvent.wheel(view.scroller, { deltaY: -50 })
      resetListCalls()
      view.update(without(rows, 'seq:4'))
      view.update(without(rows, 'seq:4', 'seq:5'))
      expect(listCalls.scrolls).toEqual([])
      now.mockReturnValue(1600)
      vi.advanceTimersByTime(500)
      // The anchor seq:4 is only in the first composition; seq:6 is its next surviving row.
      expect(listCalls.scrolls).toEqual([{ index: 3, align: 'start', offset: -0, behavior: 'auto' }])
    } finally { now.mockRestore() }
  })

  it('a deferred restore puts back the anchor captured last: a reader who scrolled on during the gesture is not pulled back', async () => {
    layout.offset = 54
    const rows = rowsOf(30)
    const view = mount(rows)
    await readAnchor(view.scroller)
    vi.useFakeTimers()
    const now = vi.spyOn(performance, 'now').mockReturnValue(1000)
    try {
      Object.defineProperty(view.scroller, 'scrollHeight', { value: 1030, configurable: true })
      fireEvent.wheel(view.scroller, { deltaY: -50 })
      resetListCalls()
      view.update(without(rows, 'seq:4'))
      // The wheel goes on: seq:9, row 7 now, is captured 6px below the top.
      layout.offset = 134
      moveScroller(view.scroller, 134)
      vi.advanceTimersByTime(20)
      now.mockReturnValue(1600)
      vi.advanceTimersByTime(500)
      expect(listCalls.scrolls).toEqual([{ index: 7, align: 'start', offset: -6, behavior: 'auto' }])
    } finally { now.mockRestore() }
  })

  it('a tool series renamed by two capped evictions during a gesture moves to the row after the name captured between them', async () => {
    // A window of 30 events behind three conversation rows that stay; calls 4-6 make one series, keyed by its first call.
    const messages = [1, 2, 3].map((n): MeshMessage => ({ id: `conv:${n}`, thread_id: 't1', sender_id: 'run:qa', recipient_ids: ['stage:s1'], kind: 'message', body: `conversation ${n}`, seq: n, created_at: new Date(Date.UTC(2026, 6, 10, 9, 59, n)).toISOString() }))
    const eventsFrom = (from: number): AfmEvent[] => Array.from({ length: 30 }, (_, i) => from + i).map((n) => (n <= 6 ? ({ ...text(n), payload: { tool: 'Read', detail: `file ${n}` } } as AfmEvent) : text(n)))
    const windowFrom = (from: number) => buildVirtualFeedRows(buildFeedTimeline(toFeedItems(eventsFrom(from)), messages, 'all'), new Set()).rows
    layout.offset = 54
    const handle = createRef<FeedListHandle>()
    const view = mount(windowFrom(4), { ref: handle })
    await readAnchor(view.scroller)
    expect(handle.current?.anchor()).toEqual({ key: 'tools:seq:4', offset: 6 })
    vi.useFakeTimers()
    const now = vi.spyOn(performance, 'now').mockReturnValue(1000)
    try {
      Object.defineProperty(view.scroller, 'scrollHeight', { value: 1030, configurable: true })
      fireEvent.wheel(view.scroller, { deltaY: -10 })
      resetListCalls()
      view.update(windowFrom(5))
      // The wheel goes on and captures the series under its new key.
      layout.offset = 53
      moveScroller(view.scroller, 53)
      vi.advanceTimersByTime(20)
      expect(handle.current?.anchor()).toEqual({ key: 'tools:seq:5', offset: 7 })
      view.update(windowFrom(6))
      expect(listCalls.scrolls).toEqual([])
      now.mockReturnValue(1600)
      vi.advanceTimersByTime(500)
      // tools:seq:5 stood right before seq:7 in the composition it was captured in.
      expect(listCalls.scrolls).toEqual([{ index: 4, align: 'start', offset: -0, behavior: 'auto' }])
    } finally { now.mockRestore() }
  })

  describe('a footer that appears during an End press', () => {
    const footer = <div className="feed-working">@qa is working</div>

    it('is followed once the press is over, though it leaves less than 40px to the bottom', () => {
      vi.useFakeTimers()
      const now = vi.spyOn(performance, 'now').mockReturnValue(1000)
      try {
        const onFollow = vi.fn()
        const view = mount(rowsOf(30), { onFollow, following: true })
        place(view.scroller, 1000, 800, onFollow)
        fireEvent.keyDown(view.scroller, { key: 'End' })
        resetListCalls()
        // The browser answers a tail request by standing the scroller at its bottom.
        listCalls.move = () => moveScroller(view.scroller, view.scroller.scrollHeight - 200)
        view.update(rowsOf(30), { onFollow, following: true, footer, footerKey: 'working' })
        Object.defineProperty(view.scroller, 'scrollHeight', { value: 1037, configurable: true })
        vi.advanceTimersByTime(100)
        expect(listCalls.scrolls).toEqual([])
        now.mockReturnValue(1600)
        vi.advanceTimersByTime(20)
        expect(listCalls.scrolls).toEqual([{ index: 'LAST', align: 'end', behavior: 'auto' }])
        expect(view.scroller.scrollHeight - view.scroller.scrollTop - 200).toBeLessThanOrEqual(2)
        expect(followAfter(onFollow, true)).toBe(true)
      } finally { now.mockRestore() }
    })

    it('does not move the list after the press when follow was released meanwhile', () => {
      vi.useFakeTimers()
      const now = vi.spyOn(performance, 'now').mockReturnValue(1000)
      try {
        const view = mount(rowsOf(30), { following: true })
        place(view.scroller, 1000, 800, vi.fn())
        fireEvent.keyDown(view.scroller, { key: 'End' })
        resetListCalls()
        view.update(rowsOf(30), { following: true, footer, footerKey: 'working' })
        Object.defineProperty(view.scroller, 'scrollHeight', { value: 1037, configurable: true })
        view.update(rowsOf(30), { following: false, footer, footerKey: 'working' })
        now.mockReturnValue(1600)
        vi.advanceTimersByTime(100)
        expect(listCalls.scrolls).toEqual([])
      } finally { now.mockRestore() }
    })
  })

  // A live window of `to - from` events slid to start at `from`; the last event may have grown. Calls 1 and 2 make one
  // series: sliding past call 1 keeps the series and adds a row, as a capped append usually does.
  const slide = (from: number, to: number, last = '') =>
    buildVirtualFeedRows(buildFeedTimeline(toFeedItems(Array.from({ length: to - from }, (_, i) => {
      const event = text(from + i)
      if (from + i <= 2) return { ...event, payload: { tool: 'Read', detail: `file ${from + i}` } } as AfmEvent
      return i === to - from - 1 && last !== '' ? ({ ...event, payload: { tool: 'text', detail: last } } as AfmEvent) : event
    })), [], 'all'), new Set()).rows

  it('a nudge with a capped append, a growing last row, a footer and an image load leaves the tail to one owner: nothing moves during the nudge, one tail request after it', () => {
    vi.useFakeTimers()
    const now = vi.spyOn(performance, 'now').mockReturnValue(1000)
    const listRender = vi.spyOn(Virtuoso as unknown as { render: (props: { followOutput?: unknown }, ref: unknown) => unknown }, 'render')
    try {
      const onFollow = vi.fn()
      const view = mount(slide(1, 31), { onFollow, following: true })
      // Under the mock context a scroll of the list's own moves only its state. The browser moves the scroller too, and
      // the list scrolls by itself only to follow its output, to the bottom.
      const list = listRender.mock.calls.find(([props]) => props !== undefined)?.[1] as { current: VirtuosoHandle }
      const deliverOwnScrolls = (): void => list.current.getState(({ scrollTop }) => {
        if (scrollTop !== view.scroller.scrollTop) moveScroller(view.scroller, view.scroller.scrollHeight - 200)
      })
      const height = (value: number): void => {
        Object.defineProperty(view.scroller, 'scrollHeight', { value, configurable: true })
        deliverOwnScrolls()
      }
      const wait = (ms: number): void => {
        vi.advanceTimersByTime(ms)
        deliverOwnScrolls()
      }
      place(view.scroller, 1000, 800, onFollow)
      fireEvent.wheel(view.scroller, { deltaY: -10 })
      moveScroller(view.scroller, 795)
      resetListCalls()
      listCalls.move = () => moveScroller(view.scroller, view.scroller.scrollHeight - 200)
      view.update(slide(2, 32), { onFollow, following: true })
      height(1020)
      view.update(slide(2, 32, 'message 31, now much longer'), { onFollow, following: true })
      height(1060)
      view.update(slide(2, 32, 'message 31, now much longer'), { onFollow, following: true, footer: <div className="feed-working">@qa is working</div>, footerKey: 'working' })
      height(1097)
      wait(100)
      // The image of the last row loaded: the list grew without a commit.
      height(1297)
      wait(100)
      expect(listCalls.scrolls).toEqual([])
      expect(view.scroller.scrollTop).toBe(795)
      now.mockReturnValue(1600)
      vi.advanceTimersByTime(100)
      expect(listCalls.scrolls).toEqual([{ index: 'LAST', align: 'end', behavior: 'auto' }])
      expect(view.scroller.scrollHeight - view.scroller.scrollTop - 200).toBeLessThanOrEqual(2)
      expect(followAfter(onFollow, true)).toBe(true)
      // The list's own follow stays off in every render, also where this sequence would not trigger it.
      const followsOutput = (option: unknown): unknown => (typeof option === 'function' ? option(true) : option)
      // React also calls the render without props to describe a component stack.
      const renders = listRender.mock.calls.filter(([props]) => props !== undefined)
      expect(renders.length).toBeGreaterThan(0)
      for (const [props] of renders) expect(followsOutput(props.followOutput)).toBe(false)
    } finally {
      listRender.mockRestore()
      now.mockRestore()
    }
  })

  it('a scrollbar drag released near the bottom after more than 500 ms still resumes follow on its last scroll event', () => {
    const onFollow = vi.fn()
    const view = mount(rowsOf(30), { onFollow })
    place(view.scroller, 2000, 500, onFollow)
    const now = vi.spyOn(performance, 'now')
    try {
      now.mockReturnValue(1000)
      fireEvent.pointerDown(view.scroller)
      now.mockReturnValue(2000)
      fireEvent.pointerUp(window)
      moveScroller(view.scroller, 1790)
      expect(followAfter(onFollow)).toBe(true)
      // The attribution is that of the drag only: a later scroll without a gesture is not the reader's.
      onFollow.mockClear()
      now.mockReturnValue(3000)
      moveScroller(view.scroller, 1500)
      expect(onFollow).toHaveBeenLastCalledWith({ type: 'scroll', nearBottom: false, user: null })
    } finally { now.mockRestore() }
  })

  it('a correction that follows a collapse-time anchor restore keeps the held row within 2px and never asks for the tail', () => {
    vi.useFakeTimers()
    const onFollow = vi.fn()
    const handle = createRef<FeedListHandle>()
    const rows = rowsOf(30)
    layout.offset = 100
    const view = mount(rows, { ref: handle, onFollow })
    place(view.scroller, 1000, 100, onFollow)
    const heldTop = (): number => (view.scroller.querySelector('[data-virtual-row-key="seq:8"]') as HTMLElement).getBoundingClientRect().top
    const before = heldTop()
    // The list and its rows share one geometry: scrollToIndex and scrollBy move what the row rectangles report.
    listCalls.move = (location) => {
      const { index, offset } = location as { index: number; offset?: number }
      layout.offset = index * 20 + (offset ?? 0)
      moveScroller(view.scroller, layout.offset)
    }
    scrollBy.mockImplementation(function (this: unknown, ...args: unknown[]) {
      layout.offset += (args[0] as ScrollToOptions).top ?? 0
      moveScroller(view.scroller, layout.offset)
    })
    act(() => handle.current?.hold('seq:8'))
    view.update(without(rows, 'seq:3'), { onFollow })
    vi.advanceTimersByTime(1400)
    expect(Math.abs(heldTop() - before)).toBeLessThanOrEqual(2)
    expect(listCalls.scrolls.some((location) => (location as { index: unknown }).index === 'LAST')).toBe(false)
    expect(followAfter(onFollow)).toBe(false)
    layout.offset = 0
  })

  describe('a frame hidden and shown again', () => {
    class ObserverStub {
      static callbacks: ResizeObserverCallback[] = []
      constructor(callback: ResizeObserverCallback) { ObserverStub.callbacks.push(callback) }
      observe(): void {}
      unobserve(): void {}
      disconnect(): void {}
    }
    const size = (frame: Element, width: number, height: number): void => {
      Object.defineProperty(frame, 'clientWidth', { value: width, configurable: true })
      Object.defineProperty(frame, 'clientHeight', { value: height, configurable: true })
    }

    it.each([100, 400])('returns the reader to the row at the reading line (list offset %i) without resuming follow', (offset) => {
      vi.useFakeTimers()
      vi.stubGlobal('ResizeObserver', ObserverStub)
      ObserverStub.callbacks = []
      try {
        const onFollow = vi.fn()
        layout.offset = offset
        const view = mount(rowsOf(30), { onFollow, renderRow: (row, isFirst) => <RowFrame row={row} isFirst={isFirst}><div data-feed-item>{row.key}</div></RowFrame> })
        place(view.scroller, 600, offset, onFollow)
        // The reading row is captured on the frame after the scroll event.
        vi.advanceTimersByTime(20)
        const frame = view.container.querySelector('.feed-scroll-frame') as Element
        const reading = [...view.scroller.querySelectorAll<HTMLElement>('[data-feed-item]')].find((el) => el.getBoundingClientRect().top === 100) as HTMLElement
        expect(reading).toBeDefined()
        listCalls.move = (location) => {
          const { index, offset: shift } = location as { index: number; offset?: number }
          layout.offset = index * 20 + (shift ?? 0)
          moveScroller(view.scroller, layout.offset)
        }
        const callback = ObserverStub.callbacks[ObserverStub.callbacks.length - 1] as ResizeObserverCallback
        const notify = (): void => callback([], {} as ResizeObserver)
        size(frame, 0, 0)
        act(notify)
        // Hidden: the browser keeps no meaningful position; shown again the list stands at its tail.
        layout.offset = 400
        moveScroller(view.scroller, 400)
        onFollow.mockClear()
        size(frame, 300, 200)
        act(notify)
        vi.advanceTimersByTime(1400)
        expect(Math.abs(reading.getBoundingClientRect().top - 100)).toBeLessThanOrEqual(2)
        expect(listCalls.scrolls.some((location) => (location as { index: unknown }).index === 'LAST')).toBe(false)
        expect(followAfter(onFollow)).toBe(false)
      } finally {
        vi.unstubAllGlobals()
        layout.offset = 0
      }
    })
  })

  describe('a reader who returns to the bottom', () => {
    const readerAt = (onFollow: ReturnType<typeof vi.fn>) => {
      const view = mount(rowsOf(30), { onFollow })
      place(view.scroller, 2000, 500, onFollow)
      return view
    }

    it('by wheel down resumes follow when the scroll ends near the bottom', () => {
      const onFollow = vi.fn()
      const { scroller } = readerAt(onFollow)
      fireEvent.wheel(scroller, { deltaY: 100 })
      moveScroller(scroller, 1790)
      expect(followAfter(onFollow)).toBe(true)
    })

    it.each(['ArrowDown', 'PageDown', 'End', ' '])('by the %j key on the feed resumes follow', (key) => {
      const onFollow = vi.fn()
      const { scroller } = readerAt(onFollow)
      fireEvent.keyDown(scroller, { key })
      moveScroller(scroller, 1790)
      expect(followAfter(onFollow)).toBe(true)
    })

    it('by dragging the scrollbar or by touch resumes follow', () => {
      const onFollow = vi.fn()
      const { scroller } = readerAt(onFollow)
      fireEvent.pointerDown(scroller)
      moveScroller(scroller, 1790)
      fireEvent.pointerUp(window)
      expect(followAfter(onFollow)).toBe(true)
      onFollow.mockClear()
      moveScroller(scroller, 500)
      fireEvent.touchMove(scroller)
      moveScroller(scroller, 1790)
      expect(followAfter(onFollow, false)).toBe(true)
    })

    it('by wheel down or End when the list already stands at its end resumes follow without a scroll event', () => {
      const onFollow = vi.fn()
      const { scroller } = mount(rowsOf(30), { onFollow })
      place(scroller, 1000, 800, onFollow)
      fireEvent.wheel(scroller, { deltaY: 100 })
      expect(followAfter(onFollow)).toBe(true)
      onFollow.mockClear()
      fireEvent.keyDown(scroller, { key: 'End' })
      expect(followAfter(onFollow)).toBe(true)
    })

    it('does not resume on a nudge up that stays inside the threshold', () => {
      const onFollow = vi.fn()
      const { scroller } = mount(rowsOf(30), { onFollow })
      place(scroller, 1030, 820, onFollow)
      fireEvent.wheel(scroller, { deltaY: -50 })
      moveScroller(scroller, 805)
      expect(followAfter(onFollow)).toBe(false)
    })
  })

  it('a list opened by a filter without the reader row opens at the tail and asks to follow', () => {
    const onFollow = vi.fn()
    mount(rowsOf(30), { onFollow, start: { key: 'seq:999', align: 'start' } })
    expect(onFollow).toHaveBeenCalledWith({ type: 'reset' })
  })

  it('a list opened at the reader row does not touch follow', () => {
    const onFollow = vi.fn()
    mount(rowsOf(30), { onFollow, start: { key: 'seq:8', align: 'start' } })
    expect(onFollow).not.toHaveBeenCalled()
  })

  // Высоту списка и scrollTop раздвигает уточнение размеров строк уже после коммита.
  const driftFromBottom = (scroller: HTMLElement, distance: number): void => {
    Object.defineProperty(scroller, 'scrollHeight', { value: 1000 + distance, configurable: true })
    Object.defineProperty(scroller, 'scrollTop', { value: 800, writable: true, configurable: true })
  }

  it('a following list that drifted from the bottom after the commit goes back on the next frame', () => {
    vi.useFakeTimers()
    const view = mount(rowsOf(30), { following: true })
    view.update(rowsOf(30), { following: true, footerKey: 'a' })
    resetListCalls()
    driftFromBottom(view.scroller, 6500)
    vi.advanceTimersByTime(20)
    expect(lastScroll()).toEqual({ index: 'LAST', align: 'end', behavior: 'auto' })
  })

  it('a following list that stands at the bottom is left alone between commits', () => {
    vi.useFakeTimers()
    const view = mount(rowsOf(30), { following: true })
    view.update(rowsOf(30), { following: true, footerKey: 'a' })
    resetListCalls()
    driftFromBottom(view.scroller, 10)
    vi.advanceTimersByTime(100)
    expect(listCalls.scrolls).toHaveLength(0)
  })

  it('a reader who is not following is never pulled to the bottom by a frame', () => {
    vi.useFakeTimers()
    const view = mount(rowsOf(30), { following: false })
    resetListCalls()
    driftFromBottom(view.scroller, 6500)
    vi.advanceTimersByTime(100)
    expect(listCalls.scrolls).toHaveLength(0)
  })

  it('a following list is not pinned back to the bottom by a frame while the reader scrolls up', () => {
    vi.useFakeTimers()
    const view = mount(rowsOf(30), { following: true })
    view.update(rowsOf(30), { following: true, footerKey: 'a' })
    resetListCalls()
    fireEvent.wheel(view.scroller, { deltaY: -80 })
    driftFromBottom(view.scroller, 6500)
    fireEvent.scroll(view.scroller)
    vi.advanceTimersByTime(20)
    expect(listCalls.scrolls).toHaveLength(0)
  })

  it('a following list goes to its last row after an appended row and after the footer changes', () => {
    const rows = rowsOf(30)
    const view = mount(rows, { following: true, footerKey: '' })
    resetListCalls()
    view.update(rowsOf(31), { following: true })
    expect(lastScroll()).toEqual({ index: 'LAST', align: 'end', behavior: 'auto' })
    resetListCalls()
    view.update(rowsOf(31), { following: true, footerKey: 'working' })
    expect(lastScroll()).toEqual({ index: 'LAST', align: 'end', behavior: 'auto' })
    expect(listCalls.scrolls).toHaveLength(1)
  })

  it('a reader who is not following is not pulled to the bottom by an appended row', () => {
    layout.offset = 110
    const rows = rowsOf(30)
    const view = mount(rows)
    resetListCalls()
    view.update(rowsOf(31))
    expect(listCalls.scrolls).toEqual([])
  })

  it('scrolls to a row by its key: index, alignment, instantly; an unknown key does nothing', () => {
    const handle = createRef<FeedListHandle>()
    mount(rowsOf(30), { ref: handle })
    resetListCalls()
    handle.current?.scrollToRow('seq:7', 'center')
    expect(lastScroll()).toEqual({ index: 6, align: 'center', behavior: 'auto' })
    resetListCalls()
    handle.current?.scrollToRow('nope', 'start')
    expect(listCalls.scrolls).toEqual([])
  })

  describe('focus of an unmounted row', () => {
    it('stays with the feed when the focused row is removed', async () => {
      layout.offset = 110
      const rows = rowsOf(30)
      const view = mount(rows)
      const button = screen.getByRole('button', { name: 'seq:6' })
      act(() => button.focus())
      expect(document.activeElement).toBe(button)
      view.update(without(rows, 'seq:6'))
      await act(async () => {})
      expect(button.isConnected).toBe(false)
      expect(document.activeElement).toBe(view.scroller)
    })

    it('does not take the focus from an element the reader moved it to', async () => {
      layout.offset = 110
      const rows = rowsOf(30)
      const outside = document.createElement('button')
      document.body.append(outside)
      try {
        const view = mount(rows)
        act(() => screen.getByRole('button', { name: 'seq:6' }).focus())
        act(() => outside.focus())
        view.update(without(rows, 'seq:6'))
        await act(async () => {})
        expect(document.activeElement).toBe(outside)
      } finally {
        outside.remove()
      }
    })
  })
})
