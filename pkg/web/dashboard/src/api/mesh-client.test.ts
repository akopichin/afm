import { afterEach, describe, expect, test, vi } from 'vitest'
import {
  MeshApiError,
  attachAndSendMesh,
  genMeshRequestId,
  getMeshBoard,
  getMeshThread,
  getStageMessages,
  listMeshParticipants,
  listMeshThreads,
  openMeshThread,
  postMeshTask,
  sendMeshMessage,
  sendMeshMessageTo,
  updateMeshThreadTitle,
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
    expect(res.summaries).toEqual({})
  })

  test('listMeshThreads decodes keyed summaries and defaults an absent title revision to zero', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        threads: [{ id: 'mt-1', scope: 'run', members: ['user', 'run:qa'], status: 'open' }],
        summaries: {
          'mt-1': {
            last_message: { id: 'm-7', seq: 7, created_at: '2026-10-21T10:00:00Z', sender_id: 'run:qa', preview: 'Saved message' },
            last_other_message_seq: 7,
            last_agent_to_user_message_seq: 5,
          },
          'mt-2': { last_other_message_seq: 0, title_seq: 4 },
        },
      }),
    } as Response)

    expect(await listMeshThreads()).toEqual({
      threads: [{ id: 'mt-1', scope: 'run', members: ['user', 'run:qa'], status: 'open' }],
      summaries: {
        'mt-1': {
          last_message: { id: 'm-7', seq: 7, created_at: '2026-10-21T10:00:00Z', sender_id: 'run:qa', preview: 'Saved message' },
          last_other_message_seq: 7,
          last_agent_to_user_message_seq: 5,
          title_seq: 0,
        },
        'mt-2': { last_other_message_seq: 0, last_agent_to_user_message_seq: 0, title_seq: 4 },
      },
    })
    expect(fetchSpy).toHaveBeenCalledTimes(1)
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

  test('getMeshBoard maps typed_tasks and tasks; a legacy response has no typed tasks', async () => {
    const view = { key: 'task/a', author_id: 'user', title: 'A', version: 2, generation: 1, assignments: [], created_seq: 1, updated_seq: 2, created_at: 't', updated_at: 't', status: 'todo' }
    vi.spyOn(globalThis, 'fetch').mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ entries: [], typed_tasks: true, tasks: [view] }) } as Response)
    expect(await getMeshBoard()).toEqual({ entries: [], tasks: [view], typedTasks: true })

    vi.spyOn(globalThis, 'fetch').mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ entries: [] }) } as Response)
    expect(await getMeshBoard()).toEqual({ entries: [], tasks: [], typedTasks: false })
  })

  test('postMeshTask posts snake_case fields to /api/mesh/tasks and omits unset ones', async () => {
    const receipt = { key: 'task/a', version: 3, status: 'in_progress', seq: 9 }
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: true, status: 200, json: async () => receipt } as Response)

    const res = await postMeshTask({ requestId: 'r1', action: 'assign', key: 'task/a', expectedVersion: 2, assignmentId: 'as-1', to: 'run:qa', text: 'go' })

    expect(res).toEqual(receipt)
    const [url, init] = fetchSpy.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/mesh/tasks')
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body as string)).toEqual({ request_id: 'r1', action: 'assign', key: 'task/a', expected_version: 2, assignment_id: 'as-1', to: 'run:qa', text: 'go' })
  })

  test('postMeshTask surfaces a server error as MeshApiError', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: false, status: 409, json: async () => ({ error: 'version_conflict', message: 'stale' }) } as Response)
    await expect(postMeshTask({ requestId: 'r', action: 'update', key: 'task/a' })).rejects.toMatchObject({ status: 409 })
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

  test('updateMeshThreadTitle posts a revisioned topic edit and preserves conflict status', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValueOnce({
      ok: true, status: 200, json: async () => ({ title: 'New topic', title_seq: 17 }),
    } as Response).mockResolvedValueOnce({
      ok: false, status: 409, json: async () => ({ error: 'operation_conflict', message: 'topic changed' }),
    } as Response)

    await expect(updateMeshThreadTitle('mt/a', { requestId: 'edit-1', title: 'New topic', expectedTitleSeq: 7 }))
      .resolves.toEqual({ title: 'New topic', title_seq: 17 })
    expect(fetchSpy).toHaveBeenCalledWith('/api/mesh/threads/mt%2Fa/topic', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ request_id: 'edit-1', title: 'New topic', expected_title_seq: 7 }),
    }))
    await expect(updateMeshThreadTitle('mt/a', { requestId: 'edit-2', title: '', expectedTitleSeq: 7 }))
      .rejects.toMatchObject({ status: 409, code: 'operation_conflict', message: 'topic changed' })
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

  test('sendMeshMessageTo posts recipients without a thread, with the reply target and the feed it is written from', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ message_id: 'm2', seq: 4, thread_id: 'mt-9' }),
    } as Response)

    const receipt = await sendMeshMessageTo({ requestId: 'req-4', recipientIds: ['qa', 'docs'], inReplyTo: 'm1', body: 'both', originStage: 'impl' })

    expect(fetchSpy).toHaveBeenCalledWith(
      '/api/mesh/messages',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ request_id: 'req-4', recipient_ids: ['qa', 'docs'], kind: 'message', in_reply_to: 'm1', body: 'both', origin_stage: 'impl' }),
      }),
    )
    expect(receipt).toEqual({ message_id: 'm2', seq: 4, thread_id: 'mt-9' })
  })

  test("a rejected request's error reads as the server's reason", async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: false,
      status: 409,
      json: async () => ({ error: 'invalid_status', message: 'stage:docs cannot receive messages (stage docs has reached a terminal status)' }),
    } as Response)
    const err = await sendMeshMessageTo({ requestId: 'r', recipientIds: ['docs'], body: 'hi' }).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(MeshApiError)
    expect(err).toMatchObject({ status: 409, code: 'invalid_status' })
    expect((err as Error).message).toBe('stage:docs cannot receive messages (stage docs has reached a terminal status)')
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

  test('getStageMessages reads one page of a stage feed\'s conversations', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ messages: null, threads: null, next_cursor: 9, has_more: true }),
    } as Response)

    const page = await getStageMessages('impl 1', 4, 50)

    expect(fetchSpy).toHaveBeenCalledWith('/api/mesh/stages/impl%201/messages?cursor=4&limit=50')
    expect(page).toEqual({ messages: [], threads: [], next_cursor: 9, has_more: true })
  })

  test('openMeshThread and attachAndSendMesh send origin_stage when given', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: true, status: 200, json: async () => ({}) } as Response)

    await openMeshThread({ requestId: 'r1', title: '', scope: 'run', memberIds: ['run:qa'], originStage: 'impl' })
    await attachAndSendMesh({ requestId: 'r2', profileId: 'qa', scope: 'run', originStage: 'impl' })

    const bodies = fetchSpy.mock.calls.map((c) => JSON.parse(String((c[1] as RequestInit).body)) as Record<string, unknown>)
    expect(bodies[0]?.origin_stage).toBe('impl')
    expect(bodies[1]?.origin_stage).toBe('impl')
  })
})
