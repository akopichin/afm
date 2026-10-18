import { useEffect, useRef, useState } from 'react'
import { getMeshThread, type MeshMessage } from '../../api/mesh-client'

const POLL_INTERVAL_MS = 3000

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
    let cancelled = false
    const load = (): void => {
      getMeshThread(threadId)
        .then((page) => {
          if (!cancelled) setMessages(page.messages)
        })
        .catch(() => {
          /* transient — keep whatever we had, same posture as useStageAssistants */
        })
    }
    load()
    const timer = setInterval(load, POLL_INTERVAL_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [threadId])

  return messages
}
