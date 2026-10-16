import { useEffect, useRef, useState, type ReactElement } from 'react'
import { acceptProposal, dismissProposal, listStageProposals, type ProposalWire } from '../../api/run-client'
import { ProposalCard } from './ProposalCard'

const POLL_INTERVAL_MS = 3000

export type StageProposalsProps = {
  stageId: string
}

// StageProposals is the mounted, stateful wrapper around ProposalCard: it polls a stage's pending proposals
// (GET /api/stages/{id}/proposals, its own poll) and lets the operator accept or dismiss each one. Accept is
// the single explicit confirmation step before a proposal reaches the main agent; staleness is enforced by
// the server's CAS on accept (a stale accept fails), so the card never needs the live stage generation here.
export function StageProposals({ stageId }: StageProposalsProps): ReactElement | null {
  const [proposals, setProposals] = useState<ProposalWire[]>([])
  const [inflight, setInflight] = useState<Record<string, boolean>>({})
  const [error, setError] = useState<string | null>(null)
  const seq = useRef(0)

  useEffect(() => {
    const mySeq = ++seq.current
    let cancelled = false
    const load = async (): Promise<void> => {
      try {
        const res = await listStageProposals(stageId)
        if (cancelled || seq.current !== mySeq) return
        setProposals(res.proposals)
      } catch {
        /* transient — keep what we have */
      }
    }
    void load()
    const timer = setInterval(() => void load(), POLL_INTERVAL_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [stageId])

  // Only pending proposals are actionable; accepted/dismissed ones drop out of the list.
  const pending = proposals.filter((p) => p.status === 'pending')
  if (pending.length === 0) return null

  function act(id: string, fn: () => Promise<void>): void {
    setInflight((m) => ({ ...m, [id]: true }))
    setError(null)
    fn()
      .then(() => setProposals((prev) => prev.filter((p) => p.id !== id)))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : 'request failed'))
      .finally(() => setInflight((m) => ({ ...m, [id]: false })))
  }

  return (
    <div className="stage-proposals">
      {pending.map((p) => (
        <ProposalCard
          key={p.id}
          proposal={{ id: p.id, type: p.type, title: p.title, rationale: p.rationale, authors: p.authors, status: p.status }}
          stale={false}
          inflight={inflight[p.id] === true}
          onAccept={() => act(p.id, () => acceptProposal(p.id, `accept-${p.id}`))}
          onDismiss={() => act(p.id, () => dismissProposal(p.id, `dismiss-${p.id}`))}
        />
      ))}
      {error !== null && <div className="stage-proposals-error" role="alert">{error}</div>}
    </div>
  )
}
