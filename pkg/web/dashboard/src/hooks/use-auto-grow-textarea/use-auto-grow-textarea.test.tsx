import { StrictMode } from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen } from '@testing-library/react'
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

// Model conditional textarea mounting with a trailing action button, as PlanPanel/DialogChannel do.
function TestComponentWithSibling({ value, maxHeight }: { value: string; maxHeight: number }) {
  const ref = useAutoGrowTextarea(value, maxHeight)
  return (
    <>
      <textarea ref={ref} data-testid="textarea" defaultValue={value} />
      <button data-testid="next-sibling">Add</button>
    </>
  )
}

function ConditionalTestComponent({
  show,
  value,
  maxHeight,
}: {
  show: boolean
  value: string
  maxHeight: number
}) {
  const ref = useAutoGrowTextarea(value, maxHeight)
  if (!show) return null
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

  it('leaves a hidden field alone and measures it when it is shown again', () => {
    const { rerender } = render(<TestComponent value="three lines" maxHeight={400} />)
    const textarea = screen.getByTestId('textarea') as HTMLTextAreaElement
    const layout = { scrollHeight: 90, clientHeight: 90 }
    Object.defineProperty(textarea, 'scrollHeight', { get: () => layout.scrollHeight, configurable: true })
    Object.defineProperty(textarea, 'clientHeight', { get: () => layout.clientHeight, configurable: true })
    rerender(<TestComponent value="three lines and more" maxHeight={400} />)
    expect(textarea.style.height).toBe('90px')

    // display: none — no layout. A send that completes meanwhile trims the draft.
    Object.assign(layout, { scrollHeight: 0, clientHeight: 0 })
    rerender(<TestComponent value="more" maxHeight={400} />)
    expect(textarea.style.height).toBe('90px')

    // Shown again: the remaining draft is measured.
    Object.assign(layout, { scrollHeight: 40, clientHeight: 40 })
    lastObserver?.trigger()
    expect(textarea.style.height).toBe('40px')
  })

  it('grows height to scrollHeight as the value changes', () => {
    const { rerender } = render(<TestComponent value="" maxHeight={400} />)
    const textarea = screen.getByTestId('textarea') as HTMLTextAreaElement

    // Mock scrollHeight
    Object.defineProperty(textarea, 'scrollHeight', { value: 120, configurable: true })

    rerender(<TestComponent value="some text" maxHeight={400} />)

    expect(textarea.style.height).toBe('120px')
  })

  it('does NOT scrollIntoView on mount, but DOES on a later value change (typing)', () => {
    const spy = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})
    try {
      const { rerender } = render(<TestComponent value="" maxHeight={400} />)
      const textarea = screen.getByTestId('textarea') as HTMLTextAreaElement
      // Mount ran the layout effect once — it must NOT have scrolled the page.
      expect(spy).not.toHaveBeenCalled()

      // A subsequent value change (the user typing) DOES keep the field visible.
      Object.defineProperty(textarea, 'scrollHeight', { value: 120, configurable: true })
      rerender(<TestComponent value="typed" maxHeight={400} />)
      expect(spy).toHaveBeenCalled()
    } finally {
      spy.mockRestore()
    }
  })

  it('does NOT scrollIntoView on mount even under StrictMode (layout effect replay)', () => {
    // StrictMode double-invokes layout effects in dev; a "first effect run" counter would flip on the
    // first pass and let the replay scroll.
    const spy = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})
    try {
      render(
        <StrictMode>
          <TestComponent value="" maxHeight={400} />
        </StrictMode>,
      )
      expect(spy).not.toHaveBeenCalled()
    } finally {
      spy.mockRestore()
    }
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

  it('scrolls the next sibling (action button) into view instead of the textarea itself', () => {
    const { rerender } = render(<TestComponentWithSibling value="" maxHeight={400} />)
    const textarea = screen.getByTestId('textarea') as HTMLTextAreaElement
    const sibling = screen.getByTestId('next-sibling') as HTMLButtonElement

    const textareaSpy = vi.spyOn(textarea, 'scrollIntoView')
    const siblingSpy = vi.spyOn(sibling, 'scrollIntoView')

    Object.defineProperty(textarea, 'scrollHeight', { value: 400, configurable: true })
    rerender(<TestComponentWithSibling value="a long comment that grows the box" maxHeight={400} />)

    expect(siblingSpy).toHaveBeenCalledWith({ block: 'nearest', inline: 'nearest' })
    expect(textareaSpy).not.toHaveBeenCalled()
  })

  it('falls back to scrolling the textarea itself when it has no next sibling', () => {
    const { rerender } = render(<TestComponent value="" maxHeight={400} />)
    const textarea = screen.getByTestId('textarea') as HTMLTextAreaElement
    const textareaSpy = vi.spyOn(textarea, 'scrollIntoView')

    Object.defineProperty(textarea, 'scrollHeight', { value: 120, configurable: true })
    rerender(<TestComponent value="some text" maxHeight={400} />)

    expect(textareaSpy).toHaveBeenCalledWith({ block: 'nearest', inline: 'nearest' })
  })

  it('engages the manual-resize lock even when the textarea mounts after the hook first runs', () => {
    // Comment form starts closed — the hook runs (at the parent's top level)
    // before the textarea exists at all.
    const { rerender } = render(<ConditionalTestComponent show={false} value="" maxHeight={400} />)
    expect(screen.queryByTestId('textarea')).toBeNull()

    // Comment form "opens" — the textarea mounts well after the hook's first
    // render. This is exactly the late-mount scenario from PlanPanel/DialogChannel.
    rerender(<ConditionalTestComponent show={true} value="" maxHeight={400} />)
    const textarea = screen.getByTestId('textarea') as HTMLTextAreaElement

    Object.defineProperty(textarea, 'scrollHeight', { value: 120, configurable: true })
    rerender(<ConditionalTestComponent show={true} value="first draft" maxHeight={400} />)
    expect(textarea.style.height).toBe('120px')

    // Simulate the user dragging the resize handle on the late-mounted textarea.
    textarea.style.height = '250px'
    lastObserver?.trigger()

    // Change scrollHeight (simulating more content would create more scroll).
    Object.defineProperty(textarea, 'scrollHeight', { value: 500, configurable: true })

    // Rerender with a different value — height should stay locked at 250px.
    rerender(<ConditionalTestComponent show={true} value="first draft, now much longer" maxHeight={400} />)
    expect(textarea.style.height).toBe('250px')
  })
})
