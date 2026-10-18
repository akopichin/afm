import { useEffect, useRef, useState } from 'react'
import { getMeshThread, type MeshMessage } from '../../api/mesh-client'
import { startSerialPoll } from '../../lib/serial-poll'

const POLL_INTERVAL_MS = 3000

// PAGE_LIMIT — passed explicitly on every request so "the page came back full" is a deterministic signal we
// control, rather than relying on whatever page size the server defaults to today.
const PAGE_LIMIT = 50

// loadAllMessages walks every page of a thread from cursor 0 forward, since a thread can hold up to
// agentcomm.MaxThreadMessages (200) — well beyond one page — and the UI must show the whole conversation, not
// just its first PAGE_LIMIT messages.
async function loadAllMessages(threadId: string): Promise<MeshMessage[]> {
  const all: MeshMessage[] = []
  let cursor = 0
  for (;;) {
    const page = await getMeshThread(threadId, cursor, PAGE_LIMIT)
    all.push(...page.messages)
    if (page.messages.length < PAGE_LIMIT || page.next_cursor <= cursor) break
    cursor = page.next_cursor
  }
  return all
}

// useMeshThread polls one conversation's messages — GET /api/mesh/threads/{id}, paged forward from cursor 0
// each tick (threads are short-lived and budget-capped, so re-reading the whole thread per tick is cheap —
// same "just re-fetch everything" posture discussion/board polling already use elsewhere in this app;
// nothing here needs StageDiscussionPanel's incremental-cursor complexity). threadId === null → no thread
// opened yet (a fresh direct conversation before its first message) — no network calls, empty list.
export function useMeshThread(threadId: string | null): MeshMessage[] {
  const [messages, setMessages] = useState<MeshMessage[]>([])
  const seenThreadId = useRef<string | null>(null)

  useEffect(() => {
    if (threadId === null) {
      setMessages([])
      return
    }
    if (seenThreadId.current !== threadId) {
      seenThreadId.current = threadId
      setMessages([])
    }
    // Serial: a slow read can never land after (and overwrite) a newer one.
    const poll = startSerialPoll(
      (isStopped) =>
        loadAllMessages(threadId).then(
          (all) => {
            if (!isStopped()) setMessages(all)
          },
          () => {
            /* transient — keep whatever we had, same posture as useStageAssistants */
          },
        ),
      POLL_INTERVAL_MS,
    )
    return poll.stop
  }, [threadId])

  return messages
}
