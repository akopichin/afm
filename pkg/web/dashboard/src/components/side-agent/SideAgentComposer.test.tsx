import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { SideAgentComposer } from './SideAgentComposer'

describe('SideAgentComposer', () => {
  it('disables Send when the draft is empty, enables it when non-empty', () => {
    const { rerender } = render(
      <SideAgentComposer draft="" onDraftChange={vi.fn()} onSend={vi.fn()} onStop={vi.fn()} running={false} />,
    )
    expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled()
    rerender(<SideAgentComposer draft="hi" onDraftChange={vi.fn()} onSend={vi.fn()} onStop={vi.fn()} running={false} />)
    expect(screen.getByRole('button', { name: 'Send' })).not.toBeDisabled()
  })

  it('sends on Cmd/Ctrl+Enter when non-empty and idle', () => {
    const onSend = vi.fn()
    render(<SideAgentComposer draft="hello" onDraftChange={vi.fn()} onSend={onSend} onStop={vi.fn()} running={false} />)
    const ta = screen.getByRole('textbox')
    fireEvent.keyDown(ta, { key: 'Enter', metaKey: true })
    expect(onSend).toHaveBeenCalledTimes(1)
    fireEvent.keyDown(ta, { key: 'Enter', ctrlKey: true })
    expect(onSend).toHaveBeenCalledTimes(2)
  })

  it('plain Enter does not send (newline)', () => {
    const onSend = vi.fn()
    render(<SideAgentComposer draft="hello" onDraftChange={vi.fn()} onSend={onSend} onStop={vi.fn()} running={false} />)
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Enter' })
    expect(onSend).not.toHaveBeenCalled()
  })

  it('does not send whitespace-only drafts', () => {
    const onSend = vi.fn()
    render(<SideAgentComposer draft="   " onDraftChange={vi.fn()} onSend={onSend} onStop={vi.fn()} running={false} />)
    expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled()
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Enter', metaKey: true })
    expect(onSend).not.toHaveBeenCalled()
  })

  it('while running: textarea editable, Send disabled, Stop shown', () => {
    const onStop = vi.fn()
    const onSend = vi.fn()
    render(<SideAgentComposer draft="next" onDraftChange={vi.fn()} onSend={onSend} onStop={onStop} running />)
    expect(screen.getByRole('textbox')).not.toBeDisabled()
    expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled()
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Enter', metaKey: true })
    expect(onSend).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Stop' }))
    expect(onStop).toHaveBeenCalledTimes(1)
  })

  it('no Stop button when idle', () => {
    render(<SideAgentComposer draft="" onDraftChange={vi.fn()} onSend={vi.fn()} onStop={vi.fn()} running={false} />)
    expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull()
  })

  it('propagates typing through onDraftChange', () => {
    const onDraftChange = vi.fn()
    render(<SideAgentComposer draft="" onDraftChange={onDraftChange} onSend={vi.fn()} onStop={vi.fn()} running={false} />)
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'typed' } })
    expect(onDraftChange).toHaveBeenCalledWith('typed')
  })
})
