import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { ActionCard } from './ActionCard'

function renderCard(overrides: Partial<Parameters<typeof ActionCard>[0]> = {}): void {
  render(
    <ActionCard
      roleTitle="QA Engineer"
      stageTitle="Frontend"
      title="Look at this"
      instruction="Please review the mobile scenario"
      maxDepth={3}
      remainingDepth={2}
      atDepthLimit={false}
      {...overrides}
    />,
  )
}

// TestCard_RendersAllPolicyFields — dev-plan.md T4.6 §12.4: role, task, immediate parent, advisory/required,
// trigger, effective_max_depth/remaining_depth, and an explicit note that delegation inside this task's work
// won't need a separate click per child.
describe('ActionCard', () => {
  test('TestCard_RendersAllPolicyFields', () => {
    renderCard()
    expect(screen.getByText('QA Engineer')).toBeInTheDocument()
    expect(screen.getByText('Frontend')).toBeInTheDocument()
    expect(screen.getByText('Look at this')).toBeInTheDocument()
    expect(screen.getByText('Please review the mobile scenario')).toBeInTheDocument()
    expect(screen.getByText(/advisory/i)).toBeInTheDocument()
    expect(screen.getByText(/one[- ]shot/i)).toBeInTheDocument()
    expect(screen.getByText(/3/)).toBeInTheDocument()
    expect(screen.getByText(/2/)).toBeInTheDocument()
    expect(screen.getByText(/won.t need a separate click/i)).toBeInTheDocument()
  })

  // TestCard_DisabledAtDepthLimit — at the last allowed level, the card must say so explicitly, not just
  // silently omit a control (the dialog that wraps this card disables its own confirm button on this flag).
  test('TestCard_DisabledAtDepthLimit', () => {
    renderCard({ maxDepth: 0, remainingDepth: -1, atDepthLimit: true })
    expect(screen.getByRole('alert')).toHaveTextContent(/delegation depth limit/i)
    // No "won't need a click per child" reassurance when there IS no room left for children.
    expect(screen.queryByText(/won.t need a separate click/i)).not.toBeInTheDocument()
  })
})
