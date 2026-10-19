import { renderHook, waitFor } from '@testing-library/react'
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
})
