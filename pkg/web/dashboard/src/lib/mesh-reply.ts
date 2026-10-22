import type { MeshMessage, MeshParticipant, MeshThread } from '../api/mesh-client'

// isAgentMessageToUser — an agent wrote to the human on purpose: not the human's own message, not agent-to-agent
// chatter, not a passive turn result afm posted (auto_result wakes nobody). Mirrors the backend's cursor for
// `last_agent_to_user_message_seq`, so a bubble and the thread's unread signal agree.
export function isAgentMessageToUser(m: MeshMessage): boolean {
  return m.sender_id !== 'user' && m.recipient_ids.includes('user') && m.auto_result !== true
}

// canReplyInConversation — the human may answer a message in its thread: someone else wrote it, the thread is
// still open and the human is a member of it (older threads agents opened among themselves are read-only), and
// the author can still read it.
export function canReplyInConversation(m: MeshMessage, thread: MeshThread | undefined, participants: MeshParticipant[]): boolean {
  if (m.sender_id === 'user') return false
  if (thread === undefined || thread.status === 'closed' || !thread.members.includes('user')) return false
  return participants.find((p) => p.id === m.sender_id)?.reachable === true
}

// isAnsweredByRecipient — the backend's rule for a `reply_to` wait (MessageAnsweredByRecipient): a reply counts
// only when one of the request's recipients wrote it. Another thread member chiming in does not resume the wait.
function isAnsweredByRecipient(request: MeshMessage, messages: MeshMessage[]): boolean {
  return messages.some((m) => m.in_reply_to === request.id && request.recipient_ids.includes(m.sender_id))
}

// latestUnansweredRequest — the newest question an agent put to the human that nobody has answered yet and the
// human can still answer: the target a typed reply is linked to, so the agent's `reply_to` wait resumes.
// justAnswered — requests the human replied to a moment ago that the message list does not show yet.
export function latestUnansweredRequest(messages: MeshMessage[], thread: MeshThread | undefined, participants: MeshParticipant[], justAnswered: ReadonlySet<string> = new Set()): MeshMessage | undefined {
  return messages.filter((m) => m.kind === 'request' && isAgentMessageToUser(m) && !justAnswered.has(m.id) && !isAnsweredByRecipient(m, messages) && canReplyInConversation(m, thread, participants)).at(-1)
}
