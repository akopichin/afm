import { act, renderHook } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { useSideAgentShortcut, type SideAgentShortcutOptions } from './use-side-agent-shortcut'

function press(init: KeyboardEventInit): KeyboardEvent {
  const e = new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init })
  act(() => { document.dispatchEvent(e) })
  return e
}

function setup(over: Partial<SideAgentShortcutOptions> = {}) {
  const onToggle = vi.fn()
  const onClose = vi.fn()
  const opts: SideAgentShortcutOptions = { isOpen: false, onToggle, onClose, ...over }
  const { rerender } = renderHook((p: SideAgentShortcutOptions) => useSideAgentShortcut(p), { initialProps: opts })
  return { onToggle, onClose, rerender, opts }
}

describe('useSideAgentShortcut', () => {
  it('toggles on Cmd+Backquote (physical code, layout-independent)', () => {
    const { onToggle } = setup()
    const e = press({ code: 'Backquote', key: '`', metaKey: true })
    expect(onToggle).toHaveBeenCalledTimes(1)
    expect(e.defaultPrevented).toBe(true)
  })

  it('toggles on Cmd+Shift+Backquote (the Cmd+~ variant)', () => {
    const { onToggle } = setup()
    press({ code: 'Backquote', key: '~', metaKey: true, shiftKey: true })
    expect(onToggle).toHaveBeenCalledTimes(1)
  })

  it('toggles on the Ctrl+Backquote fallback', () => {
    const { onToggle } = setup()
    press({ code: 'Backquote', key: '`', ctrlKey: true })
    expect(onToggle).toHaveBeenCalledTimes(1)
  })

  it('ignores a different physical key even if it prints a backtick', () => {
    const { onToggle } = setup()
    press({ code: 'Digit2', key: '`', metaKey: true })
    expect(onToggle).not.toHaveBeenCalled()
  })

  it('ignores Backquote without Cmd/Ctrl', () => {
    const { onToggle } = setup()
    press({ code: 'Backquote', key: '`' })
    expect(onToggle).not.toHaveBeenCalled()
  })

  it('ignores the combo with an extra Alt modifier', () => {
    const { onToggle } = setup()
    press({ code: 'Backquote', key: '`', metaKey: true, altKey: true })
    expect(onToggle).not.toHaveBeenCalled()
  })

  it('ignores auto-repeat and IME composition', () => {
    const { onToggle } = setup()
    press({ code: 'Backquote', key: '`', metaKey: true, repeat: true })
    press({ code: 'Backquote', key: '`', metaKey: true, isComposing: true })
    press({ code: 'Backquote', key: '`', metaKey: true, keyCode: 229 })
    expect(onToggle).not.toHaveBeenCalled()
  })

  it('does not open over another modal (blocked) but still closes when already open', () => {
    const blocked = setup({ blocked: true, isOpen: false })
    press({ code: 'Backquote', key: '`', metaKey: true })
    expect(blocked.onToggle).not.toHaveBeenCalled()

    const open = setup({ blocked: true, isOpen: true })
    press({ code: 'Backquote', key: '`', metaKey: true })
    expect(open.onToggle).toHaveBeenCalledTimes(1)
  })

  it('Escape closes only when open and consumes the event', () => {
    const closed = setup({ isOpen: false })
    const e1 = press({ code: 'Escape', key: 'Escape' })
    expect(closed.onClose).not.toHaveBeenCalled()
    expect(e1.defaultPrevented).toBe(false)

    const open = setup({ isOpen: true })
    const e2 = press({ code: 'Escape', key: 'Escape' })
    expect(open.onClose).toHaveBeenCalledTimes(1)
    expect(e2.defaultPrevented).toBe(true)
  })

  it('removes its listener on unmount', () => {
    const onToggle = vi.fn()
    const { unmount } = renderHook(() => useSideAgentShortcut({ isOpen: false, onToggle, onClose: vi.fn() }))
    unmount()
    press({ code: 'Backquote', key: '`', metaKey: true })
    expect(onToggle).not.toHaveBeenCalled()
  })
})
