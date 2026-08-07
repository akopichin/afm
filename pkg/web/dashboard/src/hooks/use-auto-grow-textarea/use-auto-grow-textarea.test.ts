import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook } from '@testing-library/react'
import { useAutoGrowTextarea } from './use-auto-grow-textarea'

class MockResizeObserver {
  callback: ResizeObserverCallback
  constructor(callback: ResizeObserverCallback) {
    this.callback = callback
  }
  observe() {}
  disconnect() {}
  unobserve() {}
  trigger() {
    this.callback([] as unknown as ResizeObserverEntry[], this as unknown as ResizeObserver)
  }
}

function makeTextarea(scrollHeight: number): HTMLTextAreaElement {
  const el = document.createElement('textarea')
  Object.defineProperty(el, 'scrollHeight', { value: scrollHeight, configurable: true })
  return el
}

describe('useAutoGrowTextarea', () => {
  let lastObserver: MockResizeObserver | null = null

  beforeEach(() => {
    lastObserver = null
    vi.stubGlobal(
      'ResizeObserver',
      class extends MockResizeObserver {
        constructor(callback: ResizeObserverCallback) {
          super(callback)
          lastObserver = this
        }
      },
    )
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('grows height to scrollHeight as the value changes', () => {
    const el = makeTextarea(120)
    const { result, rerender } = renderHook(({ value }) => useAutoGrowTextarea(value, 400), {
      initialProps: { value: '' },
    })
    result.current.current = el

    rerender({ value: 'some text' })

    expect(el.style.height).toBe('120px')
  })

  it('clamps growth at maxHeightPx', () => {
    const el = makeTextarea(999)
    const { result, rerender } = renderHook(({ value }) => useAutoGrowTextarea(value, 400), {
      initialProps: { value: '' },
    })
    result.current.current = el

    rerender({ value: 'a very long comment' })

    expect(el.style.height).toBe('400px')
  })

  it('locks out auto-grow after a manual resize, and stays put on further value changes', () => {
    const el = makeTextarea(120)
    const { result, rerender } = renderHook(({ value }) => useAutoGrowTextarea(value, 400), {
      initialProps: { value: '' },
    })
    result.current.current = el
    rerender({ value: 'first draft' })
    expect(el.style.height).toBe('120px')

    // Simulate the user dragging the resize handle (the browser sets an
    // inline height on the element, same as our hook does).
    el.style.height = '250px'
    lastObserver?.trigger()

    Object.defineProperty(el, 'scrollHeight', { value: 500, configurable: true })
    rerender({ value: 'first draft, now much longer' })

    expect(el.style.height).toBe('250px')
  })

  it('resets the lock once the value goes back to empty', () => {
    const el = makeTextarea(120)
    const { result, rerender } = renderHook(({ value }) => useAutoGrowTextarea(value, 400), {
      initialProps: { value: '' },
    })
    result.current.current = el
    rerender({ value: 'first draft' })

    el.style.height = '250px'
    lastObserver?.trigger()
    rerender({ value: 'first draft, locked' })
    expect(el.style.height).toBe('250px')

    Object.defineProperty(el, 'scrollHeight', { value: 90, configurable: true })
    rerender({ value: '' })
    rerender({ value: 'a fresh comment' })

    expect(el.style.height).toBe('90px')
  })
})
