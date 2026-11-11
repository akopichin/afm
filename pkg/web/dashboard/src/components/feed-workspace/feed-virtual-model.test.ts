import { describe, it, expect } from 'vitest'
import type { AfmEvent } from '../../types'
import type { MeshMessage } from '../../api/mesh-client'
import { buildFeedTimeline, toFeedItems } from './feed-view-model'
import { buildVirtualFeedRows, headShift, openSeriesTails } from './feed-virtual-model'

const at = (n: number): string => new Date(Date.UTC(2026, 6, 10, 10, 0, n)).toISOString()
const ev = (type: string, payload: unknown, n: number, seq?: number): AfmEvent =>
  ({ type, payload, stageId: 's1', timestamp: at(n), ...(seq !== undefined ? { seq } : {}) }) as AfmEvent
const tool = (n: number): AfmEvent => ev('agent_action', { tool: 'Read', detail: `f${n}` }, n, n)
const text = (n: number): AfmEvent => ev('agent_action', { tool: 'text', detail: `t${n}` }, n, n)
const msg = (id: string, n: number): MeshMessage => ({ id, thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['stage:s1'], kind: 'message', body: id, seq: n, created_at: at(n) })

const build = (events: AfmEvent[], messages: MeshMessage[] = [], open: string[] = []) =>
  buildVirtualFeedRows(buildFeedTimeline(toFeedItems(events), messages, 'all'), new Set(open))

describe('buildVirtualFeedRows', () => {
  it('cuts a non-framed group into one row per item, in timeline order', () => {
    const { rows } = build([ev('stage_status_changed', 'running', 0, 1), text(2), tool(3), tool(4), text(5)])
    expect(rows.map((r) => r.kind)).toEqual(['event', 'event', 'tool-summary', 'event'])
    expect(rows.map((r) => r.key)).toEqual(['seq:1', 'seq:2', 'tools:seq:3', 'seq:5'])
  })

  it('keeps a series closed without children and opens it with a call row per call', () => {
    const closed = build([tool(1), tool(2), tool(3)])
    expect(closed.rows.map((r) => r.kind)).toEqual(['tool-summary'])
    const open = build([tool(1), tool(2), tool(3)], [], ['seq:2'])
    expect(open.rows.map((r) => r.kind)).toEqual(['tool-summary', 'tool-call', 'tool-call', 'tool-call'])
    expect(open.rows[0]).toMatchObject({ open: true, key: 'tools:seq:1' })
    expect(open.rows[1]).toMatchObject({ seriesKey: 'tools:seq:1', key: 'seq:1' })
  })

  it('gives 5000 calls of one open series 5000 call rows plus the summary', () => {
    const events = Array.from({ length: 5000 }, (_, i) => tool(i))
    const { rows } = build(events, [], ['seq:0'])
    expect(rows).toHaveLength(5001)
  })

  it('marks the start and end of a group and carries the author on the first row', () => {
    const { rows } = build([text(1), tool(2), text(3)])
    expect(rows.map((r) => [r.start, r.end])).toEqual([[true, false], [false, true], [true, true]])
    // Каждое сообщение агента начинает свою группу.
    const two = build([text(1), text(2)]).rows
    expect(two.map((r) => [r.start, r.end])).toEqual([[true, true], [true, true]])
    expect(two[0]).toMatchObject({ actor: 'agent', side: 'left', stageId: 's1' })
  })

  it('keeps a framed user group as one row', () => {
    const { rows } = build([ev('approved', {}, 1, 1), ev('revised', 2, 2, 2)])
    expect(rows).toHaveLength(1)
    expect(rows[0]).toMatchObject({ kind: 'framed', start: true, end: true })
  })

  it('makes a conversation row per message with previousCreatedAt from its original block', () => {
    const { rows, rowKeyByMessageId, indexByKey } = build([text(1), text(9)], [msg('a', 3), msg('b', 4), msg('c', 5)])
    expect(rows.map((r) => r.key)).toEqual(['seq:1', 'a', 'b', 'c', 'seq:9'])
    expect(rows[2]).toMatchObject({ kind: 'conversation', previousCreatedAt: at(3) })
    expect(rows[1]).toMatchObject({ previousCreatedAt: undefined })
    expect(rowKeyByMessageId.get('b')).toBe('b')
    expect(indexByKey.get('c')).toBe(3)
  })

  it('does not depend on the open state for counts: a closed series keeps its item count', () => {
    const closed = build([tool(1), tool(2)]).rows[0]
    expect(closed).toMatchObject({ kind: 'tool-summary', open: false })
    expect(closed && 'items' in closed ? closed.items : []).toHaveLength(2)
  })

  it('keeps a series open after its first call is evicted, and drops the state with the series', () => {
    const open = ['seq:1', 'seq:2']
    // Вытеснен seq:1, но открыт seq:2 — серия по-прежнему открыта.
    expect(build([tool(2), tool(3)], [], open).rows[0]).toMatchObject({ open: true, key: 'tools:seq:2' })
    // Серия целиком другая — открытое состояние на неё не переходит.
    expect(build([tool(5), tool(6)], [], open).rows[0]).toMatchObject({ open: false })
  })

  it('keeps legacy repeated keys distinct and does not move state onto another row', () => {
    const noSeq = (): AfmEvent => ({ type: 'agent_action', payload: { tool: 'Read', detail: 'same' }, stageId: 's1', timestamp: at(1) }) as AfmEvent
    const { rows } = build([noSeq(), noSeq(), noSeq()], [], [])
    expect(rows).toHaveLength(1)
    const keys = (rows[0] as { items: { key: string }[] }).items.map((i) => i.key)
    expect(new Set(keys).size).toBe(3)
  })

  it('keeps keys of kept rows stable on append', () => {
    const a = build([text(1), tool(2)]).rows.map((r) => r.key)
    const b = build([text(1), tool(2), tool(3), text(4)]).rows.map((r) => r.key)
    expect(b.slice(0, a.length)).toEqual(a)
  })
})

describe('openSeriesTails', () => {
  const events = [text(1), tool(2), tool(3), tool(4)]
  const timelineOf = (filter: 'all' | 'conversations') => buildFeedTimeline(toFeedItems(events), [], filter)

  it('takes the last call of every open series from the timeline it is given', () => {
    expect(openSeriesTails(timelineOf('all'), new Set(['seq:2']))).toEqual(['seq:4'])
    expect(openSeriesTails(timelineOf('all'), new Set())).toEqual([])
  })

  it('needs the unfiltered timeline: a filter that hides the series leaves no tail to keep it open', () => {
    expect(openSeriesTails(timelineOf('conversations'), new Set(['seq:2']))).toEqual([])
  })
})

describe('headShift', () => {
  it('is 0 when only rows are appended', () => {
    expect(headShift(['a', 'b'], ['a', 'b', 'c'])).toBe(0)
  })
  it('counts rows evicted from the head, with appended rows', () => {
    expect(headShift(['a', 'b', 'c', 'd'], ['c', 'd', 'e', 'f'])).toBe(2)
  })
  it('is null when the head is renumbered, merged or filtered', () => {
    expect(headShift(['a', 'b', 'c'], ['x', 'b', 'c'])).toBeNull()
    expect(headShift(['a', 'b', 'c'], ['a', 'c'])).toBeNull()
    expect(headShift(['a', 'b', 'c'], ['b', 'x', 'c'])).toBeNull()
  })
  it('is null for an empty side', () => {
    expect(headShift([], ['a'])).toBeNull()
    expect(headShift(['a'], [])).toBeNull()
  })
  it('is null when the tail also changed', () => {
    expect(headShift(['a', 'b', 'c'], ['b'])).toBeNull()
  })
})
