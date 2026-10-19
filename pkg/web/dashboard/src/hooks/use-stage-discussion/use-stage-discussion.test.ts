import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { useStageDiscussion } from './use-stage-discussion'
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
