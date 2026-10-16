import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { StageProposals } from './StageProposals'
import * as runClient from '../../api/run-client'
import type { ProposalWire } from '../../api/run-client'

function proposal(overrides: Partial<ProposalWire> = {}): ProposalWire {
  return {
    id: 'p1', stage_id: 'implement', type: 'feedback', title: 'Add a nil-check',
    rationale: 'guard the response path', authors: ['reviewer'], expected_stage_generation: 1,
    status: 'pending', created_at: '2026-10-16T10:00:00Z', ...overrides,
  }
}

describe('StageProposals', () => {
  beforeEach(() => {
    vi.spyOn(runClient, 'listStageProposals').mockResolvedValue({ proposals: [proposal()] })
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('renders pending proposals and accepts one', async () => {
    const accept = vi.spyOn(runClient, 'acceptProposal').mockResolvedValue(undefined)
    render(<StageProposals stageId="implement" />)
    await waitFor(() => expect(screen.getByText('Add a nil-check')).toBeInTheDocument())
    // Labelled explicitly as a proposal, never a user command.
    expect(screen.getByText(/not a user command/i)).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Accept' }))
    await waitFor(() => expect(accept).toHaveBeenCalledWith('p1', expect.any(String)))
    // After accepting, the card drops out of the list.
    await waitFor(() => expect(screen.queryByText('Add a nil-check')).toBeNull())
  })

  test('renders nothing when there are no pending proposals', async () => {
    vi.spyOn(runClient, 'listStageProposals').mockResolvedValue({ proposals: [proposal({ status: 'accepted' })] })
    const { container } = render(<StageProposals stageId="implement" />)
    await waitFor(() => expect(runClient.listStageProposals).toHaveBeenCalled())
    expect(container.querySelector('.stage-proposals')).toBeNull()
  })
})
