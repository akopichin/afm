import { afterEach, describe, expect, test, vi } from 'vitest'
import {
  SideAgentApiError,
  getConversation,
  interrupt,
  normalizeConversation,
  removeQueued,
  sendMessage,
  streamUrl,
} from './side-agent-client'

describe('side-agent-client', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('getConversation parses the GET response and normalizes events', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        conversation_id: 'run-1',
        run_id: 'run-1',
        command: 'claude',
        command_name: 'Claude Code',
        unavailable: false,
        state: 'running',
        events: [
          { seq: 1, type: 'turn_accepted', data: { message: 'hi', client_message_id: 'c-1' } },
          { seq: 2, type: 'assistant_text', data: { text: 'hello' } },
          { seq: 3, type: 'bogus', data: {} },
        ],
        last_seq: 3,
        next_cursor: 2,
        has_more: true,
      }),
    } as Response)

    const conv = await getConversation()

    expect(fetchSpy).toHaveBeenCalledWith('/api/side-agent')
    expect(conv.conversationId).toBe('run-1')
    expect(conv.runId).toBe('run-1')
    expect(conv.command).toBe('claude')
    expect(conv.commandName).toBe('Claude Code')
    expect(conv.state).toBe('running')
    expect(conv.lastSeq).toBe(3)
    expect(conv.nextCursor).toBe(2)
    expect(conv.hasMore).toBe(true)
    // Событие с неизвестным типом отброшено.
    expect(conv.events.map((e) => e.seq)).toEqual([1, 2])
  })

  test('getConversation appends after_seq and limit query params', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ events: [] }),
    } as Response)

    await getConversation(5, 10)

    expect(fetchSpy).toHaveBeenCalledWith('/api/side-agent?after_seq=5&limit=10')
  })

  test('getConversation defaults a fully-missing body to a safe idle conversation', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({}),
    } as Response)

    const conv = await getConversation()
    expect(conv).toEqual({
      conversationId: '',
      runId: '',
      command: '',
      unavailable: false,
      state: 'idle',
      activeTurnId: '',
      activeSource: null,
      queued: [],
      events: [],
      lastSeq: 0,
      nextCursor: 0,
      hasMore: false,
    })
  })

  test('getConversation throws a typed error carrying the server code on non-2xx', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: false,
      status: 503,
      json: async () => ({ error: 'storage_unavailable' }),
    } as Response)

    await expect(getConversation()).rejects.toMatchObject({
      name: 'SideAgentApiError',
      status: 503,
      code: 'storage_unavailable',
      message: 'assistant request failed: 503 (storage_unavailable)',
    })
  })

  test('sendMessage posts client_message_id + text and parses the 202 body', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 202,
      json: async () => ({ turn_id: 't-7', state: 'running', cursor: 12 }),
    } as Response)

    const resp = await sendMessage('c-7', 'do a thing')

    expect(resp).toEqual({ messageId: '', messageState: 'queued', errorCode: '', turnId: 't-7', state: 'running', cursor: 12 })
    const [url, init] = fetchSpy.mock.calls[0]!
    expect(url).toBe('/api/side-agent/messages')
    expect(init?.method).toBe('POST')
    expect(JSON.parse(init?.body as string)).toEqual({ client_message_id: 'c-7', text: 'do a thing' })
  })

  test('sendMessage includes stage_id only when a non-empty stage context is given', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 202,
      json: async () => ({ turn_id: 't-8', state: 'running', cursor: 1 }),
    } as Response)

    await sendMessage('c-8', 'hi', 'frontend')
    expect(JSON.parse(fetchSpy.mock.calls[0]?.[1]?.body as string)).toEqual({
      client_message_id: 'c-8',
      text: 'hi',
      stage_id: 'frontend',
    })

    await sendMessage('c-9', 'hi', '')
    expect(JSON.parse(fetchSpy.mock.calls[1]?.[1]?.body as string)).toEqual({ client_message_id: 'c-9', text: 'hi' })
  })

  test('sendMessage surfaces the typed error code (e.g. busy)', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: false,
      status: 409,
      json: async () => ({ error: 'busy' }),
    } as Response)

    await expect(sendMessage('c-1', 'x')).rejects.toBeInstanceOf(SideAgentApiError)
    await expect(sendMessage('c-1', 'x')).rejects.toMatchObject({ code: 'busy', status: 409 })
  })

  test('sendMessage parses the queue receipt, including a failed state with its error code', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 202,
      json: async () => ({ message_id: 'm-1', message_state: 'failed', error_code: 'context_limit', state: 'idle', cursor: 5 }),
    } as Response)

    expect(await sendMessage('c-1', 'x')).toEqual({
      messageId: 'm-1', messageState: 'failed', errorCode: 'context_limit', turnId: '', state: 'idle', cursor: 5,
    })
  })

  test('removeQueued sends DELETE for the encoded message id and parses the receipt', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ message_id: 'm/1', message_state: 'cancelled', cursor: 9 }),
    } as Response)

    expect(await removeQueued('m/1')).toEqual({ messageId: 'm/1', messageState: 'cancelled', cursor: 9 })
    const [url, init] = fetchSpy.mock.calls[0]!
    expect(url).toBe('/api/side-agent/messages/m%2F1')
    expect(init?.method).toBe('DELETE')
  })

  test('removeQueued surfaces message_not_queued as a typed 409', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: false,
      status: 409,
      json: async () => ({ error: 'message_not_queued' }),
    } as Response)

    await expect(removeQueued('m-1')).rejects.toMatchObject({ code: 'message_not_queued', status: 409 })
  })

  test('normalizeConversation reads the active turn, its source and the queue', () => {
    const c = normalizeConversation({
      state: 'idle',
      active_turn_id: 't-1',
      active_source: { kind: 'mesh', sender_id: 'run:qa', sender_name: 'QA' },
      queued: [{ message_id: 'm-1', client_message_id: 'c-1', text: 'hi', stage_id: 's1', assistant_task_id: 'a1', seq: 4 }],
    })
    expect(c.activeTurnId).toBe('t-1')
    expect(c.activeSource).toEqual({ kind: 'mesh', senderId: 'run:qa', senderName: 'QA' })
    expect(c.queued).toEqual([{ messageId: 'm-1', clientMessageId: 'c-1', text: 'hi', stageId: 's1', assistantTaskId: 'a1', seq: 4 }])
    expect(normalizeConversation({}).queued).toEqual([])
    expect(normalizeConversation({}).activeSource).toBeNull()
  })

  test('interrupt posts the turn id', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ status: 'interrupting' }),
    } as Response)

    await interrupt('t-3')

    const [url, init] = fetchSpy.mock.calls[0]!
    expect(url).toBe('/api/side-agent/interrupt')
    expect(init?.method).toBe('POST')
    expect(JSON.parse(init?.body as string)).toEqual({ turn_id: 't-3' })
  })

  test('interrupt throws a typed error with the stale_turn code', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: false,
      status: 409,
      json: async () => ({ error: 'stale_turn' }),
    } as Response)

    await expect(interrupt('old')).rejects.toMatchObject({ code: 'stale_turn' })
  })

  test('streamUrl adds after_seq only when positive', () => {
    expect(streamUrl()).toBe('/api/side-agent/stream')
    expect(streamUrl(0)).toBe('/api/side-agent/stream')
    expect(streamUrl(42)).toBe('/api/side-agent/stream?after_seq=42')
  })
})
