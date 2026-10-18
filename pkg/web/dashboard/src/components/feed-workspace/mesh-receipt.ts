// mesh-receipt — presentation mapping for a sent mesh message's delivery state (Agent Mesh Phase 7, T7.4).
//
// The backend's REAL per-recipient agentcomm.DeliveryStatus (accepted/delivered/acknowledged/answered/
// undeliverable, pkg/agentcomm/types.go) is only readable from the agent-facing /api/agent-mesh/* surface
// (agentcomm_messages.go's rs.Assistants.Mesh.Receipts) — the dashboard-facing /api/mesh/* adapter
// (pkg/server/mesh.go's MeshActions) exposes no equivalent read today, and adding one is a backend change out
// of this (frontend-only) task's scope. computeReceiptPhase below is therefore a CLIENT-SIDE APPROXIMATION
// built from data the dashboard already has: the roster's Participant.reachable/unreachable_reason, whether
// the recipient's own stage currently shows 'paused', and whether a reply addressed to our sent message has
// shown up in the thread. A follow-up that adds a receipts read to /api/mesh/* could replace this outright.

import type { MeshParticipant } from '../../api/mesh-client'

export type ReceiptPhase =
  | 'awaiting'
  | 'delivered'
  | 'waiting_inbox'
  | 'answered'
  | 'paused'
  | 'unavailable'
  | 'messaging_unavailable'

// RECEIPT_LABEL — the literal presentation strings dev-plan.md T7.4 specifies.
export const RECEIPT_LABEL: Record<ReceiptPhase, string> = {
  awaiting: 'Awaiting delivery',
  delivered: 'Delivered',
  waiting_inbox: 'Waiting for an inbox check',
  answered: 'Answered',
  paused: 'Paused · waiting for Continue',
  unavailable: 'Unavailable',
  messaging_unavailable: 'Messaging unavailable',
}

export type ReceiptInput = {
  // participant — the recipient's CURRENT roster entry, or undefined when it has not shown up in a poll yet
  // (e.g. the instant after attach-and-send creates a brand-new helper). Undefined is treated as "no bad news
  // yet", not as unreachable — see the waiting_inbox fallback below.
  participant: MeshParticipant | undefined
  // stagePaused — true when the recipient's own stage (participant.stage_id) currently shows status
  // 'paused'. Only ever meaningful for a stage_agent/helper participant.
  stagePaused: boolean
  // answered — some message in the thread already carries in_reply_to === the message we're tracking.
  answered: boolean
  // inFlight — the Send request that produced this receipt has not resolved yet (optimistic "just clicked
  // Send" state, shown before we even know the new message's id).
  inFlight: boolean
}

// computeReceiptPhase picks ONE presentation phase, most-specific first: an actual answer trumps everything,
// an in-flight send is reported before any participant lookup (we simply don't know anything yet), a KNOWN
// unreachable participant is reported before a delivery guess, and only once none of those apply do we fall
// back to "is this the kind of participant that only reads mail between turns".
export function computeReceiptPhase(input: ReceiptInput): ReceiptPhase {
  if (input.answered) return 'answered'
  if (input.inFlight) return 'awaiting'

  const p = input.participant
  if (p !== undefined && !p.reachable) {
    return p.unreachable_reason?.toLowerCase().includes('mesh') === true ? 'messaging_unavailable' : 'unavailable'
  }
  if (input.stagePaused) return 'paused'
  if (p === undefined || p.kind === 'helper' || p.kind === 'run_agent') return 'waiting_inbox'
  return 'delivered'
}
