import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { useMeshRoster } from './use-mesh-roster'
import * as meshClient from '../../api/mesh-client'

describe('useMeshRoster', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('enabled=true and populated once both fetches resolve', async () => {
    vi.spyOn(meshClient, 'listMeshParticipants').mockResolvedValue({
      participants: [{ id: 'run:qa', kind: 'run_agent', scope: 'run', reachable: true }],
    })
    vi.spyOn(meshClient, 'listMeshThreads').mockResolvedValue({
      threads: [{ id: 'mt-1', scope: 'run', members: ['user', 'run:qa'], status: 'open' }],
      summaries: { 'mt-1': { last_other_message_seq: 8, title_seq: 0 } },
    })

    const { result } = renderHook(() => useMeshRoster())

    await waitFor(() => expect(result.current.probed).toBe(true))
    expect(result.current.enabled).toBe(true)
    expect(result.current.participants).toHaveLength(1)
    expect(result.current.threads).toHaveLength(1)
    expect(result.current.summaries).toEqual({ 'mt-1': { last_other_message_seq: 8, title_seq: 0 } })
  })

  test('a run switch hides the old roster until the new run poll resolves', async () => {
    vi.spyOn(meshClient, 'listMeshParticipants').mockResolvedValue({ participants: [] })
    let resolveNew!: (value: { threads: meshClient.MeshThread[]; summaries: meshClient.MeshThreadSummaries }) => void
    vi.spyOn(meshClient, 'listMeshThreads')
      .mockResolvedValueOnce({ threads: [], summaries: { old: { last_other_message_seq: 9, title_seq: 0 } } })
      .mockReturnValueOnce(new Promise((resolve) => { resolveNew = resolve }))
    const { result, rerender } = renderHook(({ runId }) => useMeshRoster(runId), { initialProps: { runId: 'run-a' } })
    await waitFor(() => expect(result.current.runId).toBe('run-a'))
    await waitFor(() => expect(result.current.probed).toBe(true))
    rerender({ runId: 'run-b' })
    expect(result.current.runId).toBe('run-b')
    expect(result.current.probed).toBe(false)
    expect(result.current.summaries).toEqual({})
    await act(async () => resolveNew({ threads: [], summaries: { fresh: { last_other_message_seq: 3, title_seq: 0 } } }))
    expect(result.current.runId).toBe('run-b')
    expect(result.current.summaries).toHaveProperty('fresh')
  })

  test('a 404 on the first participants fetch means the capability is off', async () => {
    vi.spyOn(meshClient, 'listMeshParticipants').mockRejectedValue(new meshClient.MeshApiError('/api/mesh/participants', 404, undefined))
    vi.spyOn(meshClient, 'listMeshThreads').mockResolvedValue({ threads: [], summaries: {} })

    const { result } = renderHook(() => useMeshRoster())

    await waitFor(() => expect(result.current.probed).toBe(true))
    expect(result.current.enabled).toBe(false)
    expect(result.current.participants).toEqual([])
    expect(result.current.summaries).toEqual({})
  })

  test('a later failed poll keeps the last successful summaries', async () => {
    vi.useFakeTimers()
    try {
      vi.spyOn(meshClient, 'listMeshParticipants').mockResolvedValue({ participants: [] })
      const threads = vi.spyOn(meshClient, 'listMeshThreads')
        .mockResolvedValueOnce({ threads: [], summaries: { 'mt-1': { last_other_message_seq: 5, title_seq: 0 } } })
        .mockRejectedValueOnce(new Error('network error'))
      const { result } = renderHook(() => useMeshRoster())

      await act(async () => vi.advanceTimersByTime(0))
      expect(result.current.summaries).toEqual({ 'mt-1': { last_other_message_seq: 5, title_seq: 0 } })

      await act(async () => vi.advanceTimersByTime(3000))
      expect(threads).toHaveBeenCalledTimes(2)
      expect(result.current.enabled).toBe(true)
      expect(result.current.summaries).toEqual({ 'mt-1': { last_other_message_seq: 5, title_seq: 0 } })
    } finally {
      vi.useRealTimers()
    }
  })

  test('a slow poll is not overlapped by the next tick, so an older roster never replaces a newer one', async () => {
    vi.useFakeTimers()
    try {
      let resolveFirst!: (v: { participants: meshClient.MeshParticipant[] }) => void
      const participants = vi
        .spyOn(meshClient, 'listMeshParticipants')
        .mockReturnValueOnce(new Promise((res) => (resolveFirst = res)))
        .mockResolvedValue({ participants: [{ id: 'run:new', kind: 'run_agent', scope: 'run', reachable: true }] })
      vi.spyOn(meshClient, 'listMeshThreads')
        .mockResolvedValueOnce({ threads: [], summaries: { 'mt-1': { last_other_message_seq: 1, title_seq: 0 } } })
        .mockResolvedValue({ threads: [], summaries: { 'mt-1': { last_other_message_seq: 2, title_seq: 0 } } })
      const { result } = renderHook(() => useMeshRoster())
      await act(async () => vi.advanceTimersByTime(6000))
      expect(participants).toHaveBeenCalledTimes(1)

      await act(async () => resolveFirst({ participants: [{ id: 'run:old', kind: 'run_agent', scope: 'run', reachable: true }] }))
      await act(async () => vi.advanceTimersByTime(0))
      expect(result.current.participants.map((p) => p.id)).toEqual(['run:new'])
      expect(result.current.summaries).toEqual({ 'mt-1': { last_other_message_seq: 2, title_seq: 0 } })
    } finally {
      vi.useRealTimers()
    }
  })
})
