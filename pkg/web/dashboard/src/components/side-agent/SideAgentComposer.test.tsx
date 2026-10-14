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

  it('no quote chip when quote is null/omitted', () => {
    render(<SideAgentComposer draft="" onDraftChange={vi.fn()} onSend={vi.fn()} onStop={vi.fn()} running={false} />)
    expect(document.querySelector('.side-agent-quote-chip')).toBeNull()
  })

  it('shows the quote chip with label and verbatim body when quote is set', () => {
    const quote = '## Review notes\n- Line 1; original: "foo": bar'
    render(
      <SideAgentComposer draft="" onDraftChange={vi.fn()} onSend={vi.fn()} onStop={vi.fn()} running={false} quote={quote} />,
    )
    const chip = document.querySelector('.side-agent-quote-chip')
    expect(chip).toBeInTheDocument()
    expect(screen.getByText('↩ File comments')).toBeInTheDocument()
    // Verbatim — not wrapped in a "> " blockquote (that prefixing happens later, at send time).
    const body = document.querySelector('.side-agent-quote-chip-body')
    expect(body?.textContent).toBe(quote)
    expect(body?.textContent?.startsWith('>')).toBe(false)
  })

  it('clicking the chip cancel button calls onCancelQuote', () => {
    const onCancelQuote = vi.fn()
    render(
      <SideAgentComposer
        draft=""
        onDraftChange={vi.fn()}
        onSend={vi.fn()}
        onStop={vi.fn()}
        running={false}
        quote="Q"
        onCancelQuote={onCancelQuote}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Cancel attached file comments' }))
    expect(onCancelQuote).toHaveBeenCalledTimes(1)
  })

  it('quote does not gate sendability: Send still needs a non-empty draft', () => {
    const { rerender } = render(
      <SideAgentComposer draft="" onDraftChange={vi.fn()} onSend={vi.fn()} onStop={vi.fn()} running={false} quote="Q" />,
    )
    expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled()
    rerender(
      <SideAgentComposer draft="hi" onDraftChange={vi.fn()} onSend={vi.fn()} onStop={vi.fn()} running={false} quote="Q" />,
    )
    expect(screen.getByRole('button', { name: 'Send' })).not.toBeDisabled()
  })
})
