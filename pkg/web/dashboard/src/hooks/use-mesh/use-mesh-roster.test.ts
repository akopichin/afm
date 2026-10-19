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
    })

    const { result } = renderHook(() => useMeshRoster())

    await waitFor(() => expect(result.current.probed).toBe(true))
    expect(result.current.enabled).toBe(true)
    expect(result.current.participants).toHaveLength(1)
    expect(result.current.threads).toHaveLength(1)
  })

  test('a 404 on the first participants fetch means the capability is off', async () => {
    vi.spyOn(meshClient, 'listMeshParticipants').mockRejectedValue(new meshClient.MeshApiError('/api/mesh/participants', 404, undefined))
    vi.spyOn(meshClient, 'listMeshThreads').mockResolvedValue({ threads: [] })

    const { result } = renderHook(() => useMeshRoster())

    await waitFor(() => expect(result.current.probed).toBe(true))
    expect(result.current.enabled).toBe(false)
    expect(result.current.participants).toEqual([])
  })

  test('a slow poll is not overlapped by the next tick, so an older roster never replaces a newer one', async () => {
    vi.useFakeTimers()
    try {
      let resolveFirst!: (v: { participants: meshClient.MeshParticipant[] }) => void
      const participants = vi
        .spyOn(meshClient, 'listMeshParticipants')
        .mockReturnValueOnce(new Promise((res) => (resolveFirst = res)))
        .mockResolvedValue({ participants: [{ id: 'run:new', kind: 'run_agent', scope: 'run', reachable: true }] })
      vi.spyOn(meshClient, 'listMeshThreads').mockResolvedValue({ threads: [] })
      const { result } = renderHook(() => useMeshRoster())
      await act(async () => vi.advanceTimersByTime(6000))
      expect(participants).toHaveBeenCalledTimes(1)

      await act(async () => resolveFirst({ participants: [{ id: 'run:old', kind: 'run_agent', scope: 'run', reachable: true }] }))
      await act(async () => vi.advanceTimersByTime(0))
      expect(result.current.participants.map((p) => p.id)).toEqual(['run:new'])
    } finally {
      vi.useRealTimers()
    }
  })
})
