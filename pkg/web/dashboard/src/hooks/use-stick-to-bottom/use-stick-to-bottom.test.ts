import { afterEach, describe, it, expect, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useStickToBottom } from './use-stick-to-bottom'

describe('useStickToBottom', () => {
  it('stick=true by default; ref is a callback, jumpToBottom is a function', () => {
    const { result } = renderHook(() => useStickToBottom<HTMLDivElement>())
    expect(result.current.stick).toBe(true)
    expect(typeof result.current.jumpToBottom).toBe('function')
    expect(typeof result.current.ref).toBe('function')
  })

  it('scrolls to the bottom immediately when the node attaches after content already exists', () => {
    const { result } = renderHook(() => useStickToBottom<HTMLDivElement>())

    const el = document.createElement('div')
    Object.defineProperty(el, 'scrollHeight', { value: 500, configurable: true })
    el.scrollTop = 0

    // Simulates DialogChannel: the container mounts (ref callback fires) only after history/pending
    // content is already in the DOM, unlike a container that's always mounted from the first render.
    act(() => {
      result.current.ref(el)
    })

    expect(el.scrollTop).toBe(500)
  })

  it('release() stops the observer from re-pinning to the bottom on later content growth', async () => {
    const { result } = renderHook(() => useStickToBottom<HTMLDivElement>())

    const el = document.createElement('div')
    Object.defineProperty(el, 'scrollHeight', { value: 500, configurable: true })
    Object.defineProperty(el, 'clientHeight', { value: 100, configurable: true })
    el.scrollTop = 0

    act(() => {
      result.current.ref(el)
    })
    expect(el.scrollTop).toBe(500) // stuck to bottom on attach

    // A targeted navigation scrolls up AND releases stick.
    act(() => {
      el.scrollTop = 120
      result.current.release()
    })
    expect(result.current.stick).toBe(false)

    // Content grows (MutationObserver fires). With stick released, it must NOT
    // yank scrollTop back to the bottom — the user stays where they navigated.
    await act(async () => {
      Object.defineProperty(el, 'scrollHeight', { value: 900, configurable: true })
      el.appendChild(document.createElement('div'))
      await Promise.resolve()
    })
    expect(el.scrollTop).toBe(120)
  })

  it('re-attaching to a new node (e.g. after the container remounts) tracks the new node, not the old one', () => {
    const { result } = renderHook(() => useStickToBottom<HTMLDivElement>())

    const first = document.createElement('div')
    Object.defineProperty(first, 'scrollHeight', { value: 100, configurable: true })

    act(() => {
      result.current.ref(first)
    })
    expect(first.scrollTop).toBe(100)

    act(() => {
      result.current.ref(null)
    })

    const second = document.createElement('div')
    Object.defineProperty(second, 'scrollHeight', { value: 900, configurable: true })
    second.scrollTop = 0

    act(() => {
      result.current.ref(second)
    })
    expect(second.scrollTop).toBe(900)
  })

  describe('a hidden container that is shown again', () => {
    afterEach(() => vi.unstubAllGlobals())

    // stubResize installs a ResizeObserver whose callback the test fires by hand.
    function stubResize(): { fire: () => void } {
      let callback: (() => void) | null = null
      vi.stubGlobal('ResizeObserver', class {
        constructor(cb: () => void) { callback = cb }
        observe() {}
        disconnect() {}
      })
      return { fire: () => callback?.() }
    }

    // box — a scroll container whose layout the test controls (jsdom has no layout).
    function box(): { el: HTMLDivElement; layout: (scrollHeight: number, clientHeight: number) => void } {
      const el = document.createElement('div')
      const size = { scrollHeight: 0, clientHeight: 0 }
      Object.defineProperty(el, 'scrollHeight', { get: () => size.scrollHeight, configurable: true })
      Object.defineProperty(el, 'clientHeight', { get: () => size.clientHeight, configurable: true })
      return { el, layout: (scrollHeight, clientHeight) => Object.assign(size, { scrollHeight, clientHeight }) }
    }

    it('returns a reader who scrolled up to where they left, clamped to the new maximum', () => {
      const resize = stubResize()
      const { result } = renderHook(() => useStickToBottom<HTMLDivElement>())
      const { el, layout } = box()
      layout(2000, 400)
      act(() => result.current.ref(el))
      act(() => {
        el.scrollTop = 900
        el.dispatchEvent(new Event('scroll'))
      })
      expect(result.current.stick).toBe(false)

      // Hidden: no layout, and the browser reports scrollTop 0.
      layout(0, 0)
      act(() => {
        el.scrollTop = 0
        el.dispatchEvent(new Event('scroll'))
        resize.fire()
      })
      expect(result.current.stick).toBe(false)

      layout(2000, 400)
      act(() => resize.fire())
      expect(el.scrollTop).toBe(900)

      // A wider window wraps less text: the old offset no longer fits.
      layout(0, 0)
      act(() => resize.fire())
      layout(1000, 400)
      act(() => resize.fire())
      expect(el.scrollTop).toBe(600)
    })

    it('keeps a reader at the bottom on the bottom when shown again', () => {
      const resize = stubResize()
      const { result } = renderHook(() => useStickToBottom<HTMLDivElement>())
      const { el, layout } = box()
      layout(2000, 400)
      act(() => result.current.ref(el))
      layout(0, 0)
      act(() => resize.fire())
      layout(2600, 400)
      act(() => resize.fire())
      expect(el.scrollTop).toBe(2600)
    })
  })
})
