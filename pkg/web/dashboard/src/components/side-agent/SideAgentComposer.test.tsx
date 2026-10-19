import { useState, type ReactElement } from 'react'
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { SideAgentComposer, type SideAgentComposerProps } from './SideAgentComposer'
import type { AssistantProfile } from '../../types/assistant-profile'

const PROFILES: AssistantProfile[] = [
  { id: 'frontendDeveloper', title: 'Frontend reviewer', command: 'claude', available: true, unavailableReason: '' },
  { id: 'qaEngineer', title: 'QA engineer', command: 'claude', available: true, unavailableReason: '' },
]

// ControlledComposer — a real draft/onDraftChange round trip (unlike a static `draft` prop + a bare vi.fn(),
// which would let the textarea's displayed value silently drift from what replaceRange/insertAtCaret
// actually compute from — see use-caret-insert.ts: it reads the `draft` PROP as its base value, exactly
// like production's SideAgentModal does via useSideAgent's draft/setDraft). The mention tests below insert
// text via replaceRange, so they need this real round trip to observe the final, correct draft.
function ControlledComposer(props: Omit<SideAgentComposerProps, 'draft' | 'onDraftChange'> & { initialDraft?: string }): ReactElement {
  const [draft, setDraft] = useState(props.initialDraft ?? '')
  return <SideAgentComposer {...props} draft={draft} onDraftChange={setDraft} />
}

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
    const ta = screen.getByRole('combobox')
    fireEvent.keyDown(ta, { key: 'Enter', metaKey: true })
    expect(onSend).toHaveBeenCalledTimes(1)
    fireEvent.keyDown(ta, { key: 'Enter', ctrlKey: true })
    expect(onSend).toHaveBeenCalledTimes(2)
  })

  it('plain Enter does not send (newline)', () => {
    const onSend = vi.fn()
    render(<SideAgentComposer draft="hello" onDraftChange={vi.fn()} onSend={onSend} onStop={vi.fn()} running={false} />)
    fireEvent.keyDown(screen.getByRole('combobox'), { key: 'Enter' })
    expect(onSend).not.toHaveBeenCalled()
  })

  it('does not send whitespace-only drafts', () => {
    const onSend = vi.fn()
    render(<SideAgentComposer draft="   " onDraftChange={vi.fn()} onSend={onSend} onStop={vi.fn()} running={false} />)
    expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled()
    fireEvent.keyDown(screen.getByRole('combobox'), { key: 'Enter', metaKey: true })
    expect(onSend).not.toHaveBeenCalled()
  })

  it('while running: textarea editable, Send disabled, Stop shown', () => {
    const onStop = vi.fn()
    const onSend = vi.fn()
    render(<SideAgentComposer draft="next" onDraftChange={vi.fn()} onSend={onSend} onStop={onStop} running />)
    expect(screen.getByRole('combobox')).not.toBeDisabled()
    expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled()
    fireEvent.keyDown(screen.getByRole('combobox'), { key: 'Enter', metaKey: true })
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
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'typed' } })
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

  // --- T4.2: @mention menu ---

  it('opens the mention menu on @ and filters as the query narrows', () => {
    render(<ControlledComposer onSend={vi.fn()} onStop={vi.fn()} running={false} assistantProfiles={PROFILES} />)
    const ta = screen.getByRole('combobox')
    fireEvent.change(ta, { target: { value: '@', selectionStart: 1 } })
    expect(screen.getByRole('listbox')).toBeInTheDocument()
    expect(screen.getAllByRole('option')).toHaveLength(2)

    fireEvent.change(ta, { target: { value: '@fro', selectionStart: 4 } })
    expect(screen.getAllByRole('option')).toHaveLength(1)
    expect(screen.getByText('@frontendDeveloper')).toBeInTheDocument()
  })

  it('Enter inside the mention list selects the profile and does not send', () => {
    const onSend = vi.fn()
    render(<ControlledComposer onSend={onSend} onStop={vi.fn()} running={false} assistantProfiles={PROFILES} />)
    const ta = screen.getByRole('combobox') as HTMLTextAreaElement
    fireEvent.change(ta, { target: { value: '@fro', selectionStart: 4 } })
    fireEvent.keyDown(ta, { key: 'Enter' })
    expect(onSend).not.toHaveBeenCalled()
    expect(ta.value).toBe('@frontendDeveloper ')
  })

  it('Cmd/Ctrl+Enter still sends once there is no open mention list', () => {
    const onSend = vi.fn()
    render(<ControlledComposer initialDraft="hello" onSend={onSend} onStop={vi.fn()} running={false} assistantProfiles={PROFILES} />)
    fireEvent.keyDown(screen.getByRole('combobox'), { key: 'Enter', metaKey: true })
    expect(onSend).toHaveBeenCalledTimes(1)
  })

  it('first Escape closes the mention list without reaching a parent handler, second Escape propagates', () => {
    const parentEscape = vi.fn()
    render(
      <div onKeyDown={(e) => { if (e.key === 'Escape') parentEscape() }}>
        <ControlledComposer onSend={vi.fn()} onStop={vi.fn()} running={false} assistantProfiles={PROFILES} />
      </div>,
    )
    const ta = screen.getByRole('combobox')
    fireEvent.change(ta, { target: { value: '@fro', selectionStart: 4 } })
    expect(screen.getByRole('listbox')).toBeInTheDocument()

    fireEvent.keyDown(ta, { key: 'Escape' })
    expect(parentEscape).not.toHaveBeenCalled()
    expect(screen.queryByRole('listbox')).toBeNull()

    fireEvent.keyDown(ta, { key: 'Escape' })
    expect(parentEscape).toHaveBeenCalledTimes(1)
  })

  it('does not open the mention menu for an email-like @ (no preceding whitespace)', () => {
    render(<ControlledComposer onSend={vi.fn()} onStop={vi.fn()} running={false} assistantProfiles={PROFILES} />)
    const ta = screen.getByRole('combobox')
    fireEvent.change(ta, { target: { value: 'contact user@example.com', selectionStart: 25 } })
    expect(screen.queryByRole('listbox')).toBeNull()
  })

  it('inserting a mention mid-text preserves surrounding text and places the caret after it', () => {
    render(<ControlledComposer onSend={vi.fn()} onStop={vi.fn()} running={false} assistantProfiles={PROFILES} />)
    const ta = screen.getByRole('combobox') as HTMLTextAreaElement
    fireEvent.change(ta, { target: { value: 'ask @fro please', selectionStart: 8 } })
    fireEvent.keyDown(ta, { key: 'Enter' })
    expect(ta.value).toBe('ask @frontendDeveloper  please')
  })

  it('supports multiple mentions in one message', () => {
    render(<ControlledComposer onSend={vi.fn()} onStop={vi.fn()} running={false} assistantProfiles={PROFILES} />)
    const ta = screen.getByRole('combobox') as HTMLTextAreaElement

    fireEvent.change(ta, { target: { value: '@fro', selectionStart: 4 } })
    fireEvent.keyDown(ta, { key: 'Enter' })
    expect(ta.value).toBe('@frontendDeveloper ')

    const withSecond = '@frontendDeveloper and @qa'
    fireEvent.change(ta, { target: { value: withSecond, selectionStart: withSecond.length } })
    fireEvent.keyDown(ta, { key: 'Enter' })
    expect(ta.value).toBe('@frontendDeveloper and @qaEngineer ')
  })

  it('composition (IME) keystrokes do not break typing or get mistaken for a mention selection', () => {
    render(<ControlledComposer onSend={vi.fn()} onStop={vi.fn()} running={false} assistantProfiles={PROFILES} />)
    const ta = screen.getByRole('combobox') as HTMLTextAreaElement
    fireEvent.change(ta, { target: { value: '@fro', selectionStart: 4 } })
    fireEvent.compositionStart(ta)
    // Enter fired while composing (e.g. confirming an IME candidate) must not be treated as "select mention".
    fireEvent.keyDown(ta, { key: 'Enter', isComposing: true })
    expect(ta.value).toBe('@fro')
    fireEvent.compositionEnd(ta)
    // A normal change after composition ends still works.
    fireEvent.change(ta, { target: { value: '@frog', selectionStart: 5 } })
    expect(ta.value).toBe('@frog')
  })

  it('hides the mention menu entirely when no assistant profiles are available (capability off)', () => {
    render(<ControlledComposer onSend={vi.fn()} onStop={vi.fn()} running={false} />)
    const ta = screen.getByRole('combobox')
    fireEvent.change(ta, { target: { value: '@fro', selectionStart: 4 } })
    expect(screen.queryByRole('listbox')).toBeNull()
  })
})
