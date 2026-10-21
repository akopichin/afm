import type { MeshThreadSummaries } from '../api/mesh-client'

// Page-local only: a Team remount keeps its cursors, while a page reload starts a fresh baseline.
const seenByRun = new Map<string, Map<string, number>>()

export function baselineTeamUnread(runId: string, summaries: MeshThreadSummaries): void {
  if (runId === '' || seenByRun.has(runId)) return
  seenByRun.set(runId, new Map(Object.entries(summaries).map(([id, summary]) => [id, summary.last_other_message_seq])))
}

export function isTeamUnread(runId: string, threadId: string, lastOtherSeq: number): boolean {
  const cursors = seenByRun.get(runId)
  return cursors !== undefined && lastOtherSeq > (cursors.get(threadId) ?? 0)
}

export function advanceTeamSeen(runId: string, threadId: string, renderedOtherSeq: number): boolean {
  const cursors = seenByRun.get(runId)
  if (cursors === undefined || renderedOtherSeq <= (cursors.get(threadId) ?? 0)) return false
  cursors.set(threadId, renderedOtherSeq)
  return true
}
