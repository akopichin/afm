import { describe, it, expect, vi, afterEach } from 'vitest'
import type { AfmEvent } from '../../types'
import { toEvent } from '../../hooks/use-event-feed/use-event-feed'
import { buildFeedTimeline, toFeedItems } from './feed-view-model'
import { buildVirtualFeedRows, headShift, openSeriesTails } from './feed-virtual-model'
import { emptyRowState, syncRowState, toggleSeries, type FeedRowState } from './feed-row-state'

// Live events of one millisecond: nothing but their order tells identical calls apart.
const call = (): AfmEvent => toEvent({ type: 'agent_action', stage_id: 's1', data: { tool: 'Read', detail: 'same' } })
const status = (seq: number): AfmEvent => toEvent({ type: 'stage_status_changed', stage_id: 's1', data: 'running', seq })
const calls = (n: number): AfmEvent[] => Array.from({ length: n }, call)

const keysOf = (events: AfmEvent[]): string[] => toFeedItems(events).map((item) => item.key)

// chain is the production order of FeedWorkspace: rows are built from the state before the sync, then it syncs.
function chain(events: AfmEvent[], state: FeedRowState) {
  const items = toFeedItems(events)
  const timeline = buildFeedTimeline(items, [], 'all')
  const model = buildVirtualFeedRows(timeline, state.openTools)
  const synced = syncRowState(state, {
    itemKeys: new Set(items.map((item) => item.key)),
    reportKeys: new Set(),
    messageIds: new Set(),
    imageUrls: new Set(),
    seriesTails: openSeriesTails(timeline, state.openTools),
  })
  return { items, model, synced }
}
const summaries = (model: ReturnType<typeof buildVirtualFeedRows>) => model.rows.flatMap((row) => (row.kind === 'tool-summary' ? [{ open: row.open, size: row.items.length }] : []))

afterEach(() => vi.useRealTimers())

describe('keys of live events without seq', () => {
  it('stay the same when the head is evicted, also when as many are appended', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-01-01T10:00:00Z'))
    const events = calls(5)
    const keys = keysOf(events)
    expect(new Set(keys).size).toBe(5)
    expect(keysOf(events.slice(1))).toEqual(keys.slice(1))
    const next = call()
    expect(keysOf([...events.slice(1), next])).toEqual([...keys.slice(1), expect.not.stringMatching(new RegExp(`^(${keys.join('|')})$`))])
  })

  it('keep a closed series closed and an open one open when its first call is evicted', () => {
    const events = [...calls(3), status(1), ...calls(3)]
    const items = toFeedItems(events)
    const series1 = items.slice(0, 3).map((item) => item.key)
    const open = toggleSeries(emptyRowState('s1'), series1)

    const evicted = chain(events.slice(1), open)
    expect(summaries(evicted.model)).toEqual([{ open: true, size: 2 }, { open: false, size: 3 }])
    expect(summaries(buildVirtualFeedRows(buildFeedTimeline(evicted.items, [], 'all'), evicted.synced.openTools))).toEqual([{ open: true, size: 2 }, { open: false, size: 3 }])
  })

  it('keep a closed series closed when the head is evicted and as many are appended', () => {
    const events = [...calls(3), status(1), ...calls(3)]
    const series1 = toFeedItems(events).slice(0, 3).map((item) => item.key)
    const open = toggleSeries(emptyRowState('s1'), series1)

    const shifted = chain([...events.slice(1), call()], open)
    expect(summaries(shifted.model)).toEqual([{ open: true, size: 2 }, { open: false, size: 4 }])
    expect(summaries(buildVirtualFeedRows(buildFeedTimeline(shifted.items, [], 'all'), shifted.synced.openTools))).toEqual([{ open: true, size: 2 }, { open: false, size: 4 }])
  })

  it('drop the state of a series that is evicted whole', () => {
    const events = [...calls(3), status(1), ...calls(3)]
    const series1 = toFeedItems(events).slice(0, 3).map((item) => item.key)
    const open = toggleSeries(emptyRowState('s1'), series1)

    const gone = chain(events.slice(4), open)
    expect(gone.synced.openTools.size).toBe(0)
    expect(summaries(gone.model)).toEqual([{ open: false, size: 3 }])
  })

  it('give headShift the number of evicted rows for a pure head eviction', () => {
    const texts = Array.from({ length: 5 }, () => toEvent({ type: 'agent_action', stage_id: 's1', data: { tool: 'text', detail: 'same' } }))
    const rowKeys = (list: AfmEvent[]) => buildVirtualFeedRows(buildFeedTimeline(toFeedItems(list), [], 'all'), new Set()).rows.map((row) => row.key)
    expect(headShift(rowKeys(texts), rowKeys(texts.slice(1)))).toBe(1)
    expect(headShift(rowKeys(texts), rowKeys(texts.slice(2)))).toBe(2)
  })
})
