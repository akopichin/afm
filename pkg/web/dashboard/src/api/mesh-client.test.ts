import { afterEach, describe, expect, test, vi } from 'vitest'
import {
  MeshApiError,
  attachAndSendMesh,
  genMeshRequestId,
  getMeshBoard,
  getMeshThread,
  listMeshParticipants,
  listMeshThreads,
  openMeshThread,
  sendMeshMessage,
} from './mesh-client'

describe('mesh-client', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('listMeshParticipants reads GET /api/mesh/participants', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ participants: [{ id: 'user', kind: 'user', scope: 'run', reachable: true }] }),
    } as Response)

    const res = await listMeshParticipants()

    expect(fetchSpy).toHaveBeenCalledWith('/api/mesh/participants')
    expect(res.participants).toEqual([{ id: 'user', kind: 'user', scope: 'run', reachable: true }])
  })

  test('listMeshParticipants defaults a null participants field to an empty list', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: true, status: 200, json: async () => ({ participants: null }) } as Response)
    expect((await listMeshParticipants()).participants).toEqual([])
  })

  test('listMeshParticipants surfaces a 404 as MeshApiError (capability off)', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: false, status: 404, json: async () => ({}) } as Response)
    await expect(listMeshParticipants()).rejects.toBeInstanceOf(MeshApiError)
    await expect(listMeshParticipants()).rejects.toMatchObject({ status: 404 })
  })

  test('listMeshThreads reads GET /api/mesh/threads', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ threads: [{ id: 'mt-1', scope: 'run', members: ['user', 'run:qa'], status: 'open' }] }),
    } as Response)

    const res = await listMeshThreads()

    expect(fetchSpy).toHaveBeenCalledWith('/api/mesh/threads')
    expect(res.threads).toHaveLength(1)
    expect(res.threads[0]?.id).toBe('mt-1')
  })

  test('getMeshThread builds the cursor/limit query string', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ messages: [], next_cursor: 7 }),
    } as Response)

    const page = await getMeshThread('mt-1', 3, 10)

    expect(fetchSpy).toHaveBeenCalledWith('/api/mesh/threads/mt-1?cursor=3&limit=10')
    expect(page.next_cursor).toBe(7)
  })

  test('getMeshBoard omits the key param when none is given (every entry)', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: true, status: 200, json: async () => ({ entries: [] }) } as Response)
    await getMeshBoard()
    expect(fetchSpy).toHaveBeenCalledWith('/api/mesh/board')
  })

  test('getMeshBoard appends the key param when given', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: true, status: 200, json: async () => ({ entries: [] }) } as Response)
    await getMeshBoard('qa:report')
    expect(fetchSpy).toHaveBeenCalledWith('/api/mesh/board?key=qa%3Areport')
  })

  test('openMeshThread posts request_id/title/scope/member_ids', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ id: 'mt-2', scope: 'run', members: ['user', 'run:qa'], status: 'open' }),
    } as Response)

    await openMeshThread({ requestId: 'req-1', title: 'Hand-off', scope: 'run', memberIds: ['run:qa'] })

    expect(fetchSpy).toHaveBeenCalledWith(
      '/api/mesh/threads',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ request_id: 'req-1', title: 'Hand-off', scope: 'run', member_ids: ['run:qa'] }),
      }),
    )
  })

  test('sendMeshMessage posts into the thread named in the URL, defaulting kind to "message"', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ message_id: 'm1', seq: 1 }),
    } as Response)

    await sendMeshMessage('mt-2', { requestId: 'req-2', recipientIds: ['run:qa'], body: 'hello' })

    expect(fetchSpy).toHaveBeenCalledWith(
      '/api/mesh/threads/mt-2/messages',
      expect.objectContaining({
        body: JSON.stringify({ request_id: 'req-2', recipient_ids: ['run:qa'], kind: 'message', body: 'hello' }),
      }),
    )
  })

  test('attachAndSendMesh posts scope "run" with no stage_id for a run-scoped agent', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ task_id: 't1', participant_id: 'run:qa', thread_id: 'mt-3' }),
    } as Response)

    const result = await attachAndSendMesh({ requestId: 'req-3', profileId: 'qa', scope: 'run', title: 'QA', instruction: 'review' })

    expect(fetchSpy).toHaveBeenCalledWith(
      '/api/mesh/attach-and-send',
      expect.objectContaining({
        body: JSON.stringify({ request_id: 'req-3', profile_id: 'qa', scope: 'run', title: 'QA', instruction: 'review' }),
      }),
    )
    expect(result.thread_id).toBe('mt-3')
  })

  test('genMeshRequestId produces distinct ids carrying the given prefix', () => {
    const a = genMeshRequestId('mesh-run-agent')
    const b = genMeshRequestId('mesh-run-agent')
    expect(a).not.toBe(b)
    expect(a.startsWith('mesh-run-agent-')).toBe(true)
  })
})
