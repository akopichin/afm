import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { useMeshBoard } from './use-mesh-board'
import * as meshClient from '../../api/mesh-client'


function entry(key: string): meshClient.MeshBoardEntry {
  return { key, status: key, version: 1, author_id: 'user', seq: 1, ts: '2026-10-18T00:00:00Z' }
}

describe('useMeshBoard', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('reload() after a post waits for the running poll, so the older board never lands last', async () => {
    let resolvePoll!: (v: { entries: meshClient.MeshBoardEntry[] }) => void
    let resolveReload!: (v: { entries: meshClient.MeshBoardEntry[] }) => void
    const spy = vi
      .spyOn(meshClient, 'getMeshBoard')
      .mockReturnValueOnce(new Promise((res) => (resolvePoll = res)))
      .mockReturnValueOnce(new Promise((res) => (resolveReload = res)))
    const { result } = renderHook(() => useMeshBoard())

    let reloaded: Promise<void> = Promise.resolve()
    act(() => {
      reloaded = result.current.reload()
    })
    expect(spy).toHaveBeenCalledTimes(1)

    await act(async () => resolvePoll({ entries: [entry('task/old')] }))
    expect(spy).toHaveBeenCalledTimes(2)
    await act(async () => {
      resolveReload({ entries: [entry('task/old'), entry('task/new')] })
      await reloaded
    })
    expect(result.current.entries.map((e) => e.key)).toEqual(['task/old', 'task/new'])
  })
})
