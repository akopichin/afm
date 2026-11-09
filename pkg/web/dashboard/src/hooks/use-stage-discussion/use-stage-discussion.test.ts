import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { appendPage, emptyHistory, useStageDiscussion, withAnswersApplied, type DiscussionMessage } from './use-stage-discussion'
import * as runClient from '../../api/run-client'

type Page = Awaited<ReturnType<typeof runClient.listStageDiscussion>>
const page = (messages: Page['messages'], nextCursor: number): Page => ({ messages, nextCursor, hasMore: false })

describe('useStageDiscussion', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  test('reads the stage conversation; no stage — no requests', async () => {
    const list = vi.spyOn(runClient, 'listStageDiscussion').mockResolvedValue(page([{ id: 'm1', author_kind: 'human', round: 0, body: 'hi', created_at: '' }], 1))
    const { result, rerender } = renderHook(({ id }: { id: string | null }) => useStageDiscussion(id, false), { initialProps: { id: null as string | null } })
    expect(list).not.toHaveBeenCalled()
    rerender({ id: 'implement' })
    await waitFor(() => expect(result.current.map((m) => m.body)).toEqual(['hi']))
    expect(list).toHaveBeenCalledWith('implement', 0)
  })

  test('overlapping polls never read the same cursor twice, so no message is appended twice', async () => {
    vi.useFakeTimers()
    let resolveFirst!: (v: Page) => void
    const list = vi
      .spyOn(runClient, 'listStageDiscussion')
      .mockReturnValueOnce(new Promise((res) => (resolveFirst = res)))
      .mockResolvedValue(page([], 1))
    const { result } = renderHook(() => useStageDiscussion('implement', false))
    await act(async () => vi.advanceTimersByTime(9000)) // three ticks while the first read hangs
    expect(list).toHaveBeenCalledTimes(1)
    await act(async () => resolveFirst(page([{ id: 'm1', author_kind: 'human', round: 0, body: 'only once', created_at: '' }], 1)))
    await act(async () => vi.advanceTimersByTime(0))
    expect(list).toHaveBeenLastCalledWith('implement', 1)
    expect(result.current).toHaveLength(1)
  })

  test('when the stage finishes the log is read again from the start, so unanswered posts get marked', async () => {
    const list = vi.spyOn(runClient, 'listStageDiscussion').mockResolvedValue(page([{ id: 'm1', author_kind: 'human', round: 0, body: 'any news?', created_at: '' }], 1))
    const { result, rerender } = renderHook(({ finished }) => useStageDiscussion('implement', finished), { initialProps: { finished: false } })
    await waitFor(() => expect(result.current).toHaveLength(1))
    list.mockResolvedValue(page([{ id: 'm1', author_kind: 'human', round: 0, body: 'any news?', unanswered: true, created_at: '' }], 1))
    rerender({ finished: true })
    await waitFor(() => expect(result.current[0]?.unanswered).toBe(true))
    expect(result.current).toHaveLength(1)
    expect(list).toHaveBeenLastCalledWith('implement', 0)
  })

  test('a reply arriving later clears the "unanswered" mark of a post already read', async () => {
    vi.useFakeTimers()
    vi.spyOn(runClient, 'listStageDiscussion')
      .mockResolvedValueOnce(page([{ id: 'm1', author_kind: 'human', round: 0, body: 'any news?', unanswered: true, created_at: '' }], 1))
      .mockResolvedValue(page([{ id: 'm2', author_kind: 'assistant', author_id: 'run:t-1', round: 0, parent_message_id: 'm1', body: 'yes', created_at: '' }], 2))
    const { result } = renderHook(() => useStageDiscussion('implement', true))
    await act(async () => vi.advanceTimersByTime(0))
    expect(result.current[0]?.unanswered).toBe(true)
    await act(async () => vi.advanceTimersByTime(3000))
    expect(result.current.map((m) => [m.id, m.unanswered])).toEqual([
      ['m1', false],
      ['m2', undefined],
    ])
  })
})

describe('appendPage', () => {
  const msg = (id: string, authorKind: string, extra: Partial<DiscussionMessage> = {}): DiscussionMessage => ({ id, authorKind, round: 0, body: id, createdAt: '', ...extra })

  // Deterministic pseudo-random pages: human posts (some unanswered) and replies of every author kind to earlier posts.
  function generatePages(seed: number): DiscussionMessage[][] {
    let state = seed
    const next = (n: number) => (state = (state * 1103515245 + 12345) % 2147483648) % n
    const kinds = ['human', 'afm', 'assistant', 'main_agent']
    const humans: string[] = []
    let counter = 0
    return Array.from({ length: 6 }, () =>
      Array.from({ length: next(5) }, () => {
        const id = `m${counter++}`
        const authorKind = kinds[next(kinds.length)] as string
        if (authorKind === 'human') {
          humans.push(id)
          return msg(id, authorKind, { unanswered: next(2) === 0 })
        }
        return msg(id, authorKind, { parentMessageId: humans.length > 0 && next(3) > 0 ? humans[next(humans.length)] : undefined })
      }),
    )
  }

  test('gives the same messages as marking the whole history again', () => {
    for (let seed = 1; seed <= 50; seed++) {
      let history = emptyHistory()
      const all: DiscussionMessage[] = []
      for (const added of generatePages(seed)) {
        history = appendPage(history, added)
        all.push(...added)
        expect(history.messages).toEqual(withAnswersApplied(all))
      }
    }
  })

  test('a reply in the same page answers a post of that page; afm and assistant replies count, human ones do not', () => {
    const { messages } = appendPage(emptyHistory(), [
      msg('q1', 'human', { unanswered: true }),
      msg('q2', 'human', { unanswered: true }),
      msg('q3', 'human', { unanswered: true }),
      msg('a1', 'afm', { parentMessageId: 'q1' }),
      msg('a2', 'assistant', { parentMessageId: 'q2' }),
      msg('h1', 'human', { parentMessageId: 'q3' }),
    ])
    expect(messages.filter((m) => m.unanswered).map((m) => m.id)).toEqual(['q3'])
  })

  test('messages a new page does not touch keep their identity', () => {
    const first = appendPage(emptyHistory(), [msg('q1', 'human', { unanswered: true }), msg('q2', 'human', { unanswered: true })])
    const second = appendPage(first, [msg('a1', 'assistant', { parentMessageId: 'q1' })])
    expect(second.messages[0]?.unanswered).toBe(false)
    expect(second.messages[1]).toBe(first.messages[1])
  })

  test('a new answer does not read the history it does not answer', () => {
    let reads = 0
    const old = Array.from({ length: 1000 }, (_, i) => {
      const m = msg(`q${i}`, 'human', { unanswered: true })
      Object.defineProperty(m, 'unanswered', { get: () => (reads++, true), enumerable: true })
      return m
    })
    const first = appendPage(emptyHistory(), old)
    reads = 0
    const second = appendPage(first, [msg('a1', 'assistant', { parentMessageId: 'q5' })])
    expect(reads).toBeLessThanOrEqual(2)
    expect(second.messages[5]?.unanswered).toBe(false)
  })
})
