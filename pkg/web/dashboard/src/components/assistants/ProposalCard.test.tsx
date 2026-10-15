import { render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { ProposalCard, type ProposalCardProposal } from './ProposalCard'

function makeProposal(overrides: Partial<ProposalCardProposal> = {}): ProposalCardProposal {
  return {
    id: 'p-1',
    type: 'feedback',
    title: 'Bound the retry loop',
    rationale: 'The retry loop can spin forever under a persistent 5xx.',
    authors: ['qaEngineer'],
    status: 'pending',
    ...overrides,
  }
}

describe('ProposalCard', () => {
  test('TestProposalCard_NotStyledAsUserCommand', () => {
    const { container } = render(
      <ProposalCard proposal={makeProposal()} stale={false} inflight={false} onAccept={vi.fn()} onDismiss={vi.fn()} />,
    )
    const card = container.querySelector('.proposal-card')
    expect(card).not.toBeNull()
    // Never rendered with the user-message actor attribute a real human bubble would carry.
    expect(card).not.toHaveAttribute('data-actor', 'user')
    // A visible badge marks this as a proposal, not a command issued directly by the user.
    expect(screen.getByText(/proposal/i)).toBeInTheDocument()
  })

  test('TestProposalCard_StaleShowsRefreshNotApply', () => {
    render(<ProposalCard proposal={makeProposal()} stale={true} inflight={false} onAccept={vi.fn()} onDismiss={vi.fn()} />)
    expect(screen.getByRole('button', { name: /refresh/i })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /accept/i })).not.toBeInTheDocument()
  })

  test('TestProposalCard_AcceptDisabledWhileInflight', () => {
    render(<ProposalCard proposal={makeProposal()} stale={false} inflight={true} onAccept={vi.fn()} onDismiss={vi.fn()} />)
    const accept = screen.getByRole('button', { name: /accept|applying/i })
    expect(accept).toBeDisabled()
  })

  test('accept is enabled and clickable when not stale/inflight', () => {
    const onAccept = vi.fn()
    render(<ProposalCard proposal={makeProposal()} stale={false} inflight={false} onAccept={onAccept} onDismiss={vi.fn()} />)
    const accept = screen.getByRole('button', { name: /^accept$/i })
    expect(accept).not.toBeDisabled()
    accept.click()
    expect(onAccept).toHaveBeenCalledTimes(1)
  })

  test('rationale renders through the shared safe markdown renderer', () => {
    const { container } = render(
      <ProposalCard
        proposal={makeProposal({ rationale: 'See **evidence** below.' })}
        stale={false}
        inflight={false}
        onAccept={vi.fn()}
        onDismiss={vi.fn()}
      />,
    )
    expect(container.querySelector('.md strong')).toHaveTextContent('evidence')
  })
})
