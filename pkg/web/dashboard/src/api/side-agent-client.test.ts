import { afterEach, describe, expect, test, vi } from 'vitest'
import {
  SideAgentApiError,
  getConversation,
  interrupt,
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
    })
  })

  test('sendMessage posts client_message_id + text and parses the 202 body', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 202,
      json: async () => ({ turn_id: 't-7', state: 'running', cursor: 12 }),
    } as Response)

    const resp = await sendMessage('c-7', 'do a thing')

    expect(resp).toEqual({ turnId: 't-7', state: 'running', cursor: 12 })
    const [url, init] = fetchSpy.mock.calls[0]!
    expect(url).toBe('/api/side-agent/messages')
    expect(init?.method).toBe('POST')
    expect(JSON.parse(init?.body as string)).toEqual({ client_message_id: 'c-7', text: 'do a thing' })
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
