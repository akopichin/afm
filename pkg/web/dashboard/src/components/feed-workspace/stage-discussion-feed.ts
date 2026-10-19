// stage-discussion-feed — the stage's conversation log with its agent (GET /api/stages/{id}/discussion) folded
// into the conversation messages its feed shows. A post written since the mesh existed is mirrored into the mesh
// under the SAME id, so it is already there and shows once; a post that exists only in the log (older journals,
// a helper's result, afm's own bookkeeping) becomes a read-only bubble of its own, in journal order. The log also
// carries the server's "Not answered" marks.

import type { MeshMessage } from '../../api/mesh-client'
import type { DiscussionMessage } from '../../hooks/use-stage-discussion'

// LegacyPostMeta — what a log-only post shows beside its text: the author's role and the evidence it cited.
export type LegacyPostMeta = { role?: string; evidence?: string[] }

export type StageFeedConversation = {
  // messages — the conversation messages and the log-only posts, in journal order, each once.
  messages: MeshMessage[]
  // unanswered — ids of the human posts the stage finished without answering.
  unanswered: ReadonlySet<string>
  // legacy — the log-only posts' role and evidence, by message id.
  legacy: ReadonlyMap<string, LegacyPostMeta>
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

// fromLog — a log-only post as a conversation message. Its thread is no mesh thread (so it offers no Reply). A
// human post went to the stage agent and the agents it named; an answer to a human post went to the human (so
// "Author's work", which keeps what is addressed to you, still shows it); anything else had no addressee.
function fromLog(m: DiscussionMessage, stageId: string, humanPosts: ReadonlySet<string>): MeshMessage {
  const parent = m.parentMessageId !== undefined && m.parentMessageId !== '' ? m.parentMessageId : undefined
  const recipients = m.authorKind === 'human' ? [`stage:${stageId}`, ...(m.mentions ?? [])] : parent !== undefined && humanPosts.has(parent) ? ['user'] : []
  return {
    id: m.id,
    thread_id: `discussion:${stageId}`,
    sender_id: logSender(m, stageId),
    recipient_ids: recipients,
    kind: 'message',
    in_reply_to: parent,
    body: m.body,
    auto_result: m.autoResult === true ? true : undefined,
    seq: m.seq ?? 0,
    created_at: m.createdAt,
  }
}

// before — a goes before b: by journal seq when both carry one (the run's durable order), else by time.
function before(a: MeshMessage, b: MeshMessage): boolean {
  if (a.seq > 0 && b.seq > 0) return a.seq < b.seq
  return Date.parse(a.created_at) < Date.parse(b.created_at)
}

// mergeInOrder — a stable merge of two lists each already in journal order; on a tie the mesh one first.
function mergeInOrder(meshMessages: MeshMessage[], logOnly: MeshMessage[]): MeshMessage[] {
  const out: MeshMessage[] = []
  let j = 0
  for (const m of meshMessages) {
    while (j < logOnly.length && before(logOnly[j] as MeshMessage, m)) out.push(logOnly[j++] as MeshMessage)
    out.push(m)
  }
  return out.concat(logOnly.slice(j))
}

export function withStageDiscussion(messages: MeshMessage[], log: DiscussionMessage[], stageId: string): StageFeedConversation {
  if (log.length === 0) return { messages, unanswered: NO_IDS, legacy: NO_META }
  const inMesh = new Set(messages.map((m) => m.id))
  const humanPosts = new Set([...log.filter((m) => m.authorKind === 'human').map((m) => m.id), ...messages.filter((m) => m.sender_id === 'user').map((m) => m.id)])
  const legacyPosts = log.filter((m) => !inMesh.has(m.id))
  const logOnly = legacyPosts.map((m) => fromLog(m, stageId, humanPosts))
  const merged = logOnly.length === 0 ? messages : mergeInOrder(messages, logOnly)
  // A reply from anyone but you answers a post, wherever it arrived.
  const answered = new Set(merged.filter((m) => m.sender_id !== 'user' && m.in_reply_to !== undefined).map((m) => m.in_reply_to))
  const unanswered = new Set(log.filter((m) => m.unanswered === true && !answered.has(m.id)).map((m) => m.id))
  const legacy = new Map<string, LegacyPostMeta>()
  for (const m of legacyPosts) {
    const evidence = m.evidence !== undefined && m.evidence.length > 0 ? m.evidence : undefined
    const role = m.role !== undefined && m.role !== '' ? m.role : undefined
    if (role !== undefined || evidence !== undefined) legacy.set(m.id, { role, evidence })
  }
  return { messages: merged, unanswered, legacy }
}

const NO_IDS: ReadonlySet<string> = new Set()
const NO_META: ReadonlyMap<string, LegacyPostMeta> = new Map()
