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

// Mirrors PlanPanel/DialogChannel's actual pattern: the hook is called at the
// parent's top level, but the textarea itself is rendered conditionally (only
// when a comment form is "open") — so the DOM node does not exist yet at the
// hook's own first render.
// Mirrors PlanPanel/DialogChannel: a trailing action button as the
// textarea's DOM nextElementSibling.
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
    // Against the old useRef([]) implementation, the ResizeObserver was never
    // created (ref.current was null at mount), so lastObserver stays null,
    // the trigger() above is a no-op, the lock never engages, and this would
    // snap back to min(500, 400) = 400px instead of staying at 250px.
    rerender(<ConditionalTestComponent show={true} value="first draft, now much longer" maxHeight={400} />)
    expect(textarea.style.height).toBe('250px')
  })
})
