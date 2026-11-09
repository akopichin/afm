import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { MAX_STAGE_MESSAGES, MAX_STAGE_THREADS, useStageConversation } from './use-stage-conversation'
import * as meshClient from '../../api/mesh-client'

function msg(id: string, seq: number, threadId = 'mt-1'): meshClient.MeshMessage {
  return { id, thread_id: threadId, sender_id: 'run:qa', recipient_ids: ['user'], kind: 'message', body: id, seq, created_at: '2026-10-19T10:00:00Z' }
}
const thread = (id: string): meshClient.MeshThread => ({ id, scope: 'run', members: ['user', 'run:qa'], status: 'open' })

describe('useStageConversation', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('no stage: no network call, nothing to show', () => {
    const spy = vi.spyOn(meshClient, 'getStageMessages')
    const { result } = renderHook(() => useStageConversation(null))
    expect(result.current.messages).toEqual([])
    expect(spy).not.toHaveBeenCalled()
  })

  test('walks every page from the cursor and keeps the threads of the messages', async () => {
    const spy = vi.spyOn(meshClient, 'getStageMessages')
    spy.mockResolvedValueOnce({ messages: [msg('m1', 3)], threads: [thread('mt-1')], next_cursor: 3, has_more: true })
    spy.mockResolvedValueOnce({ messages: [msg('m2', 5, 'mt-2')], threads: [thread('mt-2')], next_cursor: 5, has_more: false })
    spy.mockResolvedValue({ messages: [], threads: [], next_cursor: 5, has_more: false })

    const { result } = renderHook(() => useStageConversation('impl'))

    await waitFor(() => expect(result.current.messages.map((m) => m.id)).toEqual(['m1', 'm2']))
    expect(spy).toHaveBeenNthCalledWith(1, 'impl', 0, 100)
    expect(spy).toHaveBeenNthCalledWith(2, 'impl', 3, 100)
    expect(Object.keys(result.current.threads).sort()).toEqual(['mt-1', 'mt-2'])
  })

  test('a later poll appends only what is new, and a message seen twice is shown once', async () => {
    const spy = vi.spyOn(meshClient, 'getStageMessages')
    spy.mockResolvedValueOnce({ messages: [msg('m1', 3)], threads: [thread('mt-1')], next_cursor: 3, has_more: false })
    spy.mockResolvedValue({ messages: [msg('m1', 3), msg('m2', 4)], threads: [thread('mt-1')], next_cursor: 4, has_more: false })

    const { result } = renderHook(() => useStageConversation('impl'))
    await waitFor(() => expect(result.current.messages).toHaveLength(1))
    await waitFor(() => expect(result.current.messages.map((m) => m.id)).toEqual(['m1', 'm2']), { timeout: 5000 })
    expect(spy).toHaveBeenCalledWith('impl', 3, 100)
  })

  test('another stage starts over', async () => {
    const spy = vi.spyOn(meshClient, 'getStageMessages')
    spy.mockResolvedValueOnce({ messages: [msg('m1', 3)], threads: [thread('mt-1')], next_cursor: 3, has_more: false })
    const { result, rerender } = renderHook(({ id }: { id: string }) => useStageConversation(id), { initialProps: { id: 'impl' } })
    await waitFor(() => expect(result.current.messages).toHaveLength(1))

    spy.mockResolvedValue({ messages: [], threads: [], next_cursor: 0, has_more: false })
    rerender({ id: 'docs' })
    expect(result.current.messages).toEqual([])
    await waitFor(() => expect(spy).toHaveBeenCalledWith('docs', 0, 100))
  })

  test('a thread closed without a new message updates; threads not sent again stay', async () => {
    const spy = vi.spyOn(meshClient, 'getStageMessages')
    spy.mockResolvedValueOnce({ messages: [msg('m1', 3), msg('m2', 4, 'mt-2')], threads: [thread('mt-1'), thread('mt-2')], next_cursor: 4, has_more: false })
    spy.mockResolvedValueOnce({ messages: [], threads: [{ ...thread('mt-1'), status: 'closed' }], next_cursor: 6, has_more: false })
    spy.mockResolvedValue({ messages: [], threads: [], next_cursor: 6, has_more: false })

    const { result } = renderHook(() => useStageConversation('impl'))
    await waitFor(() => expect(result.current.threads['mt-1']?.status).toBe('open'))
    await waitFor(() => expect(result.current.threads['mt-1']?.status).toBe('closed'), { timeout: 5000 })
    expect(spy).toHaveBeenCalledWith('impl', 4, 100)
    expect(result.current.threads['mt-2']?.status).toBe('open')
    expect(result.current.messages.map((m) => m.id)).toEqual(['m1', 'm2'])
  })

  test('keeps only the MAX_STAGE_THREADS most recently changed threads', async () => {
    const spy = vi.spyOn(meshClient, 'getStageMessages')
    const many = Array.from({ length: MAX_STAGE_THREADS + 2 }, (_, i) => thread(`mt-${i}`))
    spy.mockResolvedValueOnce({ messages: [], threads: many, next_cursor: 10, has_more: false })
    spy.mockResolvedValueOnce({ messages: [], threads: [{ ...thread('mt-0'), status: 'closed' }], next_cursor: 11, has_more: false })
    spy.mockResolvedValue({ messages: [], threads: [], next_cursor: 11, has_more: false })

    const { result } = renderHook(() => useStageConversation('impl'))
    await waitFor(() => expect(Object.keys(result.current.threads)).toHaveLength(MAX_STAGE_THREADS))
    expect(result.current.threads['mt-0']).toBeUndefined()
    await waitFor(() => expect(result.current.threads['mt-0']?.status).toBe('closed'), { timeout: 5000 })
    expect(Object.keys(result.current.threads)).toHaveLength(MAX_STAGE_THREADS)
    expect(result.current.threads['mt-2']).toBeUndefined()
  })

  test('keeps only the newest MAX_STAGE_MESSAGES messages', async () => {
    const spy = vi.spyOn(meshClient, 'getStageMessages')
    const many = Array.from({ length: MAX_STAGE_MESSAGES + 5 }, (_, i) => msg(`m${i}`, i + 1))
    spy.mockResolvedValueOnce({ messages: many, threads: [thread('mt-1')], next_cursor: many.length, has_more: false })
    spy.mockResolvedValue({ messages: [], threads: [thread('mt-1')], next_cursor: many.length, has_more: false })

    const { result } = renderHook(() => useStageConversation('impl'))
    await waitFor(() => expect(result.current.messages).toHaveLength(MAX_STAGE_MESSAGES))
    expect(result.current.messages[0]?.id).toBe('m5')
    expect(result.current.messages.at(-1)?.id).toBe(`m${MAX_STAGE_MESSAGES + 4}`)
  })

  test('counts arrivals after the first read, not the history, and keeps counting at the cap', async () => {
    const spy = vi.spyOn(meshClient, 'getStageMessages')
    const full = Array.from({ length: MAX_STAGE_MESSAGES }, (_, i) => msg(`m${i}`, i + 1))
    spy.mockResolvedValueOnce({ messages: full, threads: [thread('mt-1')], next_cursor: full.length, has_more: false })
    spy.mockResolvedValue({ messages: [msg('new', full.length + 1)], threads: [thread('mt-1')], next_cursor: full.length + 1, has_more: false })

    const { result } = renderHook(() => useStageConversation('impl'))
    await waitFor(() => expect(result.current.messages).toHaveLength(MAX_STAGE_MESSAGES))
    expect(result.current.arrived).toBe(0)
    await waitFor(() => expect(result.current.arrived).toBe(1), { timeout: 5000 })
    expect(result.current.messages).toHaveLength(MAX_STAGE_MESSAGES)
    expect(result.current.messages[0]?.id).toBe('m1')
  })

  test('another stage restarts the arrival count', async () => {
    const spy = vi.spyOn(meshClient, 'getStageMessages')
    spy.mockResolvedValueOnce({ messages: [msg('m1', 3)], threads: [thread('mt-1')], next_cursor: 3, has_more: false })
    spy.mockResolvedValueOnce({ messages: [msg('m2', 4)], threads: [thread('mt-1')], next_cursor: 4, has_more: false })
    const { result, rerender } = renderHook(({ id }: { id: string }) => useStageConversation(id), { initialProps: { id: 'impl' } })
    await waitFor(() => expect(result.current.arrived).toBe(1), { timeout: 5000 })

    spy.mockResolvedValue({ messages: [], threads: [], next_cursor: 0, has_more: false })
    rerender({ id: 'docs' })
    expect(result.current.arrived).toBe(0)
  })
  describe('coming back to a stage whose first read has not finished', () => {
    const history = (count: number) => Array.from({ length: count }, (_, i) => msg(`m${i}`, i + 1))
    const ids = (messages: meshClient.MeshMessage[]) => messages.map((m) => m.id)

    function renderWithTrace() {
      const arrivedTrace: number[] = []
      const view = renderHook(({ id }: { id: string }) => {
        const conversation = useStageConversation(id)
        arrivedTrace.push(conversation.arrived)
        return conversation
      }, { initialProps: { id: 's1' } })
      return { ...view, arrivedTrace }
    }

    // The counter of one visit may only stay or grow: [0, 1, 0, 1] is a flicker and must fail.
    function expectNeverDecreases(trace: number[]) {
      trace.slice(1).forEach((n, i) => expect(n).toBeGreaterThanOrEqual(trace[i] as number))
    }

    test('a read that never answers for the other stage leaves no stale history and no phantom arrival', async () => {
      const full = history(2001)
      let reads = 0
      vi.spyOn(meshClient, 'getStageMessages').mockImplementation((id, cursor) => {
        if (id === 'other') return new Promise(() => {})
        reads++
        if (cursor === 0) return Promise.resolve({ messages: full, threads: [], next_cursor: 2001, has_more: false })
        if (cursor === 2001 && reads < 4) return Promise.resolve({ messages: [msg('new', 2002)], threads: [], next_cursor: 2002, has_more: false })
        return Promise.resolve({ messages: [], threads: [], next_cursor: cursor, has_more: false })
      })
      const { result, rerender, arrivedTrace } = renderWithTrace()
      await waitFor(() => expect(result.current.messages).toHaveLength(MAX_STAGE_MESSAGES))

      rerender({ id: 'other' })
      const returnIndex = arrivedTrace.length
      rerender({ id: 's1' })
      expect(result.current.messages).toEqual([])
      expect(result.current.arrived).toBe(0)

      await waitFor(() => expect(result.current.messages).toHaveLength(MAX_STAGE_MESSAGES))
      expect(result.current.arrived).toBe(0)
      expect(ids(result.current.messages).slice(0, 2)).toEqual(['m1', 'm2'])
      expect(result.current.messages.at(-1)?.id).toBe('m2000')
      await waitFor(() => expect(result.current.arrived).toBe(1), { timeout: 5000 })
      expect(result.current.messages.at(-1)?.id).toBe('new')
      const visit = arrivedTrace.slice(returnIndex)
      expect(visit[0]).toBe(0)
      expect(visit.at(-1)).toBe(1)
      expectNeverDecreases(visit)
    })

    test('after the other stage read failed and 10 s passed, coming back shows no stale history and no phantom arrival', async () => {
      vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
      try {
        const full = history(2500)
        vi.spyOn(meshClient, 'getStageMessages').mockImplementation((id, cursor) => {
          if (id === 'other') return Promise.reject(new Error('read failed'))
          if (cursor === 0) return Promise.resolve({ messages: full, threads: [], next_cursor: 2500, has_more: false })
          if (cursor === 2500) return Promise.resolve({ messages: [msg('new', 2501)], threads: [], next_cursor: 2501, has_more: false })
          return Promise.resolve({ messages: [], threads: [], next_cursor: cursor, has_more: false })
        })
        const { result, rerender, arrivedTrace } = renderWithTrace()
        await act(async () => { await vi.advanceTimersByTimeAsync(0) })
        expect(result.current.messages).toHaveLength(MAX_STAGE_MESSAGES)

        rerender({ id: 'other' })
        await act(async () => { await vi.advanceTimersByTimeAsync(10_000) })
        expect(result.current.messages).toEqual([])

        const returnIndex = arrivedTrace.length
        rerender({ id: 's1' })
        expect(result.current.messages).toEqual([])
        expect(result.current.arrived).toBe(0)

        await act(async () => { await vi.advanceTimersByTimeAsync(0) })
        expect(result.current.messages).toHaveLength(MAX_STAGE_MESSAGES)
        expect(result.current.arrived).toBe(0)
        expect(result.current.messages[0]?.id).toBe('m500')
        expect(result.current.messages.at(-1)?.id).toBe('m2499')

        await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
        expect(result.current.arrived).toBe(1)
        expect(result.current.messages.at(-1)?.id).toBe('new')
        const visit = arrivedTrace.slice(returnIndex)
        expect(visit[0]).toBe(0)
        expect(visit.at(-1)).toBe(1)
        expectNeverDecreases(visit)
      } finally {
        vi.useRealTimers()
      }
    })
  })
})
