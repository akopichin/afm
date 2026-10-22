import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import type { MeshThread, MeshThreadSummaries } from '../../api/mesh-client'
import { RUN_CONVERSATIONS_SEEN_KEY, useRunConversationsSeen } from './run-conversations-seen'

const threads: MeshThread[] = [
  { id: 'a', scope: 'run', members: ['user', 'run:qa'], status: 'open' },
  { id: 'b', scope: 'run', members: ['user', 'side:r1'], status: 'open' },
]
const summaries = (a: number, b: number): MeshThreadSummaries => ({
  a: { last_other_message_seq: a, last_agent_to_user_message_seq: a, title_seq: 0 },
  b: { last_other_message_seq: b, last_agent_to_user_message_seq: b, title_seq: 0 },
})

describe('useRunConversationsSeen', () => {
  beforeEach(() => localStorage.clear())
  afterEach(() => vi.restoreAllMocks())

  test('with no stored cursor every thread with an agent-to-user message is unread', () => {
    const { result } = renderHook(() => useRunConversationsSeen('r1', threads, summaries(4, 0)))
    expect([...result.current.unreadThreadIds]).toEqual(['a'])
  })

  test('only the agent-to-user cursor counts: other-sender chatter alone raises nothing', () => {
    const chatter: MeshThreadSummaries = { a: { last_other_message_seq: 9, last_agent_to_user_message_seq: 0, title_seq: 0 } }
    const { result } = renderHook(() => useRunConversationsSeen('r1', threads, chatter))
    expect(result.current.unreadThreadIds.size).toBe(0)
  })

  test('a summary from an older backend without the field raises nothing', () => {
    const old: MeshThreadSummaries = { a: { last_other_message_seq: 9, title_seq: 0 } }
    const { result } = renderHook(() => useRunConversationsSeen('r1', threads, old))
    expect(result.current.unreadThreadIds.size).toBe(0)
  })

  test('markSeen clears a thread, never lowers the cursor and persists it per run', () => {
    const { result } = renderHook(() => useRunConversationsSeen('r1', threads, summaries(4, 6)))
    act(() => result.current.markSeen('a', 4))
    expect([...result.current.unreadThreadIds]).toEqual(['b'])
    act(() => result.current.markSeen('a', 2))
    expect(result.current.seenSeqs.a).toBe(4)
    expect(JSON.parse(localStorage.getItem(RUN_CONVERSATIONS_SEEN_KEY) ?? '{}')).toEqual({ r1: { a: 4 } })
  })

  test('saving one thread keeps what another tab already stored for other threads and for the same one', () => {
    const { result } = renderHook(() => useRunConversationsSeen('r1', threads, summaries(4, 6)))
    // Another tab persisted these after this hook loaded.
    localStorage.setItem(RUN_CONVERSATIONS_SEEN_KEY, JSON.stringify({ r1: { a: 7, b: 9 }, r2: { a: 1 } }))
    act(() => result.current.markSeen('b', 6))
    act(() => result.current.markSeen('c', 3))
    expect(JSON.parse(localStorage.getItem(RUN_CONVERSATIONS_SEEN_KEY) ?? '{}')).toEqual({ r1: { a: 7, b: 9, c: 3 }, r2: { a: 1 } })
  })

  test('the cursor survives a reload (a fresh hook reads it back) but belongs to its run', () => {
    const first = renderHook(() => useRunConversationsSeen('r1', threads, summaries(4, 0)))
    act(() => first.result.current.markSeen('a', 4))
    first.unmount()
    const reloaded = renderHook(() => useRunConversationsSeen('r1', threads, summaries(4, 0)))
    expect(reloaded.result.current.unreadThreadIds.size).toBe(0)
    const otherRun = renderHook(() => useRunConversationsSeen('r2', threads, summaries(4, 0)))
    expect([...otherRun.result.current.unreadThreadIds]).toEqual(['a'])
  })

  test('a newer message after the cursor is unread again', () => {
    localStorage.setItem(RUN_CONVERSATIONS_SEEN_KEY, JSON.stringify({ r1: { a: 4 } }))
    const { result, rerender } = renderHook(({ s }) => useRunConversationsSeen('r1', threads, s), { initialProps: { s: summaries(4, 0) } })
    expect(result.current.unreadThreadIds.size).toBe(0)
    rerender({ s: summaries(7, 0) })
    expect([...result.current.unreadThreadIds]).toEqual(['a'])
  })

  test('corrupt stored data starts every cursor at zero', () => {
    localStorage.setItem(RUN_CONVERSATIONS_SEEN_KEY, '{not json')
    const { result } = renderHook(() => useRunConversationsSeen('r1', threads, summaries(4, 0)))
    expect([...result.current.unreadThreadIds]).toEqual(['a'])
    localStorage.setItem(RUN_CONVERSATIONS_SEEN_KEY, JSON.stringify({ r1: { a: 'x', b: -3 } }))
    const second = renderHook(() => useRunConversationsSeen('r1', threads, summaries(4, 1)))
    expect([...second.result.current.unreadThreadIds].sort()).toEqual(['a', 'b'])
  })

  test('unavailable storage still tracks the cursor for this page', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('denied') })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('full') })
    const { result } = renderHook(() => useRunConversationsSeen('r1', threads, summaries(4, 0)))
    expect([...result.current.unreadThreadIds]).toEqual(['a'])
    act(() => result.current.markSeen('a', 4))
    expect(result.current.unreadThreadIds.size).toBe(0)
  })

  test('ignores threads that are not in the given list', () => {
    const { result } = renderHook(() => useRunConversationsSeen('r1', [threads[0]!], summaries(4, 6)))
    expect([...result.current.unreadThreadIds]).toEqual(['a'])
  })
})
