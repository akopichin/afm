import { useMemo, type ReactElement } from 'react'
import type { AfmEvent } from '../../types'
import { FeedWorkspace } from '../feed-workspace'
import { isRunAgentBudgetNotice } from '../../types/budget'

export type ActivityPanelProps = {
  // events — the SAME run-wide WebSocket feed App.tsx already holds (useEventFeed); this panel filters it
  // down to mesh_* and run agents' budget notices so Activity shares the existing stream with the stage Feed.
  events: AfmEvent[]
  // arrivals — mesh_* events accepted live so far (ArrivalCounts.mesh).
  arrivals?: number
}

// ActivityPanel — the Team view's "Activity" tab: a read-only log of mesh_* notices (participants joining,
// threads opening/closing, deliveries, waits, board publishes — see feed-view-model.ts's mapEvent cases).
// Reuses FeedWorkspace's existing bubble rendering instead of a second text-building switch: omitting
// noteTarget means no composer mounts, which is exactly right for a pure activity log.
export function ActivityPanel({ events, arrivals }: ActivityPanelProps): ReactElement {
  const meshEvents = useMemo(() => events.filter((e) => e.type.startsWith('mesh_') || isRunAgentBudgetNotice(e)), [events])
  return (
    <FeedWorkspace
      events={meshEvents}
      arrivals={arrivals}
      stageId={null}
      emptyHint="No mesh activity yet"
    />
  )
}
