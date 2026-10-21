import { describe, expect, test } from 'vitest'
import { advanceTeamSeen, baselineTeamUnread, isTeamUnread } from './team-unread'

describe('team unread cursors', () => {
  test('marks a conversation while its latest incoming seq is after the page cursor', () => {
    const run = 'unread-last-other'
    baselineTeamUnread(run, { a: { last_other_message_seq: 4, title_seq: 0 } })
    expect(isTeamUnread(run, 'a', 10)).toBe(true)
    advanceTeamSeen(run, 'a', 7)
    expect(isTeamUnread(run, 'a', 10)).toBe(true)
    advanceTeamSeen(run, 'a', 8)
    expect(isTeamUnread(run, 'a', 10)).toBe(true)
    // An own send at seq 11 does not change last_other_message_seq.
    expect(isTeamUnread(run, 'a', 10)).toBe(true)
    advanceTeamSeen(run, 'a', 10)
    expect(isTeamUnread(run, 'a', 10)).toBe(false)
  })
  test('baselines only once per run and survives a later view mount', () => {
    const run = 'unread-baseline-once'
    baselineTeamUnread(run, { a: { last_other_message_seq: 4, title_seq: 0 } })
    expect(isTeamUnread(run, 'a', 4)).toBe(false)
    baselineTeamUnread(run, { a: { last_other_message_seq: 6, title_seq: 0 } })
    expect(isTeamUnread(run, 'a', 6)).toBe(true)
    advanceTeamSeen(run, 'a', 5)
    expect(isTeamUnread(run, 'a', 6)).toBe(true)
    advanceTeamSeen(run, 'a', 6)
    expect(isTeamUnread(run, 'a', 6)).toBe(false)
    baselineTeamUnread('unread-new-run', { a: { last_other_message_seq: 6, title_seq: 0 } })
    expect(isTeamUnread('unread-new-run', 'a', 6)).toBe(false)
  })

  test('advancing is monotone and ignores invalid run ids', () => {
    const run = 'unread-monotone'
    baselineTeamUnread(run, { a: { last_other_message_seq: 1, title_seq: 0 } })
    advanceTeamSeen(run, 'a', 5)
    advanceTeamSeen(run, 'a', 2)
    expect(isTeamUnread(run, 'a', 4)).toBe(false)
    expect(isTeamUnread(run, 'a', 6)).toBe(true)
    baselineTeamUnread('', { a: { last_other_message_seq: 5, title_seq: 0 } })
    expect(isTeamUnread('', 'a', 6)).toBe(false)
  })

  test('a thread arriving after baseline is marked new and local reading leaves summaries unchanged', () => {
    const run = 'unread-late-thread'
    baselineTeamUnread(run, {})
    const summaries = { later: { last_other_message_seq: 7, title_seq: 0 } }
    expect(isTeamUnread(run, 'later', summaries.later.last_other_message_seq)).toBe(true)
    advanceTeamSeen(run, 'later', 7)
    expect(isTeamUnread(run, 'later', summaries.later.last_other_message_seq)).toBe(false)
    expect(summaries.later.last_other_message_seq).toBe(7)
  })
})
