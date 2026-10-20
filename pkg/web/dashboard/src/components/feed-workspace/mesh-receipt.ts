// mesh-receipt — presentation of a sent mesh message's delivery state, per recipient (Agent Mesh Phase 7, T7.4).
//
// Real signals only: the recipient's delivery receipt (the mesh_receipt notices afm publishes when a message is
// handed to an agent's turn or inbox read, and when it is acknowledged — meshDeliveries), an answer in the thread,
// the roster's reachability and the recipient stage's pause. Accepted by the API is not delivered: until the
// receipt arrives the recipient shows as waiting for an inbox check, whatever kind of agent it is.

import type { MeshParticipant } from '../../api/mesh-client'
import type { AfmEvent } from '../../types'

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
  waiting_inbox: 'Accepted · waiting for an inbox check',
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
  // delivered — the recipient's own delivery (or acknowledgement) receipt for this message has arrived.
  delivered: boolean
}

// computeReceiptPhase picks ONE presentation phase, most-specific first: an actual answer trumps everything, an
// in-flight send is reported before any participant lookup, a KNOWN unreachable participant before anything about
// delivery, a paused stage next, then the real delivery receipt; without it the message is only accepted.
export function computeReceiptPhase(input: ReceiptInput): ReceiptPhase {
  if (input.answered) return 'answered'
  if (input.inFlight) return 'awaiting'

  const p = input.participant
  if (p !== undefined && !p.reachable) {
    return p.unreachable_reason?.toLowerCase().includes('mesh') === true ? 'messaging_unavailable' : 'unavailable'
  }
  if (input.stagePaused) return 'paused'
  return input.delivered ? 'delivered' : 'waiting_inbox'
}

// deliveryKey — meshDeliveries' key for one message and recipient.
export function deliveryKey(messageId: string, participantId: string): string {
  return `${messageId}|${participantId}`
}

// meshDeliveries — the (message, recipient) pairs the run's mesh_receipt events say were delivered or
// acknowledged (either means the recipient got it).
export function meshDeliveries(events: readonly AfmEvent[]): ReadonlySet<string> {
  const out = new Set<string>()
  for (const e of events) {
    if (e.type !== 'mesh_receipt' || e.payload === null || typeof e.payload !== 'object') continue
    const p = e.payload as { message_id?: unknown; participant_id?: unknown }
    if (typeof p.message_id === 'string' && typeof p.participant_id === 'string') out.add(deliveryKey(p.message_id, p.participant_id))
  }
  return out
}
