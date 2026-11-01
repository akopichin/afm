import { act, renderHook } from '@testing-library/react'
import { useLayoutEffect, useRef } from 'react'
import { afterEach, describe, expect, it } from 'vitest'
import { useFeedFocus, type FeedFocusOptions } from './use-feed-focus'

// press — keydown так, как его шлёт браузер: из элемента в фокусе (или body), с всплытием до window.
function press(init: KeyboardEventInit, target: Element = document.activeElement ?? document.body): KeyboardEvent {
  const event = new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init })
  act(() => {
    target.dispatchEvent(event)
  })
  return event
}
const F: KeyboardEventInit = { key: 'f', code: 'KeyF' }
const ESCAPE: KeyboardEventInit = { key: 'Escape', code: 'Escape' }

function setup(options: Partial<FeedFocusOptions> = {}) {
  return renderHook((props: FeedFocusOptions) => useFeedFocus(props), {
    initialProps: { available: true, blocked: false, ...options },
  })
}

// append — элемент в документе, чтобы на нём можно было поставить фокус и отправить клавишу.
function append<T extends HTMLElement>(el: T, parent: HTMLElement = document.body): T {
  parent.appendChild(el)
  return el
}
function element(html: string): HTMLElement {
  const wrap = document.createElement('div')
  wrap.innerHTML = html
  return append(wrap.firstElementChild as HTMLElement)
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('useFeedFocus: the F key', () => {
  it('toggles focus mode with the physical F key and prevents its default', () => {
    const { result } = setup()
    expect(result.current.active).toBe(false)

    const event = press(F)
    expect(result.current.active).toBe(true)
    expect(event.defaultPrevented).toBe(true)

    press(F)
    expect(result.current.active).toBe(false)
  })

  it('follows the physical key on any keyboard layout', () => {
    const { result } = setup()
    press({ key: 'а', code: 'KeyF' })
    expect(result.current.active).toBe(true)
    press({ key: 'f', code: 'KeyG' })
    expect(result.current.active).toBe(true)
  })

  it.each([
    ['text input', '<input type="text">'],
    ['search input', '<input type="search">'],
    ['checkbox', '<input type="checkbox">'],
    ['textarea', '<textarea></textarea>'],
    ['select', '<select><option>a</option></select>'],
    ['contenteditable', '<div contenteditable="true" tabindex="0"></div>'],
    ['role=textbox', '<div role="textbox" tabindex="0"></div>'],
  ])('leaves F to a focused %s', (_label, html) => {
    const { result } = setup()
    const field = element(html)
    field.focus()

    const event = press(F, field)
    expect(result.current.active).toBe(false)
    expect(event.defaultPrevented).toBe(false)
  })

  it.each([
    ['Shift', { shiftKey: true }],
    ['Ctrl', { ctrlKey: true }],
    ['Alt', { altKey: true }],
    ['Meta', { metaKey: true }],
    ['auto-repeat', { repeat: true }],
    ['IME composition', { isComposing: true }],
    ['IME keyCode 229', { keyCode: 229 }],
  ])('ignores F with %s', (_label, init) => {
    const { result } = setup()
    const event = press({ ...F, ...init })
    expect(result.current.active).toBe(false)
    expect(event.defaultPrevented).toBe(false)
  })

  it('does nothing outside the stage Feed', () => {
    const { result } = setup({ available: false })
    press(F)
    expect(result.current.active).toBe(false)
  })

  it('does nothing while an overlay is open, reported or a native dialog', () => {
    const { result, rerender } = setup({ blocked: true })
    press(F)
    expect(result.current.active).toBe(false)

    rerender({ available: true, blocked: false })
    append(document.createElement('dialog')).setAttribute('open', '')
    press(F)
    expect(result.current.active).toBe(false)
  })

  it('ignores a key another handler already consumed', () => {
    const { result } = setup()
    const consume = (e: KeyboardEvent): void => e.preventDefault()
    document.addEventListener('keydown', consume)
    try {
      press(F)
    } finally {
      document.removeEventListener('keydown', consume)
    }
    expect(result.current.active).toBe(false)
  })
})

describe('useFeedFocus: leaving focus mode', () => {
  it('toggle() switches the mode, like the stage toolbar button', () => {
    const { result } = setup()
    act(() => result.current.toggle())
    expect(result.current.active).toBe(true)
    act(() => result.current.toggle())
    expect(result.current.active).toBe(false)
  })

  it('Escape exits focus mode, even from a text field', () => {
    const { result } = setup()
    press(F)
    const textarea = append(document.createElement('textarea'))
    textarea.focus()

    const event = press(ESCAPE, textarea)
    expect(result.current.active).toBe(false)
    expect(event.defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(textarea)
  })

  it('Escape outside focus mode is left alone', () => {
    setup()
    expect(press(ESCAPE).defaultPrevented).toBe(false)
  })

  it('Escape consumed by a menu or a modal closes only that layer', () => {
    const { result } = setup()
    press(F)
    const consume = (e: KeyboardEvent): void => e.preventDefault()
    document.addEventListener('keydown', consume)
    try {
      press(ESCAPE)
    } finally {
      document.removeEventListener('keydown', consume)
    }
    expect(result.current.active).toBe(true)

    press(ESCAPE)
    expect(result.current.active).toBe(false)
  })

  it('Escape and F keep focus mode while an overlay is open above it', () => {
    const { result, rerender } = setup()
    press(F)
    rerender({ available: true, blocked: true })

    press(ESCAPE)
    press(F)
    expect(result.current.active).toBe(true)
  })

  it('Escape during IME composition only cancels the composition', () => {
    const { result } = setup()
    press(F)
    press({ ...ESCAPE, isComposing: true })
    expect(result.current.active).toBe(true)
  })

  it('leaving the stage Feed ends focus mode, and coming back does not restore it', () => {
    const { result, rerender } = setup()
    press(F)
    expect(result.current.active).toBe(true)

    rerender({ available: false, blocked: false })
    expect(result.current.active).toBe(false)
    rerender({ available: true, blocked: false })
    expect(result.current.active).toBe(false)
  })
})

describe('useFeedFocus: keyboard focus on entering', () => {
  // The areas focus mode hides (header, tabs, Stages, People) have no stable focus target; only the stage's
  // view (Feed column, Plan, Question) with its toolbar stays on screen.
  function layout(): { rail: HTMLElement; composer: HTMLTextAreaElement; toggle: HTMLButtonElement } {
    const root = element(`
      <div>
        <aside><button class="stage-row">Stage</button></aside>
        <div class="workspace-primary">
          <div class="workspace-header"><button class="workspace-action feed-focus-toggle">Focus</button></div>
          <section class="feed-workspace"><textarea></textarea></section>
        </div>
      </div>`)
    return {
      rail: root.querySelector('.stage-row') as HTMLElement,
      composer: root.querySelector('textarea') as HTMLTextAreaElement,
      toggle: root.querySelector('.feed-focus-toggle') as HTMLButtonElement,
    }
  }

  it('moves keyboard focus from an area that focus mode hides to the mode button', () => {
    const { rail, toggle } = layout()
    setup()
    rail.focus()
    press(F, rail)
    expect(document.activeElement).toBe(toggle)
  })

  it('keeps keyboard focus inside the Feed column and on the mode button itself', () => {
    const { composer, toggle } = layout()
    const { result } = setup()
    composer.focus()
    act(() => result.current.toggle())
    expect(document.activeElement).toBe(composer)

    act(() => result.current.toggle())
    toggle.focus()
    act(() => result.current.toggle())
    expect(document.activeElement).toBe(toggle)
  })
})

describe('useFeedFocus: reading position', () => {
  // Fake layout: entering focus lifts the Feed 117px and re-wraps text, so each block sits higher in the content
  // by its own `rewrap` amount — only the block the reader holds keeps its place.
  const FOCUS_LIFT = 117
  const SCROLLER_TOP = 224
  const SCROLLER_HEIGHT = 555
  type Block = { offset: number; height: number; rewrap: number }

  function feedWithLayout(scrollTop: number) {
    const scroller = element('<div class="feed-scroll"></div>')
    let top = scrollTop
    Object.defineProperty(scroller, 'scrollTop', { get: () => top, set: (v: number) => void (top = v), configurable: true })
    Object.defineProperty(scroller, 'clientHeight', { value: SCROLLER_HEIGHT, configurable: true })
    Object.defineProperty(scroller, 'scrollHeight', { value: 5445, configurable: true })
    const focused = () => document.body.hasAttribute('data-focus')
    const scrollerTop = () => SCROLLER_TOP - (focused() ? FOCUS_LIFT : 0)
    scroller.getBoundingClientRect = () => ({ top: scrollerTop(), bottom: scrollerTop() + SCROLLER_HEIGHT }) as DOMRect
    function block(parent: HTMLElement, { offset, height, rewrap }: Block, tag = 'div', attrs: Record<string, string> = {}): HTMLElement {
      const node = append(document.createElement(tag), parent)
      for (const [name, value] of Object.entries(attrs)) node.setAttribute(name, value)
      node.getBoundingClientRect = () => {
        const y = scrollerTop() + offset - (focused() ? rewrap : 0) - top
        return { top: y, bottom: y + height, height } as DOMRect
      }
      return node
    }
    const item = (parent: HTMLElement, spec: Block) => block(parent, spec, 'div', { 'data-feed-item': '' })
    return { scroller, block, item }
  }

  // The layout flag is set in a layout effect declared BEFORE the hook, as the real class change is.
  function setupMarking() {
    return renderHook((props: FeedFocusOptions) => {
      const mirror = useRef(false)
      useLayoutEffect(() => {
        document.body.toggleAttribute('data-focus', mirror.current)
      })
      const focus = useFeedFocus(props)
      mirror.current = focus.active
      return focus
    }, { initialProps: { available: true, blocked: false } })
  }

  it('keeps the message in the middle of the Feed where it was on screen when focus is entered', () => {
    const { scroller, item } = feedWithLayout(4493)
    item(scroller, { offset: 4300, height: 100, rewrap: 0 })
    item(scroller, { offset: 4620, height: 100, rewrap: 100 })
    const middle = item(scroller, { offset: 4800, height: 100, rewrap: 200 })
    const { result } = setupMarking()
    const before = middle.getBoundingClientRect().top

    act(() => result.current.toggle())
    expect(Math.abs(middle.getBoundingClientRect().top - before)).toBeLessThanOrEqual(2)
  })

  it('returns the message held on entering to its original place on leaving, though another block is now in the middle', () => {
    const { scroller, item } = feedWithLayout(4493)
    item(scroller, { offset: 4600, height: 100, rewrap: 0 })
    const held = item(scroller, { offset: 4700, height: 100, rewrap: 40 })
    item(scroller, { offset: 4800, height: 100, rewrap: 40 })
    const { result } = setupMarking()
    const before = held.getBoundingClientRect().top

    act(() => result.current.toggle())
    press(F, scroller)
    expect(result.current.active).toBe(false)
    expect(Math.abs(held.getBoundingClientRect().top - before)).toBeLessThanOrEqual(2)
  })

  it('holds the block in the middle on leaving when the reader scrolled meanwhile', () => {
    const { scroller, item } = feedWithLayout(4493)
    const above = item(scroller, { offset: 4600, height: 100, rewrap: 0 })
    item(scroller, { offset: 4700, height: 100, rewrap: 40 })
    const { result } = setupMarking()
    act(() => result.current.toggle())
    scroller.scrollTop += 60
    const readingNow = above.getBoundingClientRect().top

    press(F, scroller)
    expect(result.current.active).toBe(false)
    expect(Math.abs(above.getBoundingClientRect().top - readingNow)).toBeLessThanOrEqual(2)
  })

  it('holds the message, not the avatar beside it, when the middle of the Feed crosses the avatar', () => {
    const { scroller, block } = feedWithLayout(4493)
    const group = block(scroller, { offset: 4760, height: 200, rewrap: 0 })
    block(group, { offset: 4760, height: 30, rewrap: 0 }, 'span', { class: 'mesh-avatar' })
    const body = block(group, { offset: 4760, height: 200, rewrap: 0 })
    block(body, { offset: 4760, height: 25, rewrap: 0 }, 'div', { class: 'feed-author-row' })
    const rows = block(body, { offset: 4785, height: 175, rewrap: 0 })
    const read = block(rows, { offset: 4785, height: 80, rewrap: 40 }, 'div', { 'data-feed-item': '' })
    block(rows, { offset: 4865, height: 80, rewrap: 40 }, 'div', { 'data-feed-item': '' })
    const { result } = setupMarking()
    const before = read.getBoundingClientRect().top

    act(() => result.current.toggle())
    expect(Math.abs(read.getBoundingClientRect().top - before)).toBeLessThanOrEqual(2)
  })

  it('holds the call being read inside an expanded tool group shorter than the Feed', () => {
    const { scroller, block } = feedWithLayout(4493)
    const run = block(scroller, { offset: 4500, height: 350, rewrap: 0 }, 'details')
    block(run, { offset: 4500, height: 30, rewrap: 0 }, 'summary', { 'data-feed-item': '' })
    const calls = block(run, { offset: 4530, height: 320, rewrap: 0 })
    block(calls, { offset: 4530, height: 100, rewrap: 0 }, 'div', { 'data-feed-item': '' })
    const read = block(calls, { offset: 4730, height: 100, rewrap: 60 }, 'div', { 'data-feed-item': '' })
    const { result } = setupMarking()
    const before = read.getBoundingClientRect().top

    act(() => result.current.toggle())
    expect(Math.abs(read.getBoundingClientRect().top - before)).toBeLessThanOrEqual(2)
  })

  it('holds the closest message when the middle of the Feed falls in a gap between messages', () => {
    const { scroller, item } = feedWithLayout(4493)
    const above = item(scroller, { offset: 4720, height: 40, rewrap: 0 })
    item(scroller, { offset: 4800, height: 100, rewrap: 80 })
    const { result } = setupMarking()
    const before = above.getBoundingClientRect().top

    act(() => result.current.toggle())
    expect(Math.abs(above.getBoundingClientRect().top - before)).toBeLessThanOrEqual(2)
  })

  it('holds the closest message even when the middle line falls inside a neighbouring group', () => {
    const { scroller, block, item } = feedWithLayout(4493)
    const before = block(scroller, { offset: 4669, height: 92, rewrap: 0 })
    const near = item(before, { offset: 4669, height: 92, rewrap: 0 })
    // The middle line (y=501) is inside this group, but its first message starts 32px below the line.
    const group = block(scroller, { offset: 4761, height: 300, rewrap: 0 })
    item(group, { offset: 4802, height: 100, rewrap: 80 })
    const { result } = setupMarking()
    const was = near.getBoundingClientRect().top

    act(() => result.current.toggle())
    expect(Math.abs(near.getBoundingClientRect().top - was)).toBeLessThanOrEqual(2)
  })

  it('holds the message, not the short list around it, when an earlier message of the list re-wraps', () => {
    const { scroller, block } = feedWithLayout(4493)
    const list = block(scroller, { offset: 4500, height: 400, rewrap: 0 }, 'ul')
    block(list, { offset: 4500, height: 200, rewrap: 0 }, 'li', { 'data-message-id': 'm1' })
    const read = block(list, { offset: 4700, height: 200, rewrap: 60 }, 'li', { 'data-message-id': 'm2' })
    const { result } = setupMarking()
    const before = read.getBoundingClientRect().top

    act(() => result.current.toggle())
    expect(Math.abs(read.getBoundingClientRect().top - before)).toBeLessThanOrEqual(2)
  })

  it('holds the row, not the agent group around it, when an earlier row of the group re-wraps', () => {
    const { scroller, block } = feedWithLayout(4493)
    const group = block(scroller, { offset: 4500, height: 400, rewrap: 0 }, 'div', {})
    const rows = block(group, { offset: 4500, height: 400, rewrap: 0 })
    block(rows, { offset: 4500, height: 200, rewrap: 0 }, 'div', { 'data-feed-item': '' })
    const read = block(rows, { offset: 4700, height: 200, rewrap: 60 }, 'div', { 'data-feed-item': '' })
    const { result } = setupMarking()
    const before = read.getBoundingClientRect().top

    act(() => result.current.toggle())
    expect(Math.abs(read.getBoundingClientRect().top - before)).toBeLessThanOrEqual(2)
  })

  it('holds the text being read inside a block taller than the Feed, not the block itself', () => {
    const { scroller, block, item } = feedWithLayout(4493)
    const tall = block(scroller, { offset: 3500, height: 2000, rewrap: 0 })
    item(tall, { offset: 3500, height: 1000, rewrap: 0 })
    const read = item(tall, { offset: 4500, height: 1000, rewrap: 100 })
    const { result } = setupMarking()
    const before = read.getBoundingClientRect().top

    act(() => result.current.toggle())
    expect(Math.abs(read.getBoundingClientRect().top - before)).toBeLessThanOrEqual(2)
  })

  it('leaves the scroll position alone when the Feed is stuck to the bottom', () => {
    const { scroller } = feedWithLayout(5445 - 555)
    const { result } = setupMarking()
    act(() => result.current.toggle())
    expect(scroller.scrollTop).toBe(5445 - 555)
  })

  it('does not fail when there is no Feed on screen', () => {
    const { result } = setupMarking()
    act(() => result.current.toggle())
    expect(result.current.active).toBe(true)
  })
})
