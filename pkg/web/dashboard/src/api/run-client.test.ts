import { afterEach, describe, expect, test, vi } from 'vitest'
import {
  addNote,
  attachAssistant,
  cancelNotes,
  continueStage,
  deleteNote,
  FlowApiError,
  getAssistantActivity,
  injectNotes,
  listNotes,
  pauseFlow,
  pauseStage,
  postStageDiscussionMessage,
  retryStage,
  triggerStageButton,
  updateNote,
} from './run-client'

describe('run-client attachAssistant', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('POSTs to /assistants with profile/mode/trigger and returns the new task id', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ task_id: 't-123' }) })
    vi.stubGlobal('fetch', fetchMock)

    const { taskId } = await attachAssistant('build', {
      profileId: 'reviewer',
      title: 'Correctness',
      instruction: 'Please review.',
      mode: 'required',
      trigger: 'one_shot',
    })

    expect(taskId).toBe('t-123')
    const [url, init] = fetchMock.mock.calls[0]!
    expect(url).toBe('/api/stages/build/assistants')
    expect(init).toMatchObject({ method: 'POST' })
    const body = JSON.parse(init.body)
    expect(body).toMatchObject({
      profile_id: 'reviewer',
      title: 'Correctness',
      instruction: 'Please review.',
      mode: 'required',
      trigger: 'one_shot',
    })
    expect(typeof body.request_id).toBe('string')
    expect(body.request_id.length).toBeGreaterThan(0)
  })

  test('throws FlowApiError on a rejected attach', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: false, status: 400, json: async () => ({ error: 'invalid_status' }) })
    vi.stubGlobal('fetch', fetchMock)
    await expect(
      attachAssistant('done-stage', { profileId: 'reviewer', title: '', instruction: 'x', mode: 'advisory', trigger: 'one_shot' }),
    ).rejects.toThrow()
  })
})

describe('run-client pause/continue', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('retryStage POSTs to /retry with no body', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true })
    vi.stubGlobal('fetch', fetchMock)

    await retryStage('s1')

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/stages/s1/retry',
      expect.objectContaining({ method: 'POST', body: null }),
    )
  })

  test('pauseStage POSTs to /pause with no body', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true })
    vi.stubGlobal('fetch', fetchMock)

    await pauseStage('s1')

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/stages/s1/pause',
      expect.objectContaining({ method: 'POST', body: null }),
    )
  })

  test('continueStage POSTs to /continue with no body', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true })
    vi.stubGlobal('fetch', fetchMock)

    await continueStage('s1')

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/stages/s1/continue',
      expect.objectContaining({ method: 'POST', body: null }),
    )
  })

  test('triggerStageButton POSTs the button name to /button', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true })
    vi.stubGlobal('fetch', fetchMock)

    await triggerStageButton('s1', 'Run linter')

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/stages/s1/button',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ name: 'Run linter' }) }),
    )
  })
})

describe('run-client flow-wide review-pause + notes calls', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('pauseFlow POSTs /api/flow/pause', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ paused_stages: ['s1'] }) })
    vi.stubGlobal('fetch', fetchMock)

    const res = await pauseFlow()

    expect(fetchMock).toHaveBeenCalledWith('/api/flow/pause', expect.objectContaining({ method: 'POST' }))
    expect(res.paused_stages).toEqual(['s1'])
  })

  test('injectNotes POSTs the target stage id to /api/flow/notes/inject', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true })
    vi.stubGlobal('fetch', fetchMock)

    await injectNotes('s1')

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/flow/notes/inject',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ stage_id: 's1' }) }),
    )
  })

  test('cancelNotes POSTs /api/flow/notes/cancel with no body', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true })
    vi.stubGlobal('fetch', fetchMock)

    await cancelNotes()

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/flow/notes/cancel',
      expect.objectContaining({ method: 'POST', body: null }),
    )
  })

  test('listNotes GETs /api/flow/notes and returns rev+notes', async () => {
    const note = {
      id: 'n1', root: 'project', path: 'a.go', display_path: 'a.go', reference: '[AFM file: "a.go"]',
      line: 3, orig_line_text: 'foo', content_sha: 'sha', text: 'fix this', created_at: '2026-09-08T00:00:00Z',
    }
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ rev: 2, notes: [note] }) })
    vi.stubGlobal('fetch', fetchMock)

    const res = await listNotes()

    expect(fetchMock).toHaveBeenCalledWith('/api/flow/notes')
    expect(res.rev).toBe(2)
    expect(res.notes).toEqual([note])
  })

  // Review-finding P1 #6: a fresh store serializes zero notes as `"notes": null`.
  test('listNotes coerces a null notes field to an empty array', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ rev: 0, notes: null }) })
    vi.stubGlobal('fetch', fetchMock)

    const res = await listNotes()

    expect(res.rev).toBe(0)
    expect(res.notes).toEqual([])
  })

  test('addNote POSTs the note body to /api/flow/notes and returns note+rev', async () => {
    const note = {
      id: 'n1', root: 'project', path: 'a.go', display_path: 'a.go', reference: '[AFM file: "a.go"]',
      line: null, orig_line_text: null, content_sha: 'sha', text: 'note text', created_at: '2026-09-08T00:00:00Z',
    }
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ note, rev: 1 }) })
    vi.stubGlobal('fetch', fetchMock)

    const body = { root: 'project', path: 'a.go', line: null, text: 'note text', content_sha: 'sha', expected_rev: 0 }
    const res = await addNote(body)

    expect(fetchMock).toHaveBeenCalledWith('/api/flow/notes', expect.objectContaining({
      method: 'POST', body: JSON.stringify(body),
    }))
    expect(res.note).toEqual(note)
    expect(res.rev).toBe(1)
  })

  test('updateNote PUTs to /api/flow/notes/{id} and returns rev', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ rev: 5 }) })
    vi.stubGlobal('fetch', fetchMock)

    const res = await updateNote('n1', 'new text', 4)

    expect(fetchMock).toHaveBeenCalledWith('/api/flow/notes/n1', expect.objectContaining({
      method: 'PUT', body: JSON.stringify({ text: 'new text', expected_rev: 4 }),
    }))
    expect(res.rev).toBe(5)
  })

  test('deleteNote DELETEs /api/flow/notes/{id}?expected_rev=', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true })
    vi.stubGlobal('fetch', fetchMock)

    await deleteNote('n1', 3)

    expect(fetchMock).toHaveBeenCalledWith('/api/flow/notes/n1?expected_rev=3', expect.objectContaining({
      method: 'DELETE',
    }))
  })

  test('a {"error":code} body on a failed call surfaces .code on the thrown Error', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false,
      status: 409,
      json: async () => ({ error: 'stale_content' }),
    })
    vi.stubGlobal('fetch', fetchMock)

    await expect(addNote({ root: 'project', path: 'a.go', line: null, text: 't', content_sha: 'x', expected_rev: 0 }))
      .rejects.toMatchObject({ code: 'stale_content' })
  })
})

describe('FlowApiError carries the server reason', () => {
  afterEach(() => vi.restoreAllMocks())

  test("a refused stage-conversation post shows the server's reason, with its code", async () => {
    const reason = '@qa cannot read this post (task t-1 has reached a terminal status) — remove the mention and send again'
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: false, status: 409, json: async () => ({ error: 'invalid_status', message: reason }) } as Response)
    const err = await postStageDiscussionMessage('s1', 'req-1', { body: 'hi', mentions: ['run:t-1'], interrupt: false }).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(FlowApiError)
    expect((err as FlowApiError).message).toBe(reason)
    expect((err as FlowApiError).code).toBe('invalid_status')
    expect((err as FlowApiError).status).toBe(409)
  })

  test('without a reason the message names the request, status and code as before', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: false, status: 404, json: async () => ({ error: 'not_found' }) } as Response)
    const err = await postStageDiscussionMessage('s1', 'req-1', { body: 'hi', mentions: [], interrupt: false }).catch((e: unknown) => e)
    expect((err as FlowApiError).message).toBe('/api/stages/s1/discussion -> 404 (not_found)')
  })
})

describe('getAssistantActivity', () => {
  afterEach(() => vi.restoreAllMocks())

  // Older browsers have no URLSearchParams.size; the cursor must still reach the server.
  test('sends limit and cursors without relying on URLSearchParams.size', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ entries: [] }) })
    vi.stubGlobal('fetch', fetchMock)
    const sizeDescriptor = Object.getOwnPropertyDescriptor(URLSearchParams.prototype, 'size')
    Object.defineProperty(URLSearchParams.prototype, 'size', { configurable: true, get: () => undefined })
    try {
      await getAssistantActivity('t-1', { limit: 100, before: '20' })
      await getAssistantActivity('t-1', { after: '30' })
    } finally {
      if (sizeDescriptor !== undefined) Object.defineProperty(URLSearchParams.prototype, 'size', sizeDescriptor)
    }
    expect(fetchMock.mock.calls.map((call) => call[0])).toEqual([
      '/api/assistants/t-1/activity?limit=100&before=20',
      '/api/assistants/t-1/activity?after=30',
    ])
  })

  test('a plain tail request has no query', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ entries: [] }) })
    vi.stubGlobal('fetch', fetchMock)
    await getAssistantActivity('t-1', {})
    expect(fetchMock.mock.calls[0]![0]).toBe('/api/assistants/t-1/activity')
  })
})
