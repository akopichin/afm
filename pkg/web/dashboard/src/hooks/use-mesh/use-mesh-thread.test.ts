import { renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { useMeshThread } from './use-mesh-thread'
import * as meshClient from '../../api/mesh-client'

describe('useMeshThread', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('threadId=null makes no network call and returns an empty list', () => {
    const spy = vi.spyOn(meshClient, 'getMeshThread')
    const { result } = renderHook(() => useMeshThread(null))
    expect(result.current).toEqual([])
    expect(spy).not.toHaveBeenCalled()
  })

  test('polls the thread and returns its messages', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({
      messages: [
        { id: 'm1', thread_id: 'mt-1', sender_id: 'user', recipient_ids: ['run:qa'], kind: 'message', body: 'hi', seq: 1, created_at: '2026-10-18T00:00:00Z' },
      ],
      next_cursor: 1,
    })

    const { result } = renderHook(() => useMeshThread('mt-1'))

    await waitFor(() => expect(result.current).toHaveLength(1))
    expect(result.current[0]?.body).toBe('hi')
  })

  test('clears messages when switching to a different thread id', async () => {
    const getMeshThreadSpy = vi.spyOn(meshClient, 'getMeshThread')
    getMeshThreadSpy.mockResolvedValueOnce({
      messages: [{ id: 'm1', thread_id: 'mt-1', sender_id: 'user', recipient_ids: [], kind: 'message', body: 'first', seq: 1, created_at: '2026-10-18T00:00:00Z' }],
      next_cursor: 1,
    })
    const { result, rerender } = renderHook(({ id }: { id: string | null }) => useMeshThread(id), { initialProps: { id: 'mt-1' as string | null } })
    await waitFor(() => expect(result.current).toHaveLength(1))

    getMeshThreadSpy.mockResolvedValueOnce({ messages: [], next_cursor: 0 })
    rerender({ id: 'mt-2' })
    expect(result.current).toEqual([])
    await waitFor(() => expect(getMeshThreadSpy).toHaveBeenCalledWith('mt-2', 0, 50))
  })

  test('follows next_cursor across a full page to return every message, not just the first page', async () => {
    const page1 = Array.from({ length: 50 }, (_, i) => ({
      id: `m${i + 1}`,
      thread_id: 'mt-1',
      sender_id: 'run:qa',
      recipient_ids: ['user'],
      kind: 'message',
      body: `msg-${i + 1}`,
      seq: i + 1,
      created_at: '2026-10-18T00:00:00Z',
    }))
    const page2 = [
      { id: 'm51', thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['user'], kind: 'message', body: 'msg-51', seq: 51, created_at: '2026-10-18T00:00:00Z' },
    ]
    const getMeshThreadSpy = vi.spyOn(meshClient, 'getMeshThread')
    getMeshThreadSpy.mockResolvedValueOnce({ messages: page1, next_cursor: 50 })
    getMeshThreadSpy.mockResolvedValueOnce({ messages: page2, next_cursor: 51 })

    const { result } = renderHook(() => useMeshThread('mt-1'))

    await waitFor(() => expect(result.current).toHaveLength(51))
    expect(getMeshThreadSpy).toHaveBeenNthCalledWith(1, 'mt-1', 0, 50)
    expect(getMeshThreadSpy).toHaveBeenNthCalledWith(2, 'mt-1', 50, 50)
    expect(result.current[50]?.body).toBe('msg-51')
  })
})
