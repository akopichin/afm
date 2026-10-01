import { render, screen, fireEvent, act } from '@testing-library/react'
import { afterEach, beforeEach, describe, it, expect, vi } from 'vitest'
import { SideAgentModal } from './SideAgentModal'
import type { SideAgentApi } from '../../hooks/use-side-agent'

function fakeApi(over: Partial<SideAgentApi> = {}): SideAgentApi {
  return {
    events: [],
    requestState: 'idle',
    unavailable: false,
    command: 'claude',
    draft: '',
    setDraft: vi.fn(),
    isOpen: true,
    open: vi.fn(),
    close: vi.fn(),
    unseenResult: false,
    error: null,
    send: vi.fn().mockResolvedValue(undefined),
    interrupt: vi.fn(),
    ...over,
  }
}

describe('SideAgentModal', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => { vi.runOnlyPendingTimers(); vi.useRealTimers() })

  function renderModal(over: Partial<SideAgentApi> = {}) {
    let utils!: ReturnType<typeof render>
    act(() => { utils = render(<SideAgentModal api={fakeApi(over)} />) })
    return utils
  }

  it('is an accessible dialog with a labelled title when open', () => {
    renderModal({ isOpen: true })
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(dialog).toHaveAttribute('aria-labelledby', 'side-agent-title')
    expect(document.getElementById('side-agent-title')?.textContent).toContain('Side agent')
  })

  it('shows the configured command name', () => {
    renderModal({ isOpen: true, command: 'codex-as-claude' })
    expect(screen.getByText('codex-as-claude')).toBeInTheDocument()
  })

  it('is hidden and inert when closed', () => {
    const { container } = renderModal({ isOpen: false })
    // getByRole excludes hidden subtrees → dialog is not accessible when closed
    expect(screen.queryByRole('dialog')).toBeNull()
    const overlay = document.querySelector('.side-agent-overlay') as HTMLElement
    expect(overlay).toHaveAttribute('hidden')
    expect(overlay.inert).toBe(true)
    expect(container).toBeDefined()
  })

  it('close button calls api.close (hide only, agent keeps running)', () => {
    const close = vi.fn()
    renderModal({ isOpen: true, close })
    fireEvent.click(screen.getByRole('button', { name: /hide side agent/i }))
    expect(close).toHaveBeenCalledTimes(1)
  })

  it('closes on a scrim press that both starts and ends on the scrim', () => {
    const close = vi.fn()
    renderModal({ isOpen: true, close })
    const overlay = document.querySelector('.side-agent-overlay') as HTMLElement
    fireEvent.mouseDown(overlay)
    fireEvent.mouseUp(overlay)
    expect(close).toHaveBeenCalledTimes(1)
  })

  it('does NOT close when the press starts inside the dialog and ends on the scrim', () => {
    const close = vi.fn()
    renderModal({ isOpen: true, close })
    const overlay = document.querySelector('.side-agent-overlay') as HTMLElement
    const dialog = screen.getByRole('dialog')
    fireEvent.mouseDown(dialog) // selection starts inside
    fireEvent.mouseUp(overlay) // released on scrim
    expect(close).not.toHaveBeenCalled()
  })

  it('focuses the composer textarea on open', () => {
    renderModal({ isOpen: true })
    expect(document.activeElement).toBe(screen.getByRole('textbox'))
  })

  it('restores focus to the opener after it fully closes', () => {
    const opener = document.createElement('button')
    document.body.appendChild(opener)
    opener.focus()
    const api = fakeApi({ isOpen: true })
    const { rerender } = render(<SideAgentModal api={api} />)
    act(() => {}) // flush open effects (steals focus to textarea)
    expect(document.activeElement).not.toBe(opener)
    act(() => { rerender(<SideAgentModal api={{ ...api, isOpen: false }} />) })
    act(() => { vi.advanceTimersByTime(300) }) // close animation window elapses
    expect(document.activeElement).toBe(opener)
    opener.remove()
  })

  it('reopening during the close animation keeps the dialog open (fast toggle)', () => {
    const api = fakeApi({ isOpen: true })
    const { rerender } = render(<SideAgentModal api={api} />)
    act(() => {})
    act(() => { rerender(<SideAgentModal api={{ ...api, isOpen: false }} />) }) // start closing
    act(() => { rerender(<SideAgentModal api={{ ...api, isOpen: true }} />) }) // reopen before timer
    act(() => { vi.advanceTimersByTime(300) }) // stale close timer must not hide it
    const overlay = document.querySelector('.side-agent-overlay') as HTMLElement
    expect(overlay).toHaveClass('side-agent-open')
    expect(overlay).not.toHaveAttribute('hidden')
  })

  it('traps Tab focus within the dialog', () => {
    renderModal({ isOpen: true, draft: 'x' })
    const dialog = screen.getByRole('dialog')
    const focusables = Array.from(dialog.querySelectorAll<HTMLElement>('button, textarea'))
    const first = focusables[0]!
    const last = focusables[focusables.length - 1]!
    last.focus()
    fireEvent.keyDown(dialog, { key: 'Tab' })
    expect(document.activeElement).toBe(first)
    first.focus()
    fireEvent.keyDown(dialog, { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(last)
  })

  it('Stop button interrupts the active turn', () => {
    const interrupt = vi.fn()
    renderModal({ isOpen: true, requestState: 'running', interrupt })
    fireEvent.click(screen.getByRole('button', { name: 'Stop' }))
    expect(interrupt).toHaveBeenCalledTimes(1)
  })

  it('renders the attention shortcut and hides the modal before activating it', () => {
    const close = vi.fn()
    const onActivate = vi.fn()
    let utils!: ReturnType<typeof render>
    act(() => {
      utils = render(
        <SideAgentModal api={fakeApi({ isOpen: true, close })} attentionShortcut={{ label: 'Question waiting', onActivate }} />,
      )
    })
    void utils
    const btn = screen.getByRole('button', { name: /question waiting/i })
    fireEvent.click(btn)
    // close() вызывается ДО onActivate() — работа агента сохраняется, воркспейс
    // переключается на ожидающую стадию.
    expect(close).toHaveBeenCalledTimes(1)
    expect(onActivate).toHaveBeenCalledTimes(1)
    const closeOrder = close.mock.invocationCallOrder[0] ?? Infinity
    const activateOrder = onActivate.mock.invocationCallOrder[0] ?? 0
    expect(closeOrder).toBeLessThan(activateOrder)
  })

  it('has no attention shortcut when none is pending', () => {
    renderModal({ isOpen: true })
    expect(screen.queryByText(/waiting/i)).toBeNull()
  })

  it('shows an error banner when api.error is set', () => {
    renderModal({ isOpen: true, error: 'Message too large' })
    expect(screen.getByRole('alert')).toHaveTextContent('Message too large')
  })

  it('sends the current draft via api.send', () => {
    const send = vi.fn().mockResolvedValue(undefined)
    renderModal({ isOpen: true, draft: 'ping', send })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(send).toHaveBeenCalledWith('ping')
  })
})
