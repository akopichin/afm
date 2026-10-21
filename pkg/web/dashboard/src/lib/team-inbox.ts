import type { MeshThread, MeshThreadSummaries } from '../api/mesh-client'

// Preserve the API's thread-open order for equal seqs and empty conversations. Timestamps are display-only:
// clocks can move backward while the journal seq remains monotonic.
export function orderInboxThreads(threads: MeshThread[], summaries: MeshThreadSummaries): MeshThread[] {
  const openOrder = new Map(threads.map((thread, index) => [thread.id, index]))
  return [...threads].sort((a, b) => {
    const aSeq = summaries[a.id]?.last_message?.seq ?? 0
    const bSeq = summaries[b.id]?.last_message?.seq ?? 0
    return bSeq - aSeq || (openOrder.get(a.id) ?? 0) - (openOrder.get(b.id) ?? 0) || a.id.localeCompare(b.id)
  })
}
