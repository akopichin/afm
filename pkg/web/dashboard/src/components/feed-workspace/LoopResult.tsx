import type { ReactElement } from 'react'
import type { MeshLoop, MeshParticipant } from '../../api/mesh-client'
import { displayTime } from '../message-time/MessageTime'
import { useMessageTimeZone } from '../../lib/message-time-zone'
import { mentionHandles, senderLabel } from '../../lib/mesh-roster'
import type { Stage } from '../../types'

// LoopOutcome — what a /loop command answered: the loop it created or stopped, or the run's loops.
export type LoopOutcome = { kind: 'created' | 'stopped'; loop: MeshLoop } | { kind: 'list'; loops: MeshLoop[] }

// formatHostTime — the instant in the host's time zone, with seconds ("Oct 23, 2026, 19:05:07"), via the same
// IANA-authoritative/UTC-offset-fallback rule the Feed's own message times use (MessageTime.tsx's displayTime).
function formatHostTime(iso: string, timeZone: string, utcOffsetMinutes: number): string {
  const date = new Date(iso)
  if (!Number.isFinite(date.getTime())) return iso
  return displayTime(date, timeZone, utcOffsetMinutes).full
}

type Names = { stages: Stage[]; handles: Map<string, string> }

const target = (loop: MeshLoop, names: Names): string => `@${names.handles.get(loop.target_id) ?? loop.target_id}`
const stageName = (id: string, names: Names): string => names.stages.find((s) => s.id === id)?.name || id
// origin — where the loop's messages show: its Feed, or the run conversations for a loop an agent made.
const origin = (loop: MeshLoop, names: Names): string =>
  loop.origin_stage !== undefined && loop.origin_stage !== '' ? `Feed: ${stageName(loop.origin_stage, names)}` : 'run conversations'
const bound = (loop: MeshLoop, names: Names): string | null =>
  loop.until_stage !== undefined && loop.until_stage !== '' ? `ends with ${stageName(loop.until_stage, names)}` : null
const ended = (loop: MeshLoop): string => (loop.end_reason !== undefined && loop.end_reason !== '' ? ` — ${loop.end_reason}` : '')

// LoopOutcomeView — the one-shot result of a /loop command, a status region the composer keeps until the next
// edit or Escape. Times are in the host's time zone, like the messages of the Feed.
export function LoopOutcomeView({ outcome, participants, stages }: { outcome: LoopOutcome; participants: MeshParticipant[]; stages: Stage[] }): ReactElement {
  const { timeZone, utcOffsetMinutes } = useMessageTimeZone()
  const names: Names = { stages, handles: mentionHandles(participants) }
  const time = (iso: string): string => formatHostTime(iso, timeZone, utcOffsetMinutes)

  if (outcome.kind === 'list') {
    return (
      <div className="mesh-message-receipt mesh-loop-result" role="status" aria-label="Loops">
        {outcome.loops.length === 0 ? (
          <p>No loops in this run</p>
        ) : (
          <ul>
            {outcome.loops.map((loop) => (
              <li key={loop.id}>
                <strong>{loop.id}</strong> · {loop.status}
                {ended(loop)} · every {loop.every} · {target(loop, names)} · by {senderLabel(loop.creator_id, participants)} ·{' '}
                {origin(loop, names)}
                {bound(loop, names) !== null && ` · ${bound(loop, names)}`}
                {loop.next_tick_at !== undefined && ` · next tick ${time(loop.next_tick_at)}`}
              </li>
            ))}
          </ul>
        )}
      </div>
    )
  }

  const { loop } = outcome
  if (outcome.kind === 'stopped') {
    // Stopping a loop that is already over is not an error: it says how it ended.
    return (
      <div className="mesh-message-receipt mesh-loop-result" role="status">
        <p>{loop.status === 'active' ? `Loop ${loop.id} is still running` : `Loop ${loop.id} ${loop.status === 'stopped' ? 'stopped' : 'had already ended'}${ended(loop)}`}</p>
      </div>
    )
  }
  return (
    <div className="mesh-message-receipt mesh-loop-result" role="status">
      <p>
        <strong>Loop {loop.id} started</strong> · every {loop.every} to {target(loop, names)}
        {bound(loop, names) !== null && ` · ${bound(loop, names)}`}
      </p>
      {loop.next_tick_at !== undefined && <p>Next tick {time(loop.next_tick_at)}</p>}
      <p>{origin(loop, names)} · stop it with <code>/loop stop {loop.id}</code></p>
    </div>
  )
}
