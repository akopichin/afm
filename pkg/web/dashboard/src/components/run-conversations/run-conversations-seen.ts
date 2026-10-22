import { useCallback, useMemo, useRef, useState } from 'react'
import type { MeshThread, MeshThreadSummaries } from '../../api/mesh-client'

// RUN_CONVERSATIONS_SEEN_KEY — one JSON map in localStorage: runId -> { threadId: last seen agent-to-user seq }.
// Unlike Team's page-local cursors it survives a reload, so a question the human has not read stays marked.
export const RUN_CONVERSATIONS_SEEN_KEY = 'afm.runConversationsSeen'

type SeenSeqs = Record<string, number>

function readStore(): Record<string, SeenSeqs> {
  try {
    const stored: unknown = JSON.parse(localStorage.getItem(RUN_CONVERSATIONS_SEEN_KEY) ?? '{}')
    return typeof stored === 'object' && stored !== null ? (stored as Record<string, SeenSeqs>) : {}
  } catch {
    return {}
  }
}

// loadSeen — this run's cursors; absent, corrupt or unusable entries count as 0 (everything unseen).
function loadSeen(runId: string): SeenSeqs {
  const stored = readStore()[runId]
  if (typeof stored !== 'object' || stored === null) return {}
  return Object.fromEntries(Object.entries(stored).filter(([, seq]) => typeof seq === 'number' && seq > 0))
}

// saveSeen — merges one thread's cursor into what is stored now, not into this page's snapshot: another tab on the
// same run may have stored other threads (or a higher cursor) since this one loaded, and none of it may be lost.
function saveSeen(runId: string, threadId: string, seq: number): void {
  try {
    const store = readStore()
    const stored = store[runId]
    const runSeen = typeof stored === 'object' && stored !== null ? stored : {}
    const storedSeq = runSeen[threadId]
    const kept = typeof storedSeq === 'number' ? storedSeq : 0
    localStorage.setItem(RUN_CONVERSATIONS_SEEN_KEY, JSON.stringify({ ...store, [runId]: { ...runSeen, [threadId]: Math.max(kept, seq) } }))
  } catch {
    // Storage disabled or full: the cursor then lives only for this page.
  }
}

export type RunConversationsSeen = {
  // unreadThreadIds — threads whose newest agent-to-user message is beyond the seen cursor.
  unreadThreadIds: ReadonlySet<string>
  seenSeqs: Readonly<SeenSeqs>
  // markSeen — move a thread's cursor forward to `seq`; it never moves back.
  markSeen: (threadId: string, seq: number) => void
}

// useRunConversationsSeen — the unread state of the run's conversations. `threads` are the ones the panel
// shows; the signal comes from the summary's last_agent_to_user_message_seq only, so the human's own messages,
// agent-to-agent chatter and passive results never raise it.
export function useRunConversationsSeen(runId: string, threads: MeshThread[], summaries: MeshThreadSummaries): RunConversationsSeen {
  const [state, setState] = useState(() => ({ runId, seen: loadSeen(runId) }))
  // A different run starts from its own stored cursors.
  const current = state.runId === runId ? state : { runId, seen: loadSeen(runId) }
  if (current !== state) setState(current)
  const seenRef = useRef(current.seen)
  seenRef.current = current.seen

  const markSeen = useCallback((threadId: string, seq: number): void => {
    if (seq <= (seenRef.current[threadId] ?? 0)) return
    const seen = { ...seenRef.current, [threadId]: seq }
    seenRef.current = seen
    setState({ runId, seen })
    saveSeen(runId, threadId, seq)
  }, [runId])

  const unreadThreadIds = useMemo(
    () => new Set(threads.filter((t) => (summaries[t.id]?.last_agent_to_user_message_seq ?? 0) > (current.seen[t.id] ?? 0)).map((t) => t.id)),
    [threads, summaries, current.seen],
  )
  return { unreadThreadIds, seenSeqs: current.seen, markSeen }
}
