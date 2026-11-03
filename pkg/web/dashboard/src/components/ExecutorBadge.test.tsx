import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { render, screen } from '@testing-library/react'
import { ExecutorBadge } from './ExecutorBadge'
import { resetUnknownCommandLabels, UnknownCommandNameContext } from '../lib/executor-label'

beforeEach(() => resetUnknownCommandLabels())
afterEach(() => document.documentElement.style.removeProperty('--afm-raw-command-tooltip'))

describe('ExecutorBadge', () => {
  test('shows the server name; the tooltip is the full name and the raw command', () => {
    render(<ExecutorBadge command="codex-as-claude" commandName="Codex Reviewer" />)
    expect(screen.getByText('Codex Reviewer')).toHaveAttribute('title', 'Codex Reviewer\nCommand: codex-as-claude')
  })

  test('keeps the long name whole in the tooltip while CSS clips the text', () => {
    const long = 'A very long configured executor name that the chip has to clip'
    render(<ExecutorBadge command="./scripts/long-agent.sh" commandName={long} className="team-person-chip" />)
    const badge = screen.getByText(long)
    expect(badge).toHaveClass('team-person-chip')
    expect(badge.getAttribute('title')).toBe(`${long}\nCommand: ./scripts/long-agent.sh`)
  })

  test('renders a name as plain text, not markup', () => {
    const { container } = render(<ExecutorBadge command="./h.sh" commandName="<b>Tag</b> Agent" />)
    expect(screen.getByText('<b>Tag</b> Agent')).toBeInTheDocument()
    expect(container.querySelector('b')).toBeNull()
  })

  test('without command_name falls back to the numbered prefix label from the status', () => {
    render(
      <UnknownCommandNameContext.Provider value="test">
        <ExecutorBadge command="./x/agent.sh" />
        <ExecutorBadge command="./y/agent.sh" />
        <ExecutorBadge command="./x/agent.sh" />
      </UnknownCommandNameContext.Provider>,
    )
    expect(screen.getAllByText('test-1')).toHaveLength(2)
    expect(screen.getByText('test-2')).toHaveAttribute('title', 'test-2\nCommand: ./y/agent.sh')
  })

  test('the skin variable --afm-raw-command-tooltip: none leaves only the full name in the tooltip', () => {
    document.documentElement.style.setProperty('--afm-raw-command-tooltip', 'none')
    render(<ExecutorBadge command="codex-as-claude" commandName="Codex Reviewer" />)
    expect(screen.getByText('Codex Reviewer')).toHaveAttribute('title', 'Codex Reviewer')
  })

  test('renders nothing without a command', () => {
    const { container } = render(<ExecutorBadge command="" />)
    expect(container).toBeEmptyDOMElement()
  })
})
