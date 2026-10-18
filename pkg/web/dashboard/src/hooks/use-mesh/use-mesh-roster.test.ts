import { renderHook, waitFor } from '@testing-library/react'
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
})
