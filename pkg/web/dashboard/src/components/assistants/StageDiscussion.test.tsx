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
    expect(screen.getByText('@qaEngineer')).toBeInTheDocument()
    expect(screen.getByText('reviewer')).toBeInTheDocument()
    expect(screen.getByText('f-1')).toBeInTheDocument()
    expect(screen.getByText('f-2')).toBeInTheDocument()
  })

  test('a silent (non-responding) participant is never rendered as agreeing — only real messages render', () => {
    render(<StageDiscussion messages={[makeMessage({ authorId: 'qa' })]} />)
    // Only ONE message rendered — no synthetic entry for anyone else.
    expect(screen.getAllByText(/@/)).toHaveLength(1)
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
})
