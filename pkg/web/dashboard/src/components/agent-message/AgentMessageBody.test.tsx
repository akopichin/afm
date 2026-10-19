import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { AgentMessageBody, TURN_RESULT_TOOLTIP } from './AgentMessageBody'

const RESULT = '{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"Two tests fail."}'

describe('AgentMessageBody', () => {
  test("an agent's pasted result object shows its summary and a verdict chip; the raw text stays reachable", () => {
    render(<AgentMessageBody body={RESULT} fromAgent />)
    expect(screen.getByText('Two tests fail.')).toBeInTheDocument()
    expect(screen.getByText('Needs changes')).toHaveAttribute('data-verdict', 'needs_changes')
    expect(screen.getByText('Raw')).toBeInTheDocument()
    expect(screen.getByText(RESULT)).toBeInTheDocument() // inside the collapsed "Raw" details
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
