import { useEffect, useState } from 'react'
import { listStageDiscussion, type DiscussionMessageWire } from '../../api/run-client'
import { readPagesFrom, startSerialPoll } from '../../lib/serial-poll'

const POLL_INTERVAL_MS = 3000

// DiscussionMessage mirrors the server's DiscussionMessageView (pkg/server/discussion.go) — one entry of a stage's
// conversation with its agent, GET /api/stages/{id}/discussion.
export type DiscussionMessage = {
  id: string
  authorKind: string // 'human' | 'main_agent' | 'assistant' | 'afm'
  authorId?: string
  role?: string
  round: number
  parentMessageId?: string
  evidence?: string[]
  body: string
  // mentions — mesh participant ids the human also addressed.
  mentions?: string[]
  // unanswered — a human post of a finished stage that nobody replied to.
  unanswered?: boolean
  // autoResult — an agent's turn result afm posted as its reply.
  autoResult?: boolean
  createdAt: string
  // seq — the journal seq of the post, the order shared with mesh messages; undefined when the server has none.
  seq?: number
}

function toMessage(w: DiscussionMessageWire): DiscussionMessage {
  return {
    id: w.id,
    authorKind: w.author_kind,
    authorId: w.author_id,
    role: w.role,
    round: w.round,
    parentMessageId: w.parent_message_id,
    evidence: w.evidence,
    body: w.body ?? '',
    mentions: w.mentions,
    unanswered: w.unanswered,
    autoResult: w.auto_result,
    createdAt: w.created_at,
    seq: w.seq,
  }
}

// withAnswersApplied — rows already read keep the `unanswered` flag they were fetched with; once a reply to one of
// them arrives on a later page, it is answered (the server's own rule: any reply not from the human).
export function withAnswersApplied(messages: DiscussionMessage[]): DiscussionMessage[] {
  const answered = new Set(messages.filter((m) => m.authorKind !== 'human' && m.parentMessageId).map((m) => m.parentMessageId))
  return messages.map((m) => (m.unanswered === true && answered.has(m.id) ? { ...m, unanswered: false } : m))
}

type Loaded = { stageId: string | null; messages: DiscussionMessage[] }
const EMPTY: Loaded = { stageId: null, messages: [] }

// useStageDiscussion polls stage stageId's conversation with its agent (its own serial poll, forward from the last
// cursor, so a page is never appended twice). finished — the stage is done or failed: the log is then read again
// from the start, because a post that was merely not answered YET is now marked as never answered. null — no
// stage (or collaboration off): no requests.
export function useStageDiscussion(stageId: string | null, finished: boolean): DiscussionMessage[] {
  const [loaded, setLoaded] = useState<Loaded>(EMPTY)

  useEffect(() => {
    if (stageId === null) return
    let cursor = 0
    let fresh = true // the first read replaces what an earlier read (another stage, unfinished) left
    const poll = startSerialPoll(async (isStopped) => {
      // A failed page rejects the read: what we have stays, and the next tick reads from the same cursor.
      const read = await readPagesFrom(cursor, async (c) => {
        const page = await listStageDiscussion(stageId, c)
        return { ...page, next: page.nextCursor }
      })
      if (isStopped()) return
      cursor = read.cursor
      const added = read.pages.flatMap((p) => p.messages.map(toMessage))
      const replace = fresh
      fresh = false
      if (added.length === 0 && !replace) return
      setLoaded((prev) => {
        const base = replace || prev.stageId !== stageId ? [] : prev.messages
        return { stageId, messages: withAnswersApplied(base.concat(added)) }
      })
    }, POLL_INTERVAL_MS)
    return poll.stop
  }, [stageId, finished])

  // A response for the previous stage is never shown for this one.
  return loaded.stageId === stageId ? loaded.messages : EMPTY.messages
}
