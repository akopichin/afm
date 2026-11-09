import { describe, expect, test } from 'vitest'
import type { AfmEvent } from '../../types'
import { EventBuffer, dedupeKey, feedDedupeKey, ingestKey, mergeCapped } from './event-buffer'

function event(type: string, payload: unknown = null, stageId = 's1', seq?: number): AfmEvent {
  return { type, payload, stageId, timestamp: '2026-11-09T10:00:00Z', seq }
}

function note(id: string): AfmEvent {
  return event('agent_note', { id, text: 'hello' })
}

describe('EventBuffer', () => {
  test('flush publishes queued events in order as one new array, then nothing', () => {
    const buffer = new EventBuffer(10)
    const a = event('agent_action', 'a')
    const b = event('agent_action', 'b')

    expect(buffer.push(a)).toBe(true)
    expect(buffer.push(b)).toBe(true)

    const first = buffer.flush()
    expect(first).toEqual([a, b])
    expect(buffer.flush()).toBeNull()

    buffer.push(event('agent_action', 'c'))
    const second = buffer.flush()
    expect(second).toHaveLength(3)
    expect(second).not.toBe(first)
    expect(first).toHaveLength(2)
  })

  test('keeps only the last cap events', () => {
    const buffer = new EventBuffer(3)
    for (let i = 0; i < 5; i++) buffer.push(event('agent_action', i))

    expect(buffer.flush()?.map((e) => e.payload)).toEqual([2, 3, 4])
  })

  test('a retry of an evicted note is accepted before the next flush', () => {
    const buffer = new EventBuffer(2)
    buffer.push(note('N'))
    buffer.flush()

    buffer.push(note('X'))
    buffer.push(note('Y'))
    expect(buffer.push(note('N'))).toBe(true)

    expect(buffer.flush()?.map((e) => (e.payload as { id: string }).id)).toEqual(['Y', 'N'])
  })

  test('collapses a repeated status change against the queue tail and the window tail', () => {
    const buffer = new EventBuffer(10)

    expect(buffer.push(event('stage_status_changed', 'ready'))).toBe(true)
    expect(buffer.push(event('stage_status_changed', 'ready'))).toBe(false)
    buffer.flush()
    expect(buffer.push(event('stage_status_changed', 'ready'))).toBe(false)
    expect(buffer.push(event('stage_status_changed', 'running'))).toBe(true)
    expect(buffer.push(event('stage_status_changed', 'running', 'other'))).toBe(true)
  })

  test('drops a repeated ingest-deduped event in the queue and in the window', () => {
    const buffer = new EventBuffer(10)

    expect(buffer.push(note('n1'))).toBe(true)
    expect(buffer.push(note('n1'))).toBe(false)
    buffer.flush()
    expect(buffer.push(note('n1'))).toBe(false)
    expect(buffer.push(note('n2'))).toBe(true)
  })

  test('does not dedupe agent_action repeats', () => {
    const buffer = new EventBuffer(10)

    expect(buffer.push(event('agent_action', 'same'))).toBe(true)
    expect(buffer.push(event('agent_action', 'same'))).toBe(true)
  })

  test('accepts an ingest key again after its event is evicted from the window', () => {
    const buffer = new EventBuffer(2)
    buffer.push(note('n1'))
    buffer.push(event('agent_action', 'x'))
    buffer.flush()
    buffer.push(event('agent_action', 'y'))
    buffer.flush()

    expect(buffer.push(note('n1'))).toBe(true)
  })

  test('discardPending forgets the queue and its dedupe keys but keeps the window', () => {
    const buffer = new EventBuffer(10)
    buffer.push(event('agent_action', 'kept'))
    buffer.flush()
    buffer.push(note('n1'))
    buffer.discardPending()

    expect(buffer.flush()).toBeNull()
    expect(buffer.push(note('n1'))).toBe(true)
    expect(buffer.flush()).toHaveLength(2)
  })

  test('clear empties the window and the queue', () => {
    const buffer = new EventBuffer(10)
    buffer.push(note('n1'))
    buffer.flush()
    buffer.push(event('agent_action', 'x'))
    buffer.clear()

    expect(buffer.flush()).toBeNull()
    expect(buffer.push(note('n1'))).toBe(true)
  })

  test('merge keeps history first, appends live-only, and includes the queue', () => {
    const buffer = new EventBuffer(10)
    const live1 = event('agent_action', 'live1')
    buffer.push(live1)
    buffer.flush()
    const queued = event('agent_action', 'queued')
    buffer.push(queued)
    const history = [event('approved', null, 's1', 1), event('agent_note', { id: 'n1' }, 's1', 2)]

    const merged = buffer.merge(history)

    expect(merged.events).toEqual([...history, live1, queued])
    expect(buffer.flush()).toBeNull()
  })

  test('merge rebuilds the ingest index so history notes block live repeats', () => {
    const buffer = new EventBuffer(10)

    buffer.merge([event('agent_note', { id: 'n1', text: 'hello' })])

    expect(buffer.push(note('n1'))).toBe(false)
  })

  test('merge reports a significant history event the window did not have', () => {
    const buffer = new EventBuffer(10)
    const known = event('approved', null, 's1', 1)
    buffer.push(known)
    buffer.flush()

    expect(buffer.merge([known]).hasNewSignificant).toBe(false)
    expect(buffer.merge([known, event('agent_action', 'x', 's1', 2)]).hasNewSignificant).toBe(false)
    expect(buffer.merge([known, event('approved', null, 's1', 3)]).hasNewSignificant).toBe(true)
  })

  describe('arrivals', () => {
    test('counts accepted events per stage and for mesh events, ignoring collapsed repeats', () => {
      const buffer = new EventBuffer(10)
      buffer.push(event('agent_action', 'a', 's1'))
      buffer.push(event('agent_action', 'b', 's2'))
      buffer.push(event('mesh_board', 'x', ''))
      buffer.push(note('N'))
      expect(buffer.push(note('N'))).toBe(false)
      buffer.push(event('stage_status_changed', 'ready', 's1'))
      expect(buffer.push(event('stage_status_changed', 'ready', 's1'))).toBe(false)

      expect(buffer.arrivals()).toEqual({ stages: new Map([['s1', 3], ['s2', 1], ['', 1]]), mesh: 1 })
    })

    test('never decreases when the window evicts old events, and a history merge adds nothing', () => {
      const buffer = new EventBuffer(2)
      for (let i = 0; i < 5; i++) buffer.push(event('agent_action', i, 's1', i + 1))
      buffer.flush()
      buffer.merge([event('agent_action', 'old', 's1', 100), event('agent_action', 3, 's1', 4)])

      expect(buffer.arrivals().stages.get('s1')).toBe(5)
    })

    test('clear starts the counters over', () => {
      const buffer = new EventBuffer(2)
      buffer.push(event('mesh_board', 'x', 's1'))
      buffer.clear()

      expect(buffer.arrivals()).toEqual({ stages: new Map(), mesh: 0 })
    })

    test('returns a snapshot that later pushes do not change', () => {
      const buffer = new EventBuffer(5)
      buffer.push(event('agent_action', 'a', 's1'))
      const before = buffer.arrivals()
      buffer.push(event('agent_action', 'b', 's1'))

      expect(before.stages.get('s1')).toBe(1)
    })

    test('stage ids that are Object.prototype names count like any other', () => {
      const buffer = new EventBuffer(5)
      for (const id of ['constructor', '__proto__', 'toString']) buffer.push(event('agent_action', id, id))

      for (const id of ['constructor', '__proto__', 'toString']) expect(buffer.arrivals().stages.get(id)).toBe(1)
    })
  })
})

describe('event keys', () => {
  test('feedDedupeKey is stable for one event object and matches dedupeKey outside the dialog family', () => {
    const e = event('agent_action', { a: 1 })

    expect(feedDedupeKey(e)).toBe(dedupeKey(e))
    expect(feedDedupeKey(e)).toBe(feedDedupeKey(e))
  })

  test('ingestKey is set only for the ingest-deduped families', () => {
    expect(ingestKey(event('agent_note', { id: 'n' }))).not.toBeNull()
    expect(ingestKey(event('script_failed', 'x'))).not.toBeNull()
    expect(ingestKey(event('dialog_question', { id: 'q' }))).not.toBeNull()
    expect(ingestKey(event('auto_answered', { dialog: true, id: 'q' }))).not.toBeNull()
    expect(ingestKey(event('auto_answered', { id: 'q' }))).toBeNull()
    expect(ingestKey(event('agent_action', 'x'))).toBeNull()
  })
})

const statusOf = (e: AfmEvent) => (e.payload as { status: string }).status

describe('mergeCapped positional merge', () => {
  const seqEvents = (from: number, to: number) =>
    Array.from({ length: to - from + 1 }, (_, i) => event('agent_action', null, 's1', from + i))
  const plainEvents = (from: number, to: number) =>
    Array.from({ length: to - from + 1 }, (_, i) => event('agent_action', { n: from + i }))
  const isIncreasing = (nums: number[]) => nums.every((n, i) => i === 0 || n > (nums[i - 1] as number))

  test('live events older than the first history-covered one go before history (seq)', () => {
    const merged = mergeCapped(seqEvents(901, 3300), seqEvents(601, 3300), 10000)
    const seqs = merged.map((e) => e.seq as number)
    expect(seqs).toHaveLength(2700)
    expect(seqs[0]).toBe(601)
    expect(seqs[2699]).toBe(3300)
    expect(isIncreasing(seqs)).toBe(true)
  })

  test('same for events without seq', () => {
    const merged = mergeCapped(plainEvents(901, 3300), plainEvents(601, 3300), 10000)
    const ns = merged.map((e) => (e.payload as { n: number }).n)
    expect(ns).toHaveLength(2700)
    expect(isIncreasing(ns)).toBe(true)
  })

  test('cap keeps the newest events of the ordered result', () => {
    const merged = mergeCapped(seqEvents(901, 3300), seqEvents(601, 3300), 1000)
    expect(merged.map((e) => e.seq)).toEqual(seqEvents(2301, 3300).map((e) => e.seq))
  })

  test('live-only events after the first covered one stay after history', () => {
    const merged = mergeCapped(seqEvents(1, 10), seqEvents(8, 12), 100)
    expect(merged.map((e) => e.seq)).toEqual(seqEvents(1, 12).map((e) => e.seq))
  })

  test('without any overlap all live events are treated as newer', () => {
    const merged = mergeCapped(seqEvents(1, 3), seqEvents(10, 12), 100)
    expect(merged.map((e) => e.seq)).toEqual([1, 2, 3, 10, 11, 12])
  })

  describe('stable anchors', () => {
    const status = () => event('stage_status_changed', { status: 'running' }, 's1', 1000000)
    const statusPlain = () => event('stage_status_changed', { status: 'running' })
    const seqOf = (events: AfmEvent[]) => events.filter((e) => e.type === 'agent_action').map((e) => e.seq as number)
    const nOf = (events: AfmEvent[]) =>
      events.filter((e) => e.type === 'agent_action').map((e) => (e.payload as { n: number }).n)

    test('an old shared FSM event does not push older live actions after history (seq)', () => {
      const shared = status()
      const merged = mergeCapped([shared, ...seqEvents(901, 3300)], [shared, ...seqEvents(1, 3300)], 10000)
      const seqs = seqOf(merged)
      expect(seqs).toHaveLength(3300)
      expect(isIncreasing(seqs)).toBe(true)
    })

    test('same for events without seq', () => {
      const shared = statusPlain()
      const merged = mergeCapped([shared, ...plainEvents(901, 3300)], [shared, ...plainEvents(1, 3300)], 10000)
      const ns = nOf(merged)
      expect(ns).toHaveLength(3300)
      expect(isIncreasing(ns)).toBe(true)
    })

    test('a live-only run between two anchors lands between them', () => {
      const a = event('agent_action', null, 's1', 1)
      const b = event('agent_action', null, 's1', 4)
      const between = [event('agent_action', null, 's1', 2), event('agent_action', null, 's1', 3)]
      const merged = mergeCapped([a, b], [a, ...between, b], 100)
      expect(merged.map((e) => e.seq)).toEqual([1, 2, 3, 4])
    })

    describe('F16: outlier anchors', () => {
      const S = () => event('stage_status_changed', { status: 'running' })
      const orderOf = (events: AfmEvent[]) =>
        events.map((e) => (e.type === 'stage_status_changed' ? statusOf(e) : (e.payload as { n: number }).n))
      const act = (n: number) => event('agent_action', { n })

      test('a seq-less status high in history but first in live does not drag older live actions', () => {
        const h = [...plainEvents(901, 3212), S(), ...plainEvents(3213, 3300)]
        const l = [S(), ...plainEvents(232, 900), ...plainEvents(901, 3300)]
        const expected = [...plainEvents(232, 3212), S(), ...plainEvents(3213, 3300)]
        expect(orderOf(mergeCapped(h, l, 10000))).toEqual(orderOf(expected))
        expect(orderOf(mergeCapped(h, l, 1000))).toEqual(orderOf(expected).slice(-1000))
      })

      test('F15 repro: outlier status in live keeps live-only runs after history', () => {
        const h = [...plainEvents(1004, 2964), S(), ...plainEvents(2965, 3000)]
        const l = [...plainEvents(1304, 3000), ...plainEvents(3001, 3193), S(), ...plainEvents(3194, 3300)]
        const expected = [...plainEvents(1004, 2964), S(), ...plainEvents(2965, 3300)]
        expect(orderOf(mergeCapped(h, l, 10000))).toEqual(orderOf(expected))
        expect(orderOf(mergeCapped(h, l, 1000))).toEqual(orderOf(expected).slice(-1000))
      })

      test('two outliers, one on each side, are ignored', () => {
        const a = event('stage_status_changed', { status: 'a' })
        const b = event('stage_status_changed', { status: 'b' })
        const h = [b, act(1), act(2), act(3), a]
        const l = [a, act(0), act(1), act(2), act(9), act(3), b, act(10)]
        expect(orderOf(mergeCapped(h, l, 100))).toEqual(['b', 0, 1, 2, 9, 3, 10, 'a'])
      })

      test('F17: a single outlier anchor is not trusted, live-only events go to the tail', () => {
        const h = [...plainEvents(1004, 2964), S(), ...plainEvents(2965, 3000)]
        const l = [...plainEvents(3002, 3193), S(), ...plainEvents(3194, 5000)]
        const expected = [...plainEvents(1004, 2964), S(), ...plainEvents(2965, 3000), ...plainEvents(3002, 5000)]
        expect(orderOf(mergeCapped(h, l, 10000))).toEqual(orderOf(expected))
        expect(orderOf(mergeCapped(h, l, 2000))).toEqual(orderOf(expected).slice(-2000))
      })

      describe('F18: statuses are never anchors', () => {
        const S1 = () => event('stage_status_changed', { status: 'paused' })
        const S2 = () => event('stage_status_changed', { status: 'running' })
        const history = () => [...plainEvents(1003, 2964), S1(), S2(), ...plainEvents(2965, 3000)]
        const live = () => [...plainEvents(3003, 3193), S1(), S2(), ...plainEvents(3194, 5000)]
        const expected = () => [...history(), ...plainEvents(3003, 5000)]

        test('two shared statuses do not form a trusted chain', () => {
          expect(orderOf(mergeCapped(history(), live(), 2000))).toEqual(orderOf(expected()).slice(-2000))
        })

        test('EventBuffer.merge gives the same order', () => {
          const buffer = new EventBuffer()
          for (const e of live()) buffer.push(e)
          expect(orderOf(buffer.merge(history()).events)).toEqual(orderOf(expected()).slice(-10000))
        })

        test('status outliers do not break a genuine action chain', () => {
          const a = S1()
          const b = S2()
          const h = [b, act(1), act(2), act(3), a]
          const l = [a, act(0), act(1), b, act(2), act(9), act(3), act(10)]
          expect(orderOf(mergeCapped(h, l, 100))).toEqual(['running', 0, 1, 2, 9, 3, 10, 'paused'])
        })
      })

      describe('F19: a single genuine action anchor is trusted', () => {
        const history = () => plainEvents(1001, 3000)
        const live = () => plainEvents(1, 1001)

        test('older live runs go before the lone anchor', () => {
          expect(orderOf(mergeCapped(history(), live(), 2000))).toEqual(orderOf(plainEvents(1001, 3000)))
        })

        test('EventBuffer.merge at the global cap keeps the older live run', () => {
          const buffer = new EventBuffer(3000)
          for (const e of live()) buffer.push(e)
          expect(orderOf(buffer.merge(history()).events)).toEqual(orderOf(plainEvents(1, 3000)))
        })
      })

      describe('F20: live-only run after the last anchor goes to the tail', () => {
        test('recovered history suffix stays before a newer live event', () => {
          expect(orderOf(mergeCapped(plainEvents(1, 10), [act(1), act(2), act(11)], 100))).toEqual(
            orderOf(plainEvents(1, 11)),
          )
        })

        test('at the cap the newest live events are kept', () => {
          const merged = mergeCapped(plainEvents(1, 2000), [act(1), act(2001), act(2002)], 2000)
          expect(orderOf(merged)).toEqual(orderOf(plainEvents(3, 2002)))
        })
      })

      test('duplicate seq-less keys do not crash', () => {
        const h = [S(), act(1)]
        const l = [S(), S(), act(1), act(2)]
        expect(orderOf(mergeCapped(h, l, 100))).toEqual(['running', 1, 2])
      })

      test('EventBuffer.merge gives the F16 order', () => {
        const buffer = new EventBuffer()
        for (const e of [S(), ...plainEvents(232, 900), ...plainEvents(901, 3300)]) buffer.push(e)
        const h = [...plainEvents(901, 3212), S(), ...plainEvents(3213, 3300)]
        const expected = [...plainEvents(232, 3212), S(), ...plainEvents(3213, 3300)]
        expect(orderOf(buffer.merge(h).events)).toEqual(orderOf(expected))
      })
    })

    describe('F15: out-of-order seq-less anchor', () => {
      const S = () => event('stage_status_changed', { status: 'running' })
      const orderOf = (events: AfmEvent[]) =>
        events.map((e) => (e.type === 'stage_status_changed' ? 'S' : (e.payload as { n: number }).n))
      const history = () => [...plainEvents(1004, 2964), S(), ...plainEvents(2965, 3000)]
      const expected = () => [...plainEvents(1004, 2964), S(), ...plainEvents(2965, 3300)]

      test('mergeCapped keeps history order and appends live-only runs after history position', () => {
        const live = [...plainEvents(1304, 3000), ...plainEvents(3001, 3193), S(), ...plainEvents(3194, 3300)]
        expect(orderOf(mergeCapped(history(), live, 10000))).toEqual(orderOf(expected()))
      })

      test('EventBuffer.merge gives the same order', () => {
        const buffer = new EventBuffer()
        for (const e of [...plainEvents(1304, 3000), ...plainEvents(3001, 3193), S(), ...plainEvents(3194, 3300)]) buffer.push(e)
        expect(orderOf(buffer.merge(history()).events)).toEqual(orderOf(expected()))
      })

      test('two out-of-order anchors do not pull runs backwards', () => {
        const a = event('stage_status_changed', { status: 'a' })
        const b = event('stage_status_changed', { status: 'b' })
        const h = [a, b, event('agent_action', { n: 1 }), event('agent_action', { n: 2 })]
        const l = [event('agent_action', { n: 1 }), b, event('agent_action', { n: 2 }), event('agent_action', { n: 3 }), a, event('agent_action', { n: 4 })]
        const merged = mergeCapped(h, l, 100)
        expect(merged.map((e) => (e.type === 'stage_status_changed' ? statusOf(e) : (e.payload as { n: number }).n))).toEqual(['a', 'b', 1, 2, 3, 4])
      })
    })
  })
})
