import { useEffect, useState } from 'react'
import { getStageMessages, type MeshMessage, type MeshThread } from '../../api/mesh-client'
import { startSerialPoll } from '../../lib/serial-poll'

const POLL_INTERVAL_MS = 3000
const PAGE_LIMIT = 100

export type StageConversation = {
  // messages — every conversation message the stage's feed shows, in journal order, each once.
  messages: MeshMessage[]
  // threads — the threads of those messages, by id.
  threads: Record<string, MeshThread>
}

type Loaded = StageConversation & { stageId: string | null }

const EMPTY: Loaded = { stageId: null, messages: [], threads: {} }

// useStageConversation polls the conversations of stage stageId's feed — GET /api/mesh/stages/{id}/messages,
// forward from the last seq read, so each tick fetches only what is new. Polls are serial, so pages are
// applied in order and the cursor never skips or re-reads a page. null — no stage (or no mesh): no requests.
export function useStageConversation(stageId: string | null): StageConversation {
  const [loaded, setLoaded] = useState<Loaded>(EMPTY)

  useEffect(() => {
    if (stageId === null) return
    let cursor = 0
    const poll = startSerialPoll(async (isStopped) => {
      const messages: MeshMessage[] = []
      const threads: MeshThread[] = []
      let next = cursor
      for (;;) {
        const page = await getStageMessages(stageId, next, PAGE_LIMIT)
        messages.push(...page.messages)
        threads.push(...page.threads)
        const advanced = page.next_cursor > next
        next = Math.max(next, page.next_cursor)
        if (!page.has_more || !advanced) break
      }
      if (isStopped()) return
      cursor = next
      if (messages.length === 0) return
      setLoaded((prev) => {
        const base = prev.stageId === stageId ? prev : { ...EMPTY, stageId }
        const known = new Set(base.messages.map((m) => m.id))
        const added: MeshMessage[] = []
        for (const m of messages) {
          if (known.has(m.id)) continue
          known.add(m.id)
          added.push(m)
        }
        const byId = { ...base.threads }
        for (const t of threads) byId[t.id] = t
        return { stageId, messages: added.length === 0 ? base.messages : [...base.messages, ...added], threads: byId }
      })
    }, POLL_INTERVAL_MS)
    return poll.stop
  }, [stageId])

  // A response for the previous stage is never shown for this one.
  return loaded.stageId === stageId ? loaded : EMPTY
}
