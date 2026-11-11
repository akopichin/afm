import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { AgentMessageBody, TURN_RESULT_TOOLTIP } from './AgentMessageBody'

const RESULT = '{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"Two tests fail."}'

describe('AgentMessageBody', () => {
  test("an agent's pasted result object shows its summary and a verdict chip; the raw text stays reachable", async () => {
    render(<AgentMessageBody body={RESULT} fromAgent />)
    expect(screen.getByText('Two tests fail.')).toBeInTheDocument()
    expect(screen.getByText('Needs changes')).toHaveAttribute('data-verdict', 'needs_changes')
    expect(screen.getByText('Raw')).toBeInTheDocument()
    // The raw text is mounted only while "Raw" is open.
    expect(screen.queryByText(RESULT)).toBeNull()
    fireEvent.click(screen.getByText('Raw'))
    expect(await screen.findByText(RESULT)).toBeInTheDocument()
  })

  test('a Raw section kept by the caller shows its open state and reports the reader\'s toggle', () => {
    const onToggle = vi.fn()
    const { rerender } = render(<AgentMessageBody body={RESULT} fromAgent raw={{ open: true, onToggle }} />)
    expect(screen.getByText(RESULT)).toBeInTheDocument()
    const details = screen.getByText('Raw').closest('details') as HTMLDetailsElement
    details.open = false
    fireEvent(details, new Event('toggle'))
    expect(onToggle).toHaveBeenCalledWith(false)
    rerender(<AgentMessageBody body={RESULT} fromAgent raw={{ open: false, onToggle }} />)
    expect(screen.queryByText(RESULT)).toBeNull()
  })

  test('a result without a verdict shows its summary and no verdict chip', () => {
    const body = '{"schema_version":1,"kind":"result","summary":"Collected the logs."}'
    const { container } = render(<AgentMessageBody body={body} fromAgent />)
    expect(screen.getByText('Collected the logs.')).toBeInTheDocument()
    expect(container.querySelector('.agent-result-verdict')).toBeNull()
    expect(screen.getByText('Raw')).toBeInTheDocument()
  })

  test('the same JSON from the human is shown as written', () => {
    render(<AgentMessageBody body={RESULT} fromAgent={false} />)
    expect(screen.getByText(RESULT)).toBeInTheDocument()
    expect(screen.queryByText('Needs changes')).toBeNull()
  })

  test('a turn result afm posted carries the "turn result" tag with its explanation', () => {
    render(<AgentMessageBody body="checked the flow" fromAgent autoResult />)
    expect(screen.getByText('turn result')).toHaveAttribute('title', TURN_RESULT_TOOLTIP)
    expect(screen.getByText('checked the flow')).toBeInTheDocument()
  })

  test('an ordinary message has no "turn result" tag', () => {
    render(<AgentMessageBody body="working on it" fromAgent />)
    expect(screen.queryByText('turn result')).toBeNull()
  })

  test('plain text is shown as is', () => {
    render(<AgentMessageBody body="working on it" fromAgent />)
    expect(screen.getByText('working on it')).toBeInTheDocument()
  })
})
