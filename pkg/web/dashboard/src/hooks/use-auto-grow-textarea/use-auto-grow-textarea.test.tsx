import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import React from 'react'
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

let lastObserver: MockResizeObserver | null = null

function TestComponent({ value, maxHeight }: { value: string; maxHeight: number }) {
  const ref = useAutoGrowTextarea(value, maxHeight)
  return <textarea ref={ref} data-testid="textarea" defaultValue={value} />
}

describe('useAutoGrowTextarea', () => {
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
    const { rerender } = render(<TestComponent value="" maxHeight={400} />)
    const textarea = screen.getByTestId('textarea') as HTMLTextAreaElement

    // Mock scrollHeight
    Object.defineProperty(textarea, 'scrollHeight', { value: 120, configurable: true })

    rerender(<TestComponent value="some text" maxHeight={400} />)

    expect(textarea.style.height).toBe('120px')
  })

  it('clamps growth at maxHeightPx', () => {
    const { rerender } = render(<TestComponent value="" maxHeight={400} />)
    const textarea = screen.getByTestId('textarea') as HTMLTextAreaElement

    // Mock scrollHeight to exceed maxHeight
    Object.defineProperty(textarea, 'scrollHeight', { value: 999, configurable: true })

    rerender(<TestComponent value="a very long comment" maxHeight={400} />)

    expect(textarea.style.height).toBe('400px')
  })

  it('locks out auto-grow after a manual resize, and stays put on further value changes', () => {
    const { rerender } = render(<TestComponent value="" maxHeight={400} />)
    const textarea = screen.getByTestId('textarea') as HTMLTextAreaElement

    // First render: hook sets height based on scrollHeight
    Object.defineProperty(textarea, 'scrollHeight', { value: 120, configurable: true })
    rerender(<TestComponent value="first draft" maxHeight={400} />)
    expect(textarea.style.height).toBe('120px')

    // Simulate user dragging the resize handle
    textarea.style.height = '250px'
    lastObserver?.trigger()

    // Change scrollHeight (simulating more content would create more scroll)
    Object.defineProperty(textarea, 'scrollHeight', { value: 500, configurable: true })

    // Rerender with different value — height should stay locked at 250px
    rerender(<TestComponent value="first draft, now much longer" maxHeight={400} />)

    expect(textarea.style.height).toBe('250px')
  })

  it('resets the lock once the value goes back to empty', () => {
    const { rerender } = render(<TestComponent value="" maxHeight={400} />)
    const textarea = screen.getByTestId('textarea') as HTMLTextAreaElement

    // First value: hook sets height
    Object.defineProperty(textarea, 'scrollHeight', { value: 120, configurable: true })
    rerender(<TestComponent value="first draft" maxHeight={400} />)

    // Simulate manual resize
    textarea.style.height = '250px'
    lastObserver?.trigger()

    // Further value change — should stay locked
    rerender(<TestComponent value="first draft, locked" maxHeight={400} />)
    expect(textarea.style.height).toBe('250px')

    // Change scrollHeight
    Object.defineProperty(textarea, 'scrollHeight', { value: 90, configurable: true })

    // Clear value — should reset lock
    rerender(<TestComponent value="" maxHeight={400} />)

    // New value — lock should be released, hook should grow again
    rerender(<TestComponent value="a fresh comment" maxHeight={400} />)

    expect(textarea.style.height).toBe('90px')
  })
})
