// stage-discussion-feed — the stage's conversation log with its agent (GET /api/stages/{id}/discussion) folded
// into the conversation messages its feed shows. A post written since the mesh existed is mirrored into the mesh
// under the SAME id, so it is already there and shows once; a post that exists only in the log (older journals,
// a helper's result, afm's own bookkeeping) becomes a read-only bubble of its own, in time order. The log also
// carries the server's "Not answered" marks.

import type { MeshMessage } from '../../api/mesh-client'
import type { DiscussionMessage } from '../../hooks/use-stage-discussion'

export type StageFeedConversation = {
  // messages — the conversation messages and the log-only posts, in time order, each once.
  messages: MeshMessage[]
  // unanswered — ids of the human posts the stage finished without answering.
  unanswered: ReadonlySet<string>
}

// logSender — the participant a log-only post is shown from.
function logSender(m: DiscussionMessage, stageId: string): string {
  if (m.authorId !== undefined && m.authorId !== '') return m.authorId
  switch (m.authorKind) {
    case 'human':
      return 'user'
    case 'main_agent':
      return `stage:${stageId}`
    default:
      return m.authorKind
  }
}

// fromLog — a log-only post as a conversation message. Its thread is no mesh thread (so it offers no Reply);
// the evidence a helper cited is kept under the text.
function fromLog(m: DiscussionMessage, stageId: string): MeshMessage {
  const human = m.authorKind === 'human'
  const evidence = m.evidence !== undefined && m.evidence.length > 0 ? `\n\nEvidence: ${m.evidence.join(', ')}` : ''
  return {
    id: m.id,
    thread_id: `discussion:${stageId}`,
    sender_id: logSender(m, stageId),
    recipient_ids: human ? [`stage:${stageId}`, ...(m.mentions ?? [])] : [],
    kind: 'message',
    in_reply_to: m.parentMessageId !== undefined && m.parentMessageId !== '' ? m.parentMessageId : undefined,
    body: m.body + evidence,
    auto_result: m.autoResult === true ? true : undefined,
    seq: 0,
    created_at: m.createdAt,
  }
}

// mergeByTime — a stable merge of two lists each already in journal order; at the same time the mesh one first.
function mergeByTime(a: MeshMessage[], b: MeshMessage[]): MeshMessage[] {
  const out: MeshMessage[] = []
  let j = 0
  for (const m of a) {
    const t = Date.parse(m.created_at)
    while (j < b.length && Date.parse((b[j] as MeshMessage).created_at) < t) out.push(b[j++] as MeshMessage)
    out.push(m)
  }
  return out.concat(b.slice(j))
}

export function withStageDiscussion(messages: MeshMessage[], log: DiscussionMessage[], stageId: string): StageFeedConversation {
  if (log.length === 0) return { messages, unanswered: NONE }
  const inMesh = new Set(messages.map((m) => m.id))
  const logOnly = log.filter((m) => !inMesh.has(m.id)).map((m) => fromLog(m, stageId))
  const merged = logOnly.length === 0 ? messages : mergeByTime(messages, logOnly)
  // A reply from anyone but you answers a post, wherever it arrived.
  const answered = new Set(merged.filter((m) => m.sender_id !== 'user' && m.in_reply_to !== undefined).map((m) => m.in_reply_to))
  const unanswered = new Set(log.filter((m) => m.unanswered === true && !answered.has(m.id)).map((m) => m.id))
  return { messages: merged, unanswered }
}

const NONE: ReadonlySet<string> = new Set()
