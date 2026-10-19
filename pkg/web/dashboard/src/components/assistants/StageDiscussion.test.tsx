import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { StageDiscussion, type DiscussionMessage } from './StageDiscussion'

function makeMessage(overrides: Partial<DiscussionMessage> = {}): DiscussionMessage {
  return {
    id: 'dm-1',
    authorKind: 'assistant',
    authorId: 'qaEngineer',
    role: 'reviewer',
    round: 1,
    evidence: [],
    body: 'looks fine',
    createdAt: '2026-10-15T00:00:00Z',
    ...overrides,
  }
}

describe('StageDiscussion', () => {
  test('empty state', () => {
    render(<StageDiscussion messages={[]} />)
    expect(screen.getByText(/no discussion yet/i)).toBeInTheDocument()
  })

  test('TestDiscussion_ShowsAuthorRoleAndEvidence', () => {
    render(
      <StageDiscussion
        messages={[
          makeMessage({
            authorKind: 'assistant',
            authorId: 'qaEngineer',
            role: 'reviewer',
            evidence: ['f-1', 'f-2'],
            body: 'the retry loop is unbounded',
          }),
        ]}
      />,
    )
    expect(screen.getByText('qaEngineer')).toBeInTheDocument()
    expect(screen.getByText('reviewer')).toBeInTheDocument()
    expect(screen.getByText('f-1')).toBeInTheDocument()
    expect(screen.getByText('f-2')).toBeInTheDocument()
  })

  test('an agent author is named from the roster, with its role in words — never a raw id or wire value', () => {
    render(
      <StageDiscussion
        messages={[makeMessage({ authorKind: 'main_agent', authorId: 'stage:harden', role: 'main_agent', body: 'returns 400' })]}
        participants={[{ id: 'stage:harden', kind: 'stage_agent', scope: 'stage', stage_id: 'harden', display_name: 'Hardening', reachable: true }]}
      />,
    )
    expect(screen.getByText('Hardening')).toBeInTheDocument()
    expect(screen.getByText('Stage agent')).toBeInTheDocument()
    expect(screen.queryByText(/stage:harden|main_agent/)).toBeNull()
  })

  test('a silent (non-responding) participant is never rendered as agreeing — only real messages render', () => {
    render(<StageDiscussion messages={[makeMessage({ authorId: 'qa' })]} />)
    // Only ONE message rendered — no synthetic entry for anyone else.
    expect(screen.getAllByRole('listitem')).toHaveLength(1)
  })

  test('TestDiscussion_MarkdownViaSafeRenderer', () => {
    const { container } = render(
      <StageDiscussion messages={[makeMessage({ body: '# Found an issue\n\nThe **retry loop** never backs off.' })]} />,
    )
    // Rendered via the shared markdown-it based renderer (real heading/bold tags), never raw-escaped text and
    // never a second ad-hoc dangerouslySetInnerHTML surface.
    expect(container.querySelector('.md h1')).toHaveTextContent('Found an issue')
    expect(container.querySelector('.md strong')).toHaveTextContent('retry loop')
    expect(container.querySelectorAll('.md')).toHaveLength(1)
  })

  test('human author renders distinctly from an assistant author', () => {
    render(<StageDiscussion messages={[makeMessage({ authorKind: 'human', authorId: '', role: '' })]} />)
    const item = screen.getByText('looks fine').closest('li')
    expect(item).toHaveAttribute('data-author-kind', 'human')
  })

  test('names the agents a post was also addressed to, and a mentioned agent by its role', () => {
    const participants = [{ id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA', reachable: true }]
    render(
      <StageDiscussion
        participants={participants}
        messages={[
          makeMessage({ id: 'q', authorKind: 'human', authorId: undefined, role: undefined, round: 0, body: 'ask', mentions: ['run:t-1', 'run:gone'] }),
          makeMessage({ id: 'a', authorKind: 'assistant', authorId: 'run:t-1', role: 'run_agent', round: 0, body: 'answer' }),
        ]}
      />,
    )
    expect(screen.getByText('→ QA, gone')).toBeInTheDocument()
    expect(screen.getByText('Run agent')).toBeInTheDocument()
  })

  test('a pasted JSON result shows as its summary with a verdict chip', () => {
    render(
      <StageDiscussion
        messages={[makeMessage({ body: '{"schema_version":1,"kind":"result","verdict":"pass","summary":"All checks green."}' })]}
      />,
    )
    expect(screen.getByText('All checks green.')).toBeInTheDocument()
    expect(screen.getByText('Pass')).toBeInTheDocument()
    // The raw JSON is only inside the collapsed "Raw" details, never the visible text.
    expect(screen.getByText(/schema_version/).closest('details')).not.toBeNull()
  })
})
