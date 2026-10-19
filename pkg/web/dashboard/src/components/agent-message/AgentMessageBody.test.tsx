import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { AgentMessageBody } from './AgentMessageBody'

describe('AgentMessageBody', () => {
  test('a pasted result object shows its summary and a verdict chip, not raw JSON', () => {
    const { container } = render(
      <AgentMessageBody body={'{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"Two tests fail."}'} />,
    )
    expect(screen.getByText('Two tests fail.')).toBeInTheDocument()
    expect(screen.getByText('Needs changes')).toHaveAttribute('data-verdict', 'needs_changes')
    expect(container.textContent).not.toContain('schema_version')
  })

  test('plain text is shown as is', () => {
    render(<AgentMessageBody body="working on it" />)
    expect(screen.getByText('working on it')).toBeInTheDocument()
  })
})
