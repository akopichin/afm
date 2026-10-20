// mesh-receipt — presentation of a sent mesh message's delivery state, per recipient (Agent Mesh Phase 7, T7.4).
//
// Real signals only: the recipient's delivery receipt (the mesh_receipt notices afm publishes when a message is
// handed to an agent's turn or inbox read, and when it is acknowledged — meshDeliveries), an answer in the thread,
// the original message's durable side failure, the roster's reachability and the recipient stage's pause.
// Accepted by the API is not delivered: until the receipt arrives the recipient shows as waiting for an inbox
// check, whatever kind of agent it is.

import type { MeshMessage, MeshParticipant } from '../../api/mesh-client'
import type { AfmEvent } from '../../types'

export type ReceiptPhase =
  | 'awaiting'
  | 'delivered'
  | 'waiting_inbox'
  | 'answered'
  | 'could_not_answer'
  | 'paused'
  | 'unavailable'
  | 'messaging_unavailable'

// RECEIPT_LABEL — the literal presentation strings dev-plan.md T7.4 specifies.
export const RECEIPT_LABEL: Record<ReceiptPhase, string> = {
  awaiting: 'Awaiting delivery',
  delivered: 'Delivered',
  waiting_inbox: 'Accepted · waiting for an inbox check',
  answered: 'Answered',
  could_not_answer: 'Could not answer',
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
    const p = e.payload as { message_id?: unknown; participant_id?: unknown; status?: unknown }
    if ((p.status === 'delivered' || p.status === 'acknowledged') &&
      typeof p.message_id === 'string' && typeof p.participant_id === 'string') out.add(deliveryKey(p.message_id, p.participant_id))
  }
  return out
}

// Recent side failures are keyed to the original message in event replay. The undeliverable receipt also covers
// a queued message that could not start; older failures are recovered from MeshMessage.side_agent_failed.
export function sideAgentFailures(events: readonly AfmEvent[]): ReadonlySet<string> {
  const ids = new Set<string>()
  for (const event of events) {
    if (event.type === 'mesh_receipt' && event.payload !== null && typeof event.payload === 'object') {
      const receipt = event.payload as { message_id?: unknown; participant_id?: unknown; status?: unknown }
      if (receipt.status === 'undeliverable' && typeof receipt.message_id === 'string' &&
        typeof receipt.participant_id === 'string' && receipt.participant_id.startsWith('side:')) ids.add(receipt.message_id)
    }
    if (event.type !== 'side_agent_mesh_failure' || event.payload === null || typeof event.payload !== 'object') continue
    const id = (event.payload as { source_message_id?: unknown }).source_message_id
    if (typeof id === 'string' && id !== '') ids.add(id)
  }
  return ids
}

// Only a response from the addressed side participant settles its receipt. Other agents' replies in a
// group conversation do not claim that side answered.
export function sideAgentOutcome(
  message: Pick<MeshMessage, 'id' | 'recipient_ids'> & Pick<MeshMessage, 'auto_result' | 'side_agent_failed'>,
  messages: readonly MeshMessage[],
  failures: ReadonlySet<string>,
  recipientId?: string,
): 'answered' | 'could_not_answer' | null {
  // A passive result gives side context but never asks it for an answer. Its
  // orphan receipt can be undeliverable after restart without being a failure.
  if (message.auto_result === true || messages.some((m) => m.id === message.id && m.auto_result === true)) return null
  const sideIds = message.recipient_ids.filter((id) => id.startsWith('side:') && (recipientId === undefined || id === recipientId))
  if (sideIds.length === 0) return null
  const linked = messages.find((reply) => reply.in_reply_to === message.id && sideIds.includes(reply.sender_id))
  if (linked?.kind === 'result' && linked.auto_result === true) return 'could_not_answer'
  if (linked !== undefined) return 'answered'
  // GET thread/stage messages carry the durable failure even after the event replay cap drops its notice.
  // The composer passes a send receipt with only the id, so look up the polled original message too.
  const originalFailed = message.side_agent_failed === true || messages.some((m) => m.id === message.id && m.side_agent_failed === true)
  return originalFailed || failures.has(message.id) ? 'could_not_answer' : null
}
