import type { AssistantActivityEntry } from '../../types/assistant-activity'

// What a tool or message row shows besides its text. 'none' is a plain message or a legacy static line.
export type ActivityRowState =
  | 'none'
  | 'running'
  | 'finished'
  | 'failed'
  | 'timed_out'
  | 'usage_limit'
  | 'unknown_outcome'
  | 'not_received'
  | 'interrupted'
  | 'answer_submitted'

export type ActivityRow = {
  // React key and scroll anchor: stable while the row's entry is replaced by a newer revision.
  key: string
  entry: AssistantActivityEntry
  state: ActivityRowState
  durationMs: number | null
}

type Item = { key: string; message?: AssistantActivityEntry; start?: AssistantActivityEntry; finish?: AssistantActivityEntry }

const FINISH_STATE: Record<string, ActivityRowState> = {
  completed: 'finished',
  failed: 'failed',
  timed_out: 'timed_out',
  usage_limit: 'usage_limit',
  interrupted: 'interrupted',
}

function span(from: string | undefined, to: string | undefined): number | null {
  if (from === undefined || to === undefined) return null
  const ms = Date.parse(to) - Date.parse(from)
  return Number.isFinite(ms) && ms >= 0 ? ms : null
}

// projectActivity merges the updates of one item into one row at its first position, whatever the arrival order.
// A tool without a finish is "running" only in the task's latest invocation and turn; otherwise it is "not received"
// (or "interrupted" when the invocation says so).
export function projectActivity(entries: AssistantActivityEntry[], taskRunning: boolean, currentTurn?: number): ActivityRow[] {
  const invocationOutcome = new Map<string, string>()
  let latestInvocation = ''
  const order: (Item | AssistantActivityEntry)[] = []
  const items = new Map<string, Item>()

  for (const entry of entries) {
    const { invocationId = '', itemId = '' } = entry
    if (invocationId !== '') latestInvocation = invocationId
    if (entry.kind === 'invocation_finished') {
      invocationOutcome.set(invocationId, entry.outcome ?? 'unknown')
      continue
    }
    if (entry.kind === undefined || itemId === '') {
      order.push(entry)
      continue
    }
    const key = `${invocationId.length}:${invocationId}:${itemId}`
    let item = items.get(key)
    if (item === undefined) {
      item = { key }
      items.set(key, item)
      order.push(item)
    }
    if (entry.kind === 'message') {
      if (item.message === undefined || (entry.revision ?? 0) >= (item.message.revision ?? 0)) item.message = entry
    } else if (entry.kind === 'tool_started') {
      item.start ??= entry
    } else {
      item.finish = entry
    }
  }

  return order.flatMap((slot): ActivityRow[] => {
    if (!('key' in slot)) return [{ key: slot.eventId, entry: slot, state: 'none', durationMs: null }]
    if (slot.message !== undefined) {
      return [{ key: slot.key, entry: slot.message, state: slot.message.final === true ? 'answer_submitted' : 'none', durationMs: null }]
    }
    const entry = slot.finish ?? slot.start
    if (entry === undefined) return []
    if (slot.finish !== undefined) {
      const durationMs = span(slot.finish.startedAt ?? slot.start?.startedAt, slot.finish.finishedAt)
      return [{ key: slot.key, entry, state: FINISH_STATE[slot.finish.outcome ?? ''] ?? 'unknown_outcome', durationMs }]
    }
    const invocationId = entry.invocationId ?? ''
    const closed = invocationOutcome.get(invocationId)
    let state: ActivityRowState = 'not_received'
    if (closed === 'interrupted') state = 'interrupted'
    else if (closed === undefined && taskRunning && invocationId === latestInvocation && entry.turn >= (currentTurn ?? 0)) state = 'running'
    return [{ key: slot.key, entry, state, durationMs: null }]
  })
}

// latestAction is the newest message or tool update in arrival order; merged rows keep their first position, so
// the last row is not the newest action. Invocation closures are control records, not actions.
export function latestAction(entries: AssistantActivityEntry[]): AssistantActivityEntry | null {
  for (let i = entries.length - 1; i >= 0; i--) {
    if (entries[i]?.kind !== 'invocation_finished') return entries[i] ?? null
  }
  return null
}
