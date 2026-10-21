import { act, renderHook, waitFor } from '@testing-library/react'
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

  test('does not request the board while the mesh is not enabled, and starts once it is', async () => {
    const spy = vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ entries: [entry('task/a')] })
    const { result, rerender } = renderHook(({ enabled }) => useMeshBoard(enabled), { initialProps: { enabled: false } })
    await act(async () => {})
    expect(spy).not.toHaveBeenCalled()
    expect(result.current.status).toBe('loading')

    rerender({ enabled: true })
    await waitFor(() => expect(result.current.status).toBe('ready'))
    expect(spy).toHaveBeenCalledTimes(1)
    expect(result.current.entries.map((e) => e.key)).toEqual(['task/a'])
  })

  test('is loading until the first fetch lands, then ready — an empty board is ready, not loading', async () => {
    let resolveFirst!: (v: { entries: meshClient.MeshBoardEntry[] }) => void
    vi.spyOn(meshClient, 'getMeshBoard').mockReturnValueOnce(new Promise((res) => (resolveFirst = res)))
    const { result } = renderHook(() => useMeshBoard())
    expect(result.current.status).toBe('loading')

    await act(async () => resolveFirst({ entries: [] }))
    expect(result.current.status).toBe('ready')
    expect(result.current.entries).toEqual([])
  })

  test('a first-load failure is an error (not an empty board) and Retry recovers through reload()', async () => {
    const spy = vi
      .spyOn(meshClient, 'getMeshBoard')
      .mockRejectedValueOnce(new Error('network'))
      .mockResolvedValueOnce({ entries: [entry('task/a')] })
    const { result } = renderHook(() => useMeshBoard())
    await waitFor(() => expect(result.current.status).toBe('error'))
    expect(result.current.entries).toEqual([])

    await act(async () => result.current.reload())
    expect(spy).toHaveBeenCalledTimes(2)
    expect(result.current.status).toBe('ready')
    expect(result.current.entries.map((e) => e.key)).toEqual(['task/a'])
  })

  test('a failed refresh keeps the last snapshot as stale until a fetch succeeds again', async () => {
    vi.spyOn(meshClient, 'getMeshBoard')
      .mockResolvedValueOnce({ entries: [entry('task/a')] })
      .mockRejectedValueOnce(new Error('network'))
      .mockResolvedValueOnce({ entries: [entry('task/a'), entry('task/b')] })
    const { result } = renderHook(() => useMeshBoard())
    await waitFor(() => expect(result.current.status).toBe('ready'))

    await act(async () => result.current.reload())
    expect(result.current.status).toBe('stale')
    expect(result.current.entries.map((e) => e.key)).toEqual(['task/a'])

    await act(async () => result.current.reload())
    expect(result.current.status).toBe('ready')
    expect(result.current.entries.map((e) => e.key)).toEqual(['task/a', 'task/b'])
  })

  test("a run switch drops the old run's board: a failed first fetch is an error, not the old run's stale board", async () => {
    vi.spyOn(meshClient, 'getMeshBoard')
      .mockResolvedValueOnce({ entries: [entry('task/a-run')] })
      .mockRejectedValueOnce(new Error('network'))
    const { result, rerender } = renderHook(({ runId }) => useMeshBoard(true, runId), { initialProps: { runId: 'A' } })
    await waitFor(() => expect(result.current.status).toBe('ready'))

    rerender({ runId: 'B' })
    expect(result.current.status).toBe('loading')
    expect(result.current.entries).toEqual([])
    await waitFor(() => expect(result.current.status).toBe('error'))
    expect(result.current.entries).toEqual([])
  })

  test("a response of the old run that lands after the switch is discarded", async () => {
    let resolveOld!: (v: { entries: meshClient.MeshBoardEntry[] }) => void
    vi.spyOn(meshClient, 'getMeshBoard')
      .mockReturnValueOnce(new Promise((res) => (resolveOld = res)))
      .mockReturnValueOnce(new Promise(() => {}))
    const { result, rerender } = renderHook(({ runId }) => useMeshBoard(true, runId), { initialProps: { runId: 'A' } })
    rerender({ runId: 'B' })
    await act(async () => resolveOld({ entries: [entry('task/a-run')] }))
    expect(result.current.status).toBe('loading')
    expect(result.current.entries).toEqual([])
  })
})

