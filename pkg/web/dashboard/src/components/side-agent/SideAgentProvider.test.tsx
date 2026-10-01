import { render, screen, fireEvent, act, waitFor } from '@testing-library/react'
import { afterEach, describe, it, expect, vi } from 'vitest'
import { SideAgentProvider, useSideAgentContext } from './SideAgentProvider'
import type { SideAgentDeps } from '../../hooks/use-side-agent'

function fakeDeps(): SideAgentDeps {
  return {
    getConversation: vi.fn().mockResolvedValue({
      conversationId: 'c1', runId: 'r1', command: 'claude', unavailable: false,
      state: 'idle', events: [], lastSeq: 0, nextCursor: 0, hasMore: false,
    }),
    sendMessage: vi.fn().mockResolvedValue({ turnId: 't1', state: 'running', cursor: 1 }),
    interrupt: vi.fn().mockResolvedValue(undefined),
    openStream: vi.fn(() => ({ close: vi.fn() })),
  }
}

// Потребитель контекста — рендерит кнопку open(), как это делает реальная
// шапка (GlobalHeader.SideAgentHeaderButton).
function Consumer(): React.ReactElement {
  const { enabled, open } = useSideAgentContext()
  return (
    <button type="button" onClick={open} data-enabled={enabled}>
      side-agent
    </button>
  )
}

function press(init: KeyboardEventInit): void {
  act(() => {
    document.dispatchEvent(new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init }))
  })
}

describe('SideAgentProvider (App-level wiring)', () => {
  afterEach(() => vi.restoreAllMocks())

  it('is inert when disabled: no modal mounts even if open() is called', () => {
    render(
      <SideAgentProvider runId="r1" enabled={false} deps={fakeDeps()}>
        <Consumer />
      </SideAgentProvider>,
    )
    expect(screen.getByRole('button', { name: 'side-agent' })).toHaveAttribute('data-enabled', 'false')
    // enabled=false → модалка не монтируется вовсе.
    expect(document.querySelector('.side-agent-overlay')).toBeNull()
  })

  it('enabled: context open() mounts and opens the modal, and fires onOpenChange', async () => {
    const onOpenChange = vi.fn()
    render(
      <SideAgentProvider runId="r1" enabled deps={fakeDeps()} onOpenChange={onOpenChange}>
        <Consumer />
      </SideAgentProvider>,
    )
    // Смонтирована, но скрыта до open.
    expect(screen.queryByRole('dialog')).toBeNull()
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'side-agent' })) })
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument())
    expect(onOpenChange).toHaveBeenCalledWith(true)
  })

  it('global shortcut Cmd+` opens the modal when not blocked', async () => {
    render(
      <SideAgentProvider runId="r1" enabled blocked={false} deps={fakeDeps()}>
        <Consumer />
      </SideAgentProvider>,
    )
    press({ code: 'Backquote', key: '`', metaKey: true })
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument())
  })

  it('global shortcut does NOT open the modal when blocked (another modal is up)', () => {
    render(
      <SideAgentProvider runId="r1" enabled blocked deps={fakeDeps()}>
        <Consumer />
      </SideAgentProvider>,
    )
    press({ code: 'Backquote', key: '`', metaKey: true })
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})
