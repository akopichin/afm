import { describe, expect, test } from 'vitest'
import type { MeshThread, MeshThreadSummaries } from '../api/mesh-client'
import { orderInboxThreads } from './team-inbox'

const threads: MeshThread[] = ['empty-a', 'older', 'newer', 'empty-b', 'equal-a', 'equal-b'].map((id) => ({
  id, scope: 'run', members: ['user'], status: 'open',
}))

describe('orderInboxThreads', () => {
  test('uses message seq, keeps API order on ties and for empty threads, and leaves input untouched', () => {
    const summaries: MeshThreadSummaries = {
      older: { last_message: { id: 'm1', seq: 7, created_at: '2026-10-21T12:00:00Z', sender_id: 'user', preview: 'older' }, last_other_message_seq: 0, title_seq: 0 },
      newer: { last_message: { id: 'm2', seq: 9, created_at: '2026-10-20T12:00:00Z', sender_id: 'user', preview: 'newer' }, last_other_message_seq: 0, title_seq: 0 },
      'equal-a': { last_message: { id: 'm3', seq: 7, created_at: '2026-10-22T12:00:00Z', sender_id: 'user', preview: 'equal' }, last_other_message_seq: 0, title_seq: 0 },
      'equal-b': { last_message: { id: 'm4', seq: 7, created_at: '2026-10-22T12:00:00Z', sender_id: 'user', preview: 'equal' }, last_other_message_seq: 0, title_seq: 0 },
      'empty-b': { last_other_message_seq: 0, title_seq: 30 },
    }
    const result = orderInboxThreads(threads, summaries)
    expect(result.map((t) => t.id)).toEqual(['newer', 'older', 'equal-a', 'equal-b', 'empty-a', 'empty-b'])
    expect(result).not.toBe(threads)
    expect(threads.map((t) => t.id)).toEqual(['empty-a', 'older', 'newer', 'empty-b', 'equal-a', 'equal-b'])
    expect(orderInboxThreads(threads, { ...summaries, older: { ...summaries.older!, title_seq: 99 } }).map((t) => t.id)).toEqual(result.map((t) => t.id))
  })
})
