import type { AssistantActivityEntry, AssistantActivityPage } from '../../types/assistant-activity'

export const PAGE_SIZE = 100
// The saved rows the panel holds: one contiguous slice of the task's history that always ends at the live end.
// Older pages load on demand until the bound; the rest of the history stays in the saved file.
export const MAX_ACTIVITY_ENTRIES = 2000

// FirstRead — the tail or forward read whose request first had a live entry in the ring: the last saved row
// before it (from) and after it (to); null — none. The entry is after `from` and before every row read after `to`.
export type FirstRead = { from: string | null; to: string | null }

// ActivityWindow — the saved rows plus the cursors that continue from both of its ends.
export type ActivityWindow = {
  rows: AssistantActivityEntry[]
  // Cursor of the next forward request; null until the tail is loaded (or after the server rejected it).
  after: string | null
  // Cursor of the next older page; valid whenever hasOlder is true.
  before: string
  hasOlder: boolean
  // Live event_ids that were already in the ring when rows[0] last moved: an unsaved one of them may belong
  // before the window, so it is hidden until an older page gives it a neighbour.
  unplaced: ReadonlySet<string>
  // Live event_id → its first read; set once, so a later read never moves an unsaved entry.
  seen: ReadonlyMap<string, FirstRead>
}

export const EMPTY_WINDOW: ActivityWindow = { rows: [], after: null, before: '0', hasOlder: false, unplaced: new Set(), seen: new Map() }

// Ring entries no earlier read saw get this read; the ring bounds the map.
function seenBy(win: ActivityWindow, rows: AssistantActivityEntry[], ringAtRequest: string[]): Map<string, FirstRead> {
  const read: FirstRead = { from: win.rows.at(-1)?.eventId ?? null, to: rows.at(-1)?.eventId ?? null }
  return new Map(ringAtRequest.map((id) => [id, win.seen.get(id) ?? read]))
}

// OrderMemory — what the next runner of the same task starts from, so switching helpers keeps the order.
export type OrderMemory = { seen: ReadonlyMap<string, FirstRead>; last: string | null }

export function rememberOrder(win: ActivityWindow): OrderMemory {
  return { seen: win.seen, last: win.rows.at(-1)?.eventId ?? null }
}

// Entries that arrived after the previous runner's last read come after its last row; with no row, nothing is known.
export function windowFrom(memory: OrderMemory | undefined, ring: string[]): ActivityWindow {
  if (memory === undefined) return EMPTY_WINDOW
  const { last } = memory
  const seen = new Map<string, FirstRead>()
  for (const id of ring) {
    const read = memory.seen.get(id) ?? (last === null ? undefined : { from: last, to: last })
    if (read !== undefined) seen.set(id, read)
  }
  return { ...EMPTY_WINDOW, seen }
}

export function tailWindow(win: ActivityWindow, page: AssistantActivityPage, ringAtRequest: string[]): ActivityWindow {
  const seen = seenBy(win, page.entries, ringAtRequest)
  return { rows: page.entries, after: page.after, before: page.before, hasOlder: page.hasOlder, unplaced: new Set(ringAtRequest), seen }
}

// growForward appends a forward page and, past the bound, drops the oldest rows; `before` then points right
// behind the new first row.
export function growForward(win: ActivityWindow, page: AssistantActivityPage, liveIds: string[], ringAtRequest: string[]): ActivityWindow {
  const all = [...win.rows, ...withoutKnown(page.entries, win.rows)]
  const seen = seenBy(win, all, ringAtRequest)
  const dropped = all.length - MAX_ACTIVITY_ENTRIES
  if (dropped <= 0) return { ...win, rows: all, after: page.after, seen }
  return {
    rows: all.slice(dropped),
    after: page.after,
    before: all[dropped - 1]?.cursor ?? win.before,
    hasOlder: true,
    unplaced: new Set(liveIds),
    seen,
  }
}

export function growOlder(win: ActivityWindow, page: AssistantActivityPage): ActivityWindow {
  return { ...win, rows: [...withoutKnown(page.entries, win.rows), ...win.rows], before: page.before, hasOlder: page.hasOlder }
}

// How many older rows still fit under the bound; 0 means the window is full.
export function olderRoom(win: ActivityWindow): number {
  return Math.min(PAGE_SIZE, MAX_ACTIVITY_ENTRIES - win.rows.length)
}

function withoutKnown(page: AssistantActivityEntry[], rows: AssistantActivityEntry[]): AssistantActivityEntry[] {
  const known = new Set(rows.map((e) => e.eventId))
  return page.filter((e) => !known.has(e.eventId))
}

export type MergeContext = {
  // The saved history has nothing before rows[0]: an unsaved entry can only belong after that point.
  atStart: boolean
  unplaced: ReadonlySet<string>
  seen: ReadonlyMap<string, FirstRead>
}

// mergeActivity puts an unsaved live entry right after the latest row known to precede it — its saved live
// predecessor or the end of the rows read before it arrived — and never past its saved live successor. With
// nothing known before it, it goes as late as its first read and its successor allow. Stream order, never time.
export function mergeActivity(rows: AssistantActivityEntry[], live: AssistantActivityEntry[], context: MergeContext): AssistantActivityEntry[] {
  if (live.length === 0) return rows
  const index = new Map(rows.map((e, i) => [e.eventId, i]))
  const position = (id: string | null) => (id === null ? -1 : (index.get(id) ?? -1))
  // Unsaved entries with the window positions of their saved live neighbours; rows.length — none after.
  const pending: { entry: AssistantActivityEntry; prev: number; next: number }[] = []
  const ids = new Set<string>()
  let prev = -1
  for (const entry of live) {
    const at = index.get(entry.eventId)
    if (at !== undefined) {
      prev = at
      for (let k = pending.length - 1; k >= 0 && pending[k]!.next === rows.length; k--) pending[k]!.next = at
      continue
    }
    if (ids.has(entry.eventId)) continue
    ids.add(entry.eventId)
    pending.push({ entry, prev, next: rows.length })
  }

  // Slot -1 is in front of rows[0]; slot i is right after rows[i].
  const bySlot = new Map<number, AssistantActivityEntry[]>()
  for (const { entry, prev, next } of pending) {
    const read = context.seen.get(entry.eventId)
    const lower = read === undefined ? rows.length - 1 : position(read.from)
    const known = prev >= 0 || (lower >= 0 && lower < next)
    if (!known && !context.atStart && context.unplaced.has(entry.eventId)) continue
    // Accepted: an unsaved entry and a saved row the socket dropped, both made within one poll interval,
    // cannot be ordered here — event_ids carry no sequence — so the entry stays before that row.
    const late = prev < 0 && read !== undefined && read.from === null
    const slot = late ? Math.min(position(read.to), next - 1) : Math.min(Math.max(prev, lower), next - 1)
    const group = bySlot.get(slot)
    if (group === undefined) bySlot.set(slot, [entry])
    else group.push(entry)
  }
  if (bySlot.size === 0) return rows

  const merged: AssistantActivityEntry[] = [...(bySlot.get(-1) ?? [])]
  rows.forEach((row, i) => merged.push(row, ...(bySlot.get(i) ?? [])))
  return merged
}
