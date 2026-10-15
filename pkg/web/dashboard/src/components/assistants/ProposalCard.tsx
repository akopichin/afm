import type { ReactElement } from 'react'
import { MarkdownRenderer } from '../plan-panel'

// ProposalCardProposal mirrors the server's ProposalView (pkg/server/proposals.go) one field at a time — see
// GET /api/stages/{id}/proposals.
export type ProposalCardProposal = {
  id: string
  type: string
  title: string
  rationale?: string
  authors?: string[]
  status: string
}

type ProposalCardProps = {
  proposal: ProposalCardProposal
  // stale — the proposal's expected stage generation no longer matches the stage's current one
  // (assistants.Proposal.CheckGeneration). A stale proposal can never be applied as-is; the only legal action
  // is to refresh/re-synthesize it, never a direct Accept (TestProposalCard_StaleShowsRefreshNotApply).
  stale: boolean
  // inflight — an accept request for THIS proposal is already in flight (e.g. the user just clicked, or a
  // duplicate click arrived before the first response). Accept is disabled, never double-submittable
  // (TestProposalCard_AcceptDisabledWhileInflight).
  inflight: boolean
  onAccept: () => void
  onDismiss: () => void
}

// ProposalCard renders one synthesized Proposal for human review. It is DELIBERATELY never styled or
// data-tagged as a user message/command — accepting it is the ONE explicit confirmation step before anything
// reaches the main agent (TestProposalCard_NotStyledAsUserCommand); until accepted, nothing here has touched
// the stage in any way.
export function ProposalCard({ proposal, stale, inflight, onAccept, onDismiss }: ProposalCardProps): ReactElement {
  return (
    <div className="proposal-card" data-actor="assistant" data-status={proposal.status} data-type={proposal.type}>
      <div className="proposal-card-badge">Proposal — not a user command</div>
      <div className="proposal-card-title">{proposal.title}</div>
      {proposal.rationale && (
        <div className="proposal-card-rationale">
          <MarkdownRenderer source={proposal.rationale} />
        </div>
      )}
      {proposal.authors && proposal.authors.length > 0 && (
        <div className="proposal-card-authors">{proposal.authors.map((a) => `@${a}`).join(', ')}</div>
      )}
      <div className="proposal-card-actions">
        {stale ? (
          <button type="button" className="proposal-card-refresh" onClick={onDismiss}>
            Refresh
          </button>
        ) : (
          <>
            <button type="button" className="proposal-card-accept" disabled={inflight} onClick={onAccept}>
              {inflight ? 'Applying…' : 'Accept'}
            </button>
            <button type="button" className="proposal-card-dismiss" disabled={inflight} onClick={onDismiss}>
              Dismiss
            </button>
          </>
        )}
      </div>
    </div>
  )
}
