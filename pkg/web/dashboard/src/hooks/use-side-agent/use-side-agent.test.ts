import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defaultStreamOpener, useSideAgent, type SideAgentDeps, type StreamHandlers } from './use-side-agent'
import { SIDE_AGENT_EVENT_TYPES } from '../../types/side-agent'
import { SideAgentApiError, type ConversationResponse, type SendResponse } from '../../api/side-agent-client'
import type { SideAgentEvent } from '../../types/side-agent'
import { buildReplyBody } from '../../lib/quote'

// Готовый parsed-event (camelCase) для ответа getConversation.
function parsed(seq: number, type: SideAgentEvent['type'], data: unknown, turnId = 't1'): SideAgentEvent {
  return { schemaVersion: 1, seq, conversationId: 'c1', turnId, timestamp: '2026-10-01T10:00:00Z', type, data } as SideAgentEvent
}

// Сырой wire-кадр (snake_case) для стрима — хук сам зовёт parseSideAgentEvent.
function wire(seq: number, type: string, data: unknown, turnId = 't1'): unknown {
  return { schema_version: 1, seq, conversation_id: 'c1', turn_id: turnId, type, timestamp: '2026-10-01T10:00:00Z', data }
}

function receipt(over: Partial<SendResponse> = {}): SendResponse {
  return { messageId: 'm-1', messageState: 'running', errorCode: '', turnId: 't1', state: 'running', cursor: 1, ...over }
}

function emptyConversation(over: Partial<ConversationResponse> = {}): ConversationResponse {
  return {
    conversationId: 'c1', runId: 'r1', command: 'claude', unavailable: false,
    state: 'idle', activeTurnId: '', activeSource: null, queued: [],
    events: [], lastSeq: 0, nextCursor: 0, hasMore: false, ...over,
  }
}

function makeDeps(convo: () => ConversationResponse = emptyConversation) {
  let handlers: StreamHandlers | null = null
  const close = vi.fn()
  const openStream = vi.fn((_afterSeq: number, h: StreamHandlers) => {
    handlers = h
    return { close }
  })
  // serverState после принятого Send становится running, чтобы последующий getConversation не откатил UI в
  // idle.
  let serverState: 'idle' | 'running' | null = null
  const getConversation = vi.fn(async () => {
    const c = convo()
    return serverState === null ? c : { ...c, state: serverState }
  })
  const sendMessage = vi.fn(async (_clientMessageId: string, _text: string) => {
    serverState = 'running'
    return receipt({ turnId: 't1', state: 'running', cursor: 1 })
  })
  const interrupt = vi.fn(async () => {})
  const removeQueued = vi.fn(async (messageId: string) => ({ messageId, messageState: 'cancelled' as const, cursor: 1 }))
  const deps: SideAgentDeps = { getConversation, sendMessage, interrupt, removeQueued, openStream }
  return { deps, openStream, close, getConversation, sendMessage, interrupt, removeQueued, emit: (raw: unknown) => handlers?.onEvent(raw) }
}

describe('useSideAgent', () => {
  beforeEach(() => vi.spyOn(console, 'error').mockImplementation(() => {}))
  afterEach(() => vi.restoreAllMocks())

  it('does not open a stream while idle and closed', () => {
    const { deps, openStream } = makeDeps()
    renderHook(() => useSideAgent('r1', true, deps))
    expect(openStream).not.toHaveBeenCalled()
  })

  it('open() connects (getConversation then stream) and clears unseenResult', async () => {
    const { deps, openStream, getConversation } = makeDeps()
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { result.current.open() })
    expect(result.current.isOpen).toBe(true)
    expect(getConversation).toHaveBeenCalled()
    await waitFor(() => expect(openStream).toHaveBeenCalled())
  })

  it('does not connect when disabled (capability off)', async () => {
    const { deps, getConversation, openStream } = makeDeps()
    const { result } = renderHook(() => useSideAgent('r1', false, deps))
    await act(async () => { result.current.open() })
    expect(getConversation).not.toHaveBeenCalled()
    expect(openStream).not.toHaveBeenCalled()
  })

  it('send() posts a client_message_id, clears the draft, goes running', async () => {
    const { deps, sendMessage } = makeDeps()
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    act(() => { result.current.setDraft('hello') })
    await act(async () => { await result.current.send('hello') })
    expect(sendMessage).toHaveBeenCalledTimes(1)
    expect(sendMessage.mock.calls[0]?.[1]).toBe('hello')
    expect(result.current.draft).toBe('')
    expect(result.current.requestState).toBe('running')
  })

  it('a send while a turn runs is its own POST with a new id and no busy error', async () => {
    const { deps, sendMessage } = makeDeps()
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { await result.current.send('first') })
    expect(result.current.requestState).toBe('running')
    await act(async () => { await result.current.send('second') })
    expect(sendMessage.mock.calls.map((c) => c[1])).toEqual(['first', 'second'])
    expect(sendMessage.mock.calls[0]?.[0]).not.toBe(sendMessage.mock.calls[1]?.[0])
    expect(result.current.error).toBeNull()
  })

  it('submitting is true only while the POST is in flight, not while the agent answers', async () => {
    const { deps, sendMessage } = makeDeps()
    let release!: (v: SendResponse) => void
    sendMessage.mockReturnValueOnce(new Promise((res) => { release = res }))
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    let pending!: Promise<void>
    await act(async () => { pending = result.current.send('hi') })
    expect(result.current.submitting).toBe(true)
    await act(async () => { release(receipt()); await pending })
    expect(result.current.submitting).toBe(false)
  })

  it('submits from one window go out one at a time in call order', async () => {
    const { deps, sendMessage } = makeDeps()
    const releases: Array<(v: SendResponse) => void> = []
    sendMessage.mockImplementation(() => new Promise((res) => { releases.push(res) }))
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    let both!: Promise<unknown>
    await act(async () => { both = Promise.all([result.current.send('one'), result.current.sendExact('req-2', 'two')]) })
    expect(sendMessage).toHaveBeenCalledTimes(1)
    await act(async () => { releases[0]!(receipt({ messageState: 'queued' })); await Promise.resolve() })
    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(2))
    expect(sendMessage.mock.calls.map((c) => c[1])).toEqual(['one', 'two'])
    await act(async () => { releases[1]!(receipt({ messageState: 'queued' })); await both })
  })

  it('a failed receipt is an error: the draft and quote stay and the id is not reused', async () => {
    const { deps, sendMessage } = makeDeps()
    sendMessage.mockResolvedValue(receipt({ messageState: 'failed', errorCode: 'context_limit', state: 'idle', turnId: '' }))
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    act(() => { result.current.openWith('Q') })
    act(() => { result.current.setDraft('hi') })
    await act(async () => { await result.current.send('hi') })
    expect(result.current.error).toBe('Conversation too long for one request')
    expect(result.current.draft).toBe('hi')
    expect(result.current.quote).toBe('Q')
    await act(async () => { await result.current.send('hi') })
    expect(sendMessage.mock.calls[0]?.[0]).not.toBe(sendMessage.mock.calls[1]?.[0])
  })

  it('a retry reuses the id only for an identical payload', async () => {
    const { deps, sendMessage } = makeDeps()
    sendMessage.mockRejectedValue(new Error('network'))
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { await result.current.send('a') })
    await act(async () => { await result.current.send('a') })
    await act(async () => { await result.current.send('b') })
    const ids = sendMessage.mock.calls.map((c) => c[0])
    expect(ids[0]).toBe(ids[1])
    expect(ids[2]).not.toBe(ids[0])
  })

  it('a terminal of an earlier turn does not drop the id of a failed submit', async () => {
    const { deps, sendMessage, emit } = makeDeps()
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { result.current.open() })
    await waitFor(() => expect(deps.openStream).toHaveBeenCalled())
    sendMessage.mockRejectedValueOnce(new Error('network'))
    await act(async () => { await result.current.send('hi') })
    act(() => { emit(wire(1, 'turn_completed', {}, 't0')) })
    await act(async () => { await result.current.send('hi') })
    expect(sendMessage.mock.calls[0]?.[0]).toBe(sendMessage.mock.calls[1]?.[0])
  })

  it('a send after a submit finished does not erase the next draft typed meanwhile (fast follow-up)', async () => {
    const { deps, sendMessage } = makeDeps()
    sendMessage.mockImplementation(async () => receipt({ messageState: 'queued', state: 'idle', turnId: '' }))
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    act(() => { result.current.setDraft('one') })
    await act(async () => { await result.current.send('one') })
    act(() => { result.current.setDraft('two') })
    await act(async () => { await result.current.send('two') })
    expect(result.current.draft).toBe('')
    expect(sendMessage).toHaveBeenCalledTimes(2)
  })

  describe('sendExact (caller-owned id and body)', () => {
    it('posts the caller id and the exact body, goes running and leaves the composer draft alone', async () => {
      const { deps, sendMessage } = makeDeps()
      const { result } = renderHook(() => useSideAgent('r1', true, deps, 'build'))
      act(() => { result.current.setDraft('typing') })
      await act(async () => { await result.current.sendExact('req-1', '  ## File comments\n- a  \n') })
      expect(sendMessage).toHaveBeenCalledWith('req-1', '  ## File comments\n- a  \n')
      expect(result.current.requestState).toBe('running')
      expect(result.current.draft).toBe('typing')
      expect(result.current.error).toBeNull()
    })

    it('posts while a turn runs and keeps the composer error clean', async () => {
      const { deps, sendMessage } = makeDeps()
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      await act(async () => { await result.current.send('first') })
      await act(async () => { await result.current.sendExact('req-1', 'body') })
      expect(sendMessage).toHaveBeenCalledTimes(2)
      expect(sendMessage).toHaveBeenLastCalledWith('req-1', 'body')
      expect(result.current.error).toBeNull()
    })

    it('rejects a failed receipt with its error code and leaves the composer error clean', async () => {
      const { deps, sendMessage } = makeDeps()
      sendMessage.mockResolvedValueOnce(receipt({ messageState: 'failed', errorCode: 'context_limit', state: 'idle', turnId: '' }))
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      let failure: unknown
      await act(async () => { failure = await result.current.sendExact('req-1', 'body').catch((e: unknown) => e) })
      expect(failure).toBeInstanceOf(SideAgentApiError)
      expect((failure as SideAgentApiError).code).toBe('context_limit')
      expect(result.current.error).toBeNull()
    })

    it('rejects with the API error and stays idle so the caller can retry the same id', async () => {
      const { deps, sendMessage } = makeDeps()
      sendMessage.mockRejectedValueOnce(new SideAgentApiError(413, 'message_too_large'))
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      let failure: unknown
      await act(async () => { failure = await result.current.sendExact('req-1', 'body').catch((e: unknown) => e) })
      expect((failure as SideAgentApiError).code).toBe('message_too_large')
      expect(result.current.requestState).toBe('idle')
      await act(async () => { await result.current.sendExact('req-1', 'body') })
      expect(sendMessage.mock.calls.map((c) => c[0])).toEqual(['req-1', 'req-1'])
    })

    it('rejects when the capability is off', async () => {
      const { deps, sendMessage } = makeDeps()
      const { result } = renderHook(() => useSideAgent('r1', false, deps))
      await expect(result.current.sendExact('req-1', 'body')).rejects.toThrow()
      expect(sendMessage).not.toHaveBeenCalled()
    })

    it('an idle idempotent answer catches the history up like a normal send', async () => {
      const completed = [
        parsed(1, 'turn_accepted', { message: 'q', client_message_id: 'req-1' }, 'tdone'),
        parsed(2, 'turn_completed', {}, 'tdone'),
      ]
      const getConversation = vi.fn(async () => emptyConversation({ state: 'idle', events: completed, lastSeq: 2, nextCursor: 2 }))
      const deps: SideAgentDeps = {
        getConversation,
        sendMessage: vi.fn(async () => (receipt({ turnId: 'tdone', state: 'idle', cursor: 2 }))),
        interrupt: vi.fn(async () => {}),
        removeQueued: vi.fn(),
        openStream: vi.fn(() => ({ close: vi.fn() })),
      }
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      await act(async () => { await result.current.sendExact('req-1', 'q') })
      await waitFor(() => expect(result.current.events).toHaveLength(2))
      expect(result.current.requestState).toBe('idle')
    })

    it('an accepted turn raises unseenResult when it finishes while the window is closed', async () => {
      const { deps, emit } = makeDeps()
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      await act(async () => { await result.current.sendExact('req-1', 'body') })
      await waitFor(() => expect(deps.openStream).toHaveBeenCalled())
      await act(async () => { emit(wire(1, 'turn_completed', {})) })
      expect(result.current.unseenResult).toBe(true)
      expect(result.current.requestState).toBe('idle')
    })

    it('a run change during the request drops the late acceptance', async () => {
      const { deps, sendMessage } = makeDeps()
      let release!: () => void
      sendMessage.mockImplementationOnce(async () => {
        await new Promise<void>((r) => { release = r })
        return receipt({ turnId: 't1', state: 'running', cursor: 1 })
      })
      const { result, rerender } = renderHook(({ runId }) => useSideAgent(runId, true, deps), { initialProps: { runId: 'r1' } })
      let pending!: Promise<void>
      await act(async () => { pending = result.current.sendExact('req-1', 'body') })
      rerender({ runId: 'r2' })
      await act(async () => { release(); await pending })
      expect(result.current.requestState).toBe('idle')
    })
  })

  it('a network error on send keeps the same client_message_id for retry', async () => {
    const { deps, sendMessage } = makeDeps()
    sendMessage.mockRejectedValueOnce(new Error('network'))
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { await result.current.send('hi') })
    expect(result.current.requestState).toBe('idle') // not accepted
    await act(async () => { await result.current.send('hi') })
    expect(sendMessage).toHaveBeenCalledTimes(2)
    expect(sendMessage.mock.calls[0]?.[0]).toBe(sendMessage.mock.calls[1]?.[0]) // same id reused
  })

  it('maps a SideAgentApiError code to a human-readable error', async () => {
    const { deps, sendMessage } = makeDeps()
    sendMessage.mockRejectedValueOnce(new SideAgentApiError(413, 'message_too_large'))
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { await result.current.send('x') })
    expect(result.current.error).toBe('Message too large')
  })

  it('ingests streamed events, dedupes by seq, and advances requestState on terminal', async () => {
    const { deps, emit } = makeDeps()
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { result.current.open() })
    await waitFor(() => expect(result.current.command).toBe('claude'))

    act(() => { emit(wire(1, 'turn_started', { command: 'claude', context_format_version: 'v1' })) })
    expect(result.current.requestState).toBe('running')
    act(() => {
      emit(wire(2, 'assistant_text', { text: 'answer' }))
      emit(wire(2, 'assistant_text', { text: 'answer' })) // duplicate seq ignored
    })
    expect(result.current.events.filter((e) => e.type === 'assistant_text')).toHaveLength(1)
    act(() => { emit(wire(3, 'turn_completed', {})) })
    expect(result.current.requestState).toBe('idle')
  })

  it('clears a failed turn banner when a later Mesh turn is accepted and completed while open', async () => {
    const { deps, emit } = makeDeps()
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { result.current.open() })
    await waitFor(() => expect(deps.openStream).toHaveBeenCalled())

    act(() => { emit(wire(1, 'turn_failed', { code: 'agent_error', message: 'failed' }, 't1')) })
    expect(result.current.error).toBe('The agent failed to answer')
    act(() => {
      emit(wire(2, 'turn_accepted', {
        message: 'help', client_message_id: 'mesh:m2', source_message_id: 'm2',
        source_sender_id: 'run:qa', source_sender_name: 'QA',
      }, 't2'))
      emit(wire(3, 'assistant_text', { text: 'done' }, 't2'))
      emit(wire(4, 'turn_completed', {}, 't2'))
    })
    expect(result.current.isOpen).toBe(true)
    expect(result.current.error).toBeNull()
    expect(result.current.events.map((e) => e.type)).toEqual(['turn_failed', 'turn_accepted', 'assistant_text', 'turn_completed'])
  })

  it('clears a failed turn banner when history sync contains a later Mesh acceptance', async () => {
    const { deps, getConversation } = makeDeps()
    getConversation.mockResolvedValueOnce(emptyConversation({
      state: 'idle', lastSeq: 4, events: [
        parsed(1, 'turn_failed', { code: 'agent_error', message: 'failed' }, 't1'),
        parsed(2, 'turn_accepted', {
          message: 'help', clientMessageId: 'mesh:m2', sourceMessageId: 'm2',
          sourceSenderId: 'run:qa', sourceSenderName: 'QA',
        }, 't2'),
        parsed(3, 'assistant_text', { text: 'done' }, 't2'),
        parsed(4, 'turn_completed', {}, 't2'),
      ],
    }))
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { result.current.open() })
    await waitFor(() => expect(result.current.events).toHaveLength(4))
    expect(result.current.error).toBeNull()
    expect(result.current.events[0]?.type).toBe('turn_failed')
  })

  it('a terminal event while the window is closed raises unseenResult', async () => {
    const { deps, emit } = makeDeps()
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { await result.current.send('q') }) // running keeps the stream alive
    await waitFor(() => expect(deps.openStream).toHaveBeenCalled())
    act(() => { emit(wire(5, 'turn_completed', {})) })
    expect(result.current.unseenResult).toBe(true)
    act(() => { result.current.open() })
    expect(result.current.unseenResult).toBe(false)
  })

  it('hydrates history and command from getConversation', async () => {
    const convo = (): ConversationResponse => emptyConversation({
      command: 'codex', commandName: 'Codex', state: 'idle',
      events: [parsed(1, 'turn_accepted', { message: 'prev', clientMessageId: 'm0' }), parsed(2, 'assistant_text', { text: 'prev answer' })],
      lastSeq: 2, nextCursor: 2,
    })
    const { deps } = makeDeps(convo)
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { result.current.open() })
    await waitFor(() => expect(result.current.events).toHaveLength(2))
    expect(result.current.command).toBe('codex')
    expect(result.current.commandName).toBe('Codex')
  })

  it('a run_id change invalidates the previous conversation (clears events/draft)', async () => {
    const { deps, emit } = makeDeps()
    const { result, rerender } = renderHook(({ runId }) => useSideAgent(runId, true, deps), {
      initialProps: { runId: 'r1' },
    })
    await act(async () => { result.current.open() })
    act(() => { emit(wire(1, 'assistant_text', { text: 'old' })) })
    act(() => { result.current.setDraft('leftover') })
    expect(result.current.events.length).toBeGreaterThan(0)

    rerender({ runId: 'r2' })
    expect(result.current.events).toHaveLength(0)
    expect(result.current.draft).toBe('')
  })

  it('interrupt() targets the last turn id', async () => {
    const { deps, interrupt, emit } = makeDeps()
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { result.current.open() })
    act(() => { emit(wire(1, 'turn_accepted', { message: 'q', client_message_id: 'c1' }, 'turn-xyz')) })
    act(() => { result.current.interrupt() })
    expect(interrupt).toHaveBeenCalledWith('turn-xyz')
  })

  it('interrupt() ignores a Mesh turn and a turn of unknown origin', async () => {
    const { deps, interrupt, emit } = makeDeps()
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { result.current.open() })
    act(() => { emit(wire(1, 'turn_accepted', { message: 'q', client_message_id: 'mesh:1', source_message_id: 'm1', source_sender_name: 'QA' }, 't-mesh')) })
    act(() => { result.current.interrupt() })
    act(() => { emit(wire(2, 'turn_completed', {}, 't-mesh')) })
    act(() => { emit(wire(3, 'turn_started', { command: 'x', context_format_version: 'v3' }, 't-unknown')) })
    act(() => { result.current.interrupt() })
    expect(interrupt).not.toHaveBeenCalled()
  })

  it('interrupt() hits the active turn, not a queued message that arrived later', async () => {
    const { deps, interrupt, emit } = makeDeps()
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { result.current.open() })
    act(() => {
      emit(wire(1, 'turn_accepted', { message: 'a', client_message_id: 'c1' }, 't-a'))
      emit(wire(2, 'message_queued', { message_id: 'm-b', client_message_id: 'c2', message: 'b' }, ''))
    })
    act(() => { result.current.interrupt() })
    expect(interrupt).toHaveBeenCalledWith('t-a')
    expect(result.current.queue.map((m) => m.messageId)).toEqual(['m-b'])
  })

  it('interrupt() right after send targets the turn learned from the history catch-up', async () => {
    const accepted = parsed(1, 'turn_accepted', { message: 'q', clientMessageId: 'c1' }, 't1')
    const { deps, interrupt } = makeDeps(() => emptyConversation({ state: 'running', events: [accepted], lastSeq: 1, nextCursor: 1 }))
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { await result.current.send('q') })
    await waitFor(() => expect(result.current.events).toHaveLength(1))
    act(() => { result.current.interrupt() })
    expect(interrupt).toHaveBeenCalledWith('t1')
  })

  it('a delayed POST response does not erase a draft typed while it was in flight', async () => {
    const { deps, sendMessage } = makeDeps()
    let resolveSend!: (v: SendResponse) => void
    sendMessage.mockReturnValueOnce(new Promise((res) => { resolveSend = res }))
    const { result } = renderHook(() => useSideAgent('r1', true, deps))

    act(() => { result.current.setDraft('first') })
    let sendPromise!: Promise<void>
    await act(async () => { sendPromise = result.current.send('first') })
    // Пользователь печатает следующее сообщение, пока 202 ещё в полёте.
    act(() => { result.current.setDraft('second message') })
    await act(async () => {
      resolveSend(receipt({ turnId: 't1', state: 'running', cursor: 1 }))
      await sendPromise
    })
    expect(result.current.draft).toBe('second message') // НЕ затёрт
  })

  it('a receipt does not erase an identical draft retyped while the POST was in flight', async () => {
    const { deps, sendMessage } = makeDeps()
    let resolveSend!: (v: SendResponse) => void
    sendMessage.mockReturnValueOnce(new Promise((res) => { resolveSend = res }))
    const { result } = renderHook(() => useSideAgent('r1', true, deps))

    act(() => { result.current.setDraft('repeat') })
    let sendPromise!: Promise<void>
    await act(async () => { sendPromise = result.current.send('repeat') })
    act(() => { result.current.setDraft('') })
    act(() => { result.current.setDraft('repeat') })
    await act(async () => {
      resolveSend(receipt({ turnId: 't1', state: 'running', cursor: 1 }))
      await sendPromise
    })
    expect(result.current.draft).toBe('repeat')
  })

  describe('queue', () => {
    const queuedEv = (seq: number, id: string, message = `text ${id}`) =>
      parsed(seq, 'message_queued', { messageId: id, clientMessageId: `c-${id}`, message, stageId: '' }, '')

    it('reopening restores the queue from the journal without any POST', async () => {
      const convo = () => emptyConversation({ state: 'idle', events: [queuedEv(1, 'b'), queuedEv(2, 'c')], lastSeq: 2, nextCursor: 2 })
      const { deps, sendMessage } = makeDeps(convo)
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      await act(async () => { result.current.open() })
      await waitFor(() => expect(result.current.queue.map((m) => m.messageId)).toEqual(['b', 'c']))
      expect(result.current.requestState).toBe('idle') // idle does not hide the queue
      expect(sendMessage).not.toHaveBeenCalled()
    })

    it('a non-empty queue keeps the stream alive with the window closed and drops it once empty', async () => {
      const { deps, openStream, emit } = makeDeps()
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      await act(async () => { await result.current.send('hi') })
      await waitFor(() => expect(openStream).toHaveBeenCalled())
      act(() => { emit(wire(1, 'message_queued', { message_id: 'b', client_message_id: 'c-b', message: 'b' }, '')) })
      act(() => { emit(wire(2, 'turn_completed', {}, 't1')) })
      expect(result.current.requestState).toBe('idle')
      expect(result.current.queue).toHaveLength(1)
      expect(deps.openStream).toHaveBeenCalledTimes(1) // still connected
      expect(result.current.isOpen).toBe(false)
    })

    it('pages through a long journal so the queue is complete', async () => {
      const getConversation = vi
        .fn<(afterSeq?: number) => Promise<ConversationResponse>>()
        .mockResolvedValueOnce(emptyConversation({ events: [queuedEv(1, 'a')], lastSeq: 2, nextCursor: 1, hasMore: true }))
        .mockResolvedValueOnce(emptyConversation({ events: [queuedEv(2, 'b')], lastSeq: 2, nextCursor: 2, hasMore: false }))
      const { deps } = makeDeps()
      const { result } = renderHook(() => useSideAgent('r1', true, { ...deps, getConversation }))
      await act(async () => { result.current.open() })
      await waitFor(() => expect(result.current.queue.map((m) => m.messageId)).toEqual(['a', 'b']))
      expect(getConversation.mock.calls.map((c) => c[0])).toEqual([0, 1])
    })

    it('Remove marks the entry as removing, then drops it when the journal confirms the cancel', async () => {
      const { deps, removeQueued, emit } = makeDeps()
      let release!: () => void
      removeQueued.mockImplementationOnce(() => new Promise((res) => {
        release = () => res({ messageId: 'b', messageState: 'cancelled' as const, cursor: 2 })
      }))
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      await act(async () => { result.current.open() })
      await waitFor(() => expect(deps.openStream).toHaveBeenCalled())
      act(() => { emit(wire(1, 'message_queued', { message_id: 'b', client_message_id: 'c-b', message: 'b' }, '')) })
      act(() => { result.current.removeQueued('b') })
      expect(result.current.removal).toEqual({ b: { status: 'removing' } })
      act(() => { result.current.removeQueued('b') }) // repeated click: no second DELETE
      expect(removeQueued).toHaveBeenCalledTimes(1)
      act(() => { emit(wire(2, 'message_cancelled', { message_id: 'b' }, '')) })
      await act(async () => { release() })
      await waitFor(() => expect(result.current.removal).toEqual({}))
      expect(result.current.queue).toEqual([])
    })

    it('a failed Remove keeps the entry with an error and can be retried', async () => {
      const { deps, removeQueued, emit } = makeDeps()
      removeQueued.mockRejectedValueOnce(new Error('network'))
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      await act(async () => { result.current.open() })
      await waitFor(() => expect(deps.openStream).toHaveBeenCalled())
      act(() => { emit(wire(1, 'message_queued', { message_id: 'b', client_message_id: 'c-b', message: 'b' }, '')) })
      await act(async () => { result.current.removeQueued('b') })
      await waitFor(() => expect(result.current.removal.b).toEqual({ status: 'error', message: 'Request failed' }))
      expect(result.current.queue).toHaveLength(1)
      await act(async () => { result.current.removeQueued('b') })
      await waitFor(() => expect(result.current.removal).toEqual({}))
      expect(removeQueued).toHaveBeenCalledTimes(2)
    })

    it('Remove racing a claim says the message is already being processed', async () => {
      const { deps, removeQueued, emit } = makeDeps()
      removeQueued.mockRejectedValueOnce(new SideAgentApiError(409, 'message_not_queued'))
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      await act(async () => { result.current.open() })
      await waitFor(() => expect(deps.openStream).toHaveBeenCalled())
      act(() => { emit(wire(1, 'message_queued', { message_id: 'b', client_message_id: 'c-b', message: 'b' }, '')) })
      await act(async () => { result.current.removeQueued('b') })
      await waitFor(() => expect(result.current.removal.b).toEqual({ status: 'error', message: 'Message is already being processed' }))
      expect(result.current.error).toBe('Message is already being processed')
    })

    it('a run change drops removal state and ignores a late Remove answer', async () => {
      const { deps, removeQueued } = makeDeps()
      let release!: () => void
      removeQueued.mockImplementationOnce(() => new Promise((res) => {
        release = () => res({ messageId: 'b', messageState: 'cancelled' as const, cursor: 2 })
      }))
      const { result, rerender } = renderHook(({ runId }) => useSideAgent(runId, true, deps), { initialProps: { runId: 'r1' } })
      act(() => { result.current.removeQueued('b') })
      rerender({ runId: 'r2' })
      expect(result.current.removal).toEqual({})
      await act(async () => { release() })
      expect(result.current.removal).toEqual({})
    })

    it('message_failed while the window is closed raises unseenResult', async () => {
      const { deps, emit } = makeDeps()
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      await act(async () => { await result.current.send('q') })
      await waitFor(() => expect(deps.openStream).toHaveBeenCalled())
      act(() => { emit(wire(1, 'message_failed', { message_id: 'b', code: 'context_limit', message: '' }, '')) })
      expect(result.current.unseenResult).toBe(true)
    })

    it('a run change with a request in flight does not touch the new conversation', async () => {
      const { deps, sendMessage } = makeDeps()
      let release!: (v: SendResponse) => void
      sendMessage.mockReturnValueOnce(new Promise((res) => { release = res }))
      const { result, rerender } = renderHook(({ runId }) => useSideAgent(runId, true, deps), { initialProps: { runId: 'r1' } })
      act(() => { result.current.setDraft('hi') })
      let pending!: Promise<void>
      await act(async () => { pending = result.current.send('hi') })
      rerender({ runId: 'r2' })
      act(() => { result.current.setDraft('new draft') })
      await act(async () => { release(receipt()); await pending })
      expect(result.current.draft).toBe('new draft')
      expect(result.current.submitting).toBe(false)
      expect(result.current.requestState).toBe('idle')
    })
  })

  describe('defaultStreamOpener', () => {
    it('subscribes to every event type of the catalog, including queue events', () => {
      const types: string[] = []
      class FakeEventSource {
        onerror: (() => void) | null = null
        addEventListener(type: string): void { types.push(type) }
        close(): void {}
      }
      vi.stubGlobal('EventSource', FakeEventSource)
      try {
        defaultStreamOpener(0, { onEvent: () => {}, onError: () => {} })
      } finally {
        vi.unstubAllGlobals()
      }
      expect(types).toEqual([...SIDE_AGENT_EVENT_TYPES])
      expect(types).toEqual(expect.arrayContaining(['message_queued', 'message_cancelled', 'message_failed']))
    })
  })

  // --- T4.3: per-turn stage context ---

  it('defaults contextStageId to the ambient currentStageId', () => {
    const { deps } = makeDeps()
    const { result } = renderHook(() => useSideAgent('r1', true, deps, 'frontend'))
    expect(result.current.contextStageId).toBe('frontend')
  })

  it('setContextStageId overrides the default before sending', () => {
    const { deps } = makeDeps()
    const { result } = renderHook(() => useSideAgent('r1', true, deps, 'frontend'))
    act(() => { result.current.setContextStageId('backend') })
    expect(result.current.contextStageId).toBe('backend')
  })

  it('closing the window forgets an explicit override — reopening defaults back to the current stage', async () => {
    const { deps } = makeDeps()
    const { result } = renderHook(() => useSideAgent('r1', true, deps, 'frontend'))
    await act(async () => { result.current.open() })
    act(() => { result.current.setContextStageId('backend') })
    expect(result.current.contextStageId).toBe('backend')
    act(() => { result.current.close() })
    expect(result.current.contextStageId).toBe('frontend')
  })

  it('send() transmits the current contextStageId as the stage of the outgoing turn', async () => {
    const { deps, sendMessage } = makeDeps()
    const { result } = renderHook(() => useSideAgent('r1', true, deps, 'frontend'))
    await act(async () => { await result.current.send('hello') })
    expect(sendMessage).toHaveBeenCalledWith(expect.any(String), 'hello', 'frontend')
  })

  // TestTurn_TargetFrozenAtSend — changing the dashboard's selected stage WHILE a send is in flight must not
  // retroactively change what that already-dispatched request carries.
  it('freezes the target at the moment of send: a later currentStageId change does not affect the in-flight request', async () => {
    const { deps, sendMessage } = makeDeps()
    let resolveSend!: (v: SendResponse) => void
    sendMessage.mockReturnValueOnce(new Promise((res) => { resolveSend = res }))
    const { result, rerender } = renderHook(
      ({ stageId }: { stageId: string }) => useSideAgent('r1', true, deps, stageId),
      { initialProps: { stageId: 'frontend' } },
    )

    let sendPromise!: Promise<void>
    await act(async () => { sendPromise = result.current.send('hello') })
    // The dashboard's selection changes WHILE the POST is in flight.
    rerender({ stageId: 'backend' })
    expect(result.current.contextStageId).toBe('backend') // UI reflects the new ambient selection...
    await act(async () => {
      resolveSend(receipt({ turnId: 't1', state: 'running', cursor: 1 }))
      await sendPromise
    })
    // ...but the request that was already dispatched still carries the stage id captured at send-time.
    expect(sendMessage).toHaveBeenCalledWith(expect.any(String), 'hello', 'frontend')
  })

  it('an idle idempotent response (completed turn, window closed) applies idle state and catches up history', async () => {
    // Повтор уже завершённого хода: POST отвечает state:'idle', нового SSE терминала не будет. Окно
    // закрыто (keepAlive=false) → connection-эффект не бежит; догон должен сделать сам send.
    const completed = [
      parsed(1, 'turn_accepted', { message: 'q', client_message_id: 'c1' }, 'tdone'),
      parsed(2, 'assistant_text', { text: 'the answer' }, 'tdone'),
      parsed(3, 'turn_completed', {}, 'tdone'),
    ]
    const getConversation = vi.fn(async () => emptyConversation({ state: 'idle', events: completed, lastSeq: 3, nextCursor: 3 }))
    const sendMessage = vi.fn(async () => (receipt({ turnId: 'tdone', state: 'idle', cursor: 3 })))
    const deps: SideAgentDeps = {
      getConversation,
      sendMessage,
      interrupt: vi.fn(async () => {}),
      removeQueued: vi.fn(),
      openStream: vi.fn(() => ({ close: vi.fn() })),
    }
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { await result.current.send('q') })
    await waitFor(() => expect(getConversation).toHaveBeenCalled()) // история догнана
    expect(result.current.requestState).toBe('idle') // НЕ форсировано running
    expect(result.current.events.some((e) => e.type === 'assistant_text')).toBe(true) // вывод восстановлен
    expect(deps.openStream).not.toHaveBeenCalled() // окно закрыто → SSE не нужен
  })

  it('a stale idle catch-up does not overwrite a newer running turn', async () => {
    // send A → idle с ОТЛОЖЕННЫМ догоном GET; пока он висит, send B принимается как running. Протухший
    // idle-GET A при резолве НЕ должен вернуть состояние в idle или подменить turn_id.
    let resolveStaleGet!: (v: ConversationResponse) => void
    const getConversation = vi
      .fn<(afterSeq?: number, limit?: number) => Promise<ConversationResponse>>()
      .mockImplementationOnce(() => new Promise<ConversationResponse>((res) => { resolveStaleGet = res }))
      .mockImplementation(async () => emptyConversation({
        state: 'running', lastSeq: 5, nextCursor: 5,
        events: [parsed(5, 'turn_accepted', { message: 'b', clientMessageId: 'cb' }, 'tnew')],
      }))
    let calls = 0
    const sendMessage = vi.fn(async (_id: string, _text: string) => {
      calls += 1
      return calls === 1
        ? receipt({ turnId: 'tdone', state: 'idle', cursor: 3 })
        : receipt({ turnId: 'tnew', state: 'running', cursor: 5 })
    })
    const interrupt = vi.fn(async () => {})
    const deps: SideAgentDeps = { getConversation, sendMessage, interrupt, removeQueued: vi.fn(), openStream: vi.fn(() => ({ close: vi.fn() })) }

    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { await result.current.send('a') }) // A: idle, deferred catch-up GET in flight
    expect(result.current.requestState).toBe('idle')
    await act(async () => { await result.current.send('b') }) // B: running (newer op)
    expect(result.current.requestState).toBe('running')

    // Резолвим ПРОТУХШИЙ idle-GET от A — он должен быть отброшен по op.
    await act(async () => {
      resolveStaleGet(emptyConversation({ state: 'idle', events: [parsed(3, 'turn_completed', {}, 'tdone')], lastSeq: 3 }))
      await Promise.resolve()
    })
    expect(result.current.requestState).toBe('running') // не откатилось в idle
    await waitFor(() => expect(result.current.activeSource).toEqual({ kind: 'modal' }))
    act(() => { result.current.interrupt() })
    expect(interrupt).toHaveBeenCalledWith('tnew') // Stop целит в НОВЫЙ ход, не в tdone
  })

  it('a stale idle history snapshot does not override a newer turn observed via SSE', async () => {
    // Окно открыто. send A → idle с ОТЛОЖЕННЫМ догоном GET.
    let handlers: StreamHandlers | null = null
    let resolveStaleGet!: (v: ConversationResponse) => void
    const getConversation = vi
      .fn<(afterSeq?: number, limit?: number) => Promise<ConversationResponse>>()
      .mockImplementationOnce(async () => emptyConversation({ state: 'idle', events: [], lastSeq: 0 })) // #1 connection-effect open
      .mockImplementationOnce(() => new Promise<ConversationResponse>((res) => { resolveStaleGet = res })) // #2 send A idle catch-up (deferred)
      .mockImplementation(async () => emptyConversation({ state: 'running', events: [], lastSeq: 7 }))
    const sendMessage = vi.fn(async (_id: string, _text: string) => (receipt({ turnId: 'tdone', state: 'idle', cursor: 3 })))
    const interrupt = vi.fn(async () => {})
    const openStream = vi.fn((_afterSeq: number, h: StreamHandlers) => { handlers = h; return { close: vi.fn() } })
    const deps: SideAgentDeps = { getConversation, sendMessage, interrupt, removeQueued: vi.fn(), openStream }

    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { result.current.open() })
    await waitFor(() => expect(openStream).toHaveBeenCalled())

    await act(async () => { await result.current.send('a') }) // idle; deferred catch-up GET in flight
    expect(result.current.requestState).toBe('idle')

    // Новый ход B приходит по SSE раньше ответа своего POST (op не поднят):
    act(() => {
      handlers!.onEvent(wire(6, 'turn_accepted', { message: 'b', client_message_id: 'cb' }, 'tnew'))
      handlers!.onEvent(wire(7, 'turn_started', { command: 'claude', context_format_version: 'v1' }, 'tnew'))
    })
    expect(result.current.requestState).toBe('running')

    // Протухший idle-GET A резолвится (lastSeq 3 < cursor 7) — отбрасывается.
    await act(async () => {
      resolveStaleGet(emptyConversation({ state: 'idle', events: [parsed(3, 'turn_completed', {}, 'tdone')], lastSeq: 3 }))
      await Promise.resolve()
    })
    expect(result.current.requestState).toBe('running') // не откатилось в idle
    act(() => { result.current.interrupt() })
    expect(interrupt).toHaveBeenCalledWith('tnew') // Stop целит в ход из SSE
  })

  it('interrupt() is a no-op when idle', () => {
    const { deps, interrupt } = makeDeps()
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    act(() => { result.current.interrupt() })
    expect(interrupt).not.toHaveBeenCalled()
  })

  it('closing the window while idle tears down the stream but keeps the cursor/events', async () => {
    const { deps, emit, close } = makeDeps()
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { result.current.open() })
    act(() => { emit(wire(1, 'assistant_text', { text: 'kept' })) })
    await act(async () => { result.current.close() })
    expect(close).toHaveBeenCalled()
    expect(result.current.events).toHaveLength(1) // state survives close
  })

  // quote — file comments attached from the file browser without pausing the flow.
  describe('quote', () => {
    it('starts as null', () => {
      const { deps } = makeDeps()
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      expect(result.current.quote).toBeNull()
    })

    it('openWith(quote) behaves like open() plus setting the quote', () => {
      const { deps } = makeDeps()
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      act(() => { result.current.openWith('Q') })
      expect(result.current.isOpen).toBe(true)
      expect(result.current.quote).toBe('Q')
      expect(result.current.unseenResult).toBe(false)
    })

    it('openWith replaces an already-set quote', () => {
      const { deps } = makeDeps()
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      act(() => { result.current.openWith('Q1') })
      act(() => { result.current.openWith('Q2') })
      expect(result.current.quote).toBe('Q2')
    })

    it('send() while a quote is set transmits buildReplyBody(quote, text) as the second argument', async () => {
      const { deps, sendMessage } = makeDeps()
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      act(() => { result.current.openWith('Q') })
      await act(async () => { await result.current.send('hi') })
      expect(sendMessage).toHaveBeenCalledTimes(1)
      expect(sendMessage.mock.calls[0]?.[1]).toBe(buildReplyBody('Q', 'hi'))
      expect(sendMessage.mock.calls[0]?.[1]).toBe('> Q\n\nhi')
    })

    it('clears the quote and the draft after a successful send', async () => {
      const { deps } = makeDeps()
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      act(() => { result.current.openWith('Q') })
      act(() => { result.current.setDraft('hi') })
      await act(async () => { await result.current.send('hi') })
      expect(result.current.quote).toBeNull()
      expect(result.current.draft).toBe('')
    })

    it('preserves the quote and draft when send() throws', async () => {
      const { deps, sendMessage } = makeDeps()
      sendMessage.mockRejectedValueOnce(new SideAgentApiError(413, 'message_too_large'))
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      act(() => { result.current.openWith('Q') })
      act(() => { result.current.setDraft('hi') })
      await act(async () => { await result.current.send('hi') })
      expect(result.current.quote).toBe('Q')
      expect(result.current.draft).toBe('hi')
    })

    it('a quote attached while a turn runs goes out with the next send', async () => {
      const { deps, sendMessage } = makeDeps()
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      await act(async () => { await result.current.send('first') })
      act(() => { result.current.openWith('Q') })
      await act(async () => { await result.current.send('second') })
      expect(sendMessage.mock.calls[1]?.[1]).toBe('> Q\n\nsecond')
      expect(result.current.quote).toBeNull()
    })

    it('clearQuote() clears the quote without changing isOpen', () => {
      const { deps } = makeDeps()
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      act(() => { result.current.openWith('Q') })
      act(() => { result.current.clearQuote() })
      expect(result.current.quote).toBeNull()
      expect(result.current.isOpen).toBe(true)
    })

    it('does not wipe a quote attached while an earlier send is still in flight', async () => {
      const { deps, sendMessage } = makeDeps()
      let resolveSend: (v: SendResponse) => void = () => {}
      sendMessage.mockImplementationOnce(() => new Promise((resolve) => { resolveSend = resolve }))
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      act(() => { result.current.openWith('Q1') })
      act(() => { result.current.setDraft('hi') })
      let sendPromise: Promise<void> = Promise.resolve()
      await act(async () => { sendPromise = result.current.send('hi') })
      // A second batch of file comments is attached while the first send (carrying Q1) is still awaiting
      // its response.
      act(() => { result.current.openWith('Q2') })
      await act(async () => {
        resolveSend(receipt({ turnId: 't1', state: 'running', cursor: 1 }))
        await sendPromise
      })
      expect(result.current.quote).toBe('Q2')
    })

    it('does not wipe a freshly-attached quote even when it is textually identical to the one that was sent', async () => {
      // Guards against comparing quotes by string value instead of by attachment identity/generation: a
      // second openWith() carrying the exact same text as the in-flight one must still survive.
      const { deps, sendMessage } = makeDeps()
      let resolveSend: (v: SendResponse) => void = () => {}
      sendMessage.mockImplementationOnce(() => new Promise((resolve) => { resolveSend = resolve }))
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      act(() => { result.current.openWith('same text') })
      act(() => { result.current.setDraft('hi') })
      let sendPromise: Promise<void> = Promise.resolve()
      await act(async () => { sendPromise = result.current.send('hi') })
      act(() => { result.current.openWith('same text') })
      await act(async () => {
        resolveSend(receipt({ turnId: 't1', state: 'running', cursor: 1 }))
        await sendPromise
      })
      expect(result.current.quote).toBe('same text')
    })

    it('a run_id change resets the quote to null', () => {
      const { deps } = makeDeps()
      const { result, rerender } = renderHook(({ runId }) => useSideAgent(runId, true, deps), {
        initialProps: { runId: 'r1' },
      })
      act(() => { result.current.openWith('Q') })
      expect(result.current.quote).toBe('Q')
      rerender({ runId: 'r2' })
      expect(result.current.quote).toBeNull()
    })
  })
})

describe('useSideAgent: владение id и поколением submit', () => {
  beforeEach(() => vi.spyOn(console, 'error').mockImplementation(() => {}))
  afterEach(() => vi.restoreAllMocks())

  it('a deferred submit queued before a run change is never sent through the new run deps', async () => {
    const old = makeDeps()
    const next = makeDeps()
    let release!: (v: SendResponse) => void
    old.sendMessage.mockReturnValueOnce(new Promise((res) => { release = res }))
    const { result, rerender } = renderHook(({ runId, d }) => useSideAgent(runId, true, d), {
      initialProps: { runId: 'r1', d: old.deps },
    })
    let both!: Promise<unknown>
    await act(async () => { both = Promise.allSettled([result.current.send('first'), result.current.sendExact('req-old', 'second')]) })
    expect(old.sendMessage).toHaveBeenCalledTimes(1)
    rerender({ runId: 'r2', d: next.deps })
    await act(async () => { release(receipt()); await both })
    expect(next.sendMessage).not.toHaveBeenCalled()
    expect(old.sendMessage).toHaveBeenCalledTimes(1)
  })

  it('a submit made in the new run does not wait for a hanging POST of the old run', async () => {
    const old = makeDeps()
    const next = makeDeps()
    old.sendMessage.mockReturnValueOnce(new Promise(() => {}))
    const { result, rerender } = renderHook(({ runId, d }) => useSideAgent(runId, true, d), {
      initialProps: { runId: 'r1', d: old.deps },
    })
    await act(async () => { void result.current.send('old') })
    rerender({ runId: 'r2', d: next.deps })
    await act(async () => { await result.current.send('fresh') })
    expect(next.sendMessage).toHaveBeenCalledTimes(1)
  })

  it('two same-payload submits before the first receipt get independent ids', async () => {
    const { deps, sendMessage } = makeDeps()
    let release!: (v: SendResponse) => void
    sendMessage.mockReturnValueOnce(new Promise((res) => { release = res }))
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    let both!: Promise<unknown>
    await act(async () => { both = Promise.all([result.current.send('same'), result.current.send('same')]) })
    await act(async () => { release(receipt()); await both })
    expect(sendMessage).toHaveBeenCalledTimes(2)
    expect(sendMessage.mock.calls[1]?.[0]).not.toBe(sendMessage.mock.calls[0]?.[0])
  })

  it('a failed network attempt still lets the identical retry reuse its id', async () => {
    const { deps, sendMessage } = makeDeps()
    sendMessage.mockRejectedValueOnce(new Error('offline'))
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { await result.current.send('again') })
    await act(async () => { await result.current.send('again') })
    expect(sendMessage.mock.calls[1]?.[0]).toBe(sendMessage.mock.calls[0]?.[0])
  })

  it('keeps the Remove conflict explanation after the catch-up loads the winning claim', async () => {
    const queued = parsed(1, 'message_queued', { messageId: 'm', clientMessageId: 'cid', message: 'queued B', stageId: '' }, '')
    const claimed = parsed(2, 'turn_accepted', { message: '', clientMessageId: '', messageIds: ['m'] }, 't-next')
    let afterClaim = false
    const { deps, removeQueued } = makeDeps(() => (afterClaim
      ? emptyConversation({ state: 'running', events: [claimed], lastSeq: 2, nextCursor: 2 })
      : emptyConversation({ events: [queued], lastSeq: 1, nextCursor: 1 })))
    removeQueued.mockImplementation(async () => { afterClaim = true; throw new SideAgentApiError(409, 'message_not_queued') })
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { result.current.open() })
    await waitFor(() => expect(result.current.queue).toHaveLength(1))
    await act(async () => { result.current.removeQueued('m') })
    await waitFor(() => expect(result.current.queue).toHaveLength(0))
    expect(result.current.error).toBe('Message is already being processed')
  })

  it.each(['queued', 'failed'] as const)(
    'a late failure of submit A cannot lend its id to a third submit after B receipt=%s',
    async (messageState) => {
      const { deps, sendMessage } = makeDeps()
      let rejectFirst!: (e: Error) => void
      sendMessage
        .mockImplementationOnce(() => new Promise<SendResponse>((_, rej) => { rejectFirst = rej }))
        .mockResolvedValue(receipt({ messageState, errorCode: messageState === 'failed' ? 'context_limit' : '' }))
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      let both!: Promise<unknown>
      await act(async () => { both = Promise.all([result.current.send('same'), result.current.send('same')]) })
      await act(async () => { rejectFirst(new Error('response lost')); await both })
      await act(async () => { await result.current.send('same') })
      const ids = sendMessage.mock.calls.map((c) => c[0])
      expect(ids[1]).not.toBe(ids[0])
      expect(ids[2]).not.toBe(ids[0])
      expect(ids[2]).not.toBe(ids[1])
    },
  )

  it('a sequential network retry reuses its id, a failed receipt retires it', async () => {
    const { deps, sendMessage } = makeDeps()
    sendMessage
      .mockRejectedValueOnce(new Error('network'))
      .mockResolvedValueOnce(receipt({ messageState: 'failed', errorCode: 'context_limit' }))
      .mockResolvedValue(receipt())
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { await result.current.send('same') })
    await act(async () => { await result.current.send('same') })
    expect(result.current.error).toBe('Conversation too long for one request')
    await act(async () => { await result.current.send('same') })
    expect(sendMessage.mock.calls[1]?.[0]).toBe(sendMessage.mock.calls[0]?.[0])
    expect(sendMessage.mock.calls[2]?.[0]).not.toBe(sendMessage.mock.calls[1]?.[0])
  })

  it('when both concurrent same-payload submits fail, the retry reuses the id of the latest one', async () => {
    const { deps, sendMessage } = makeDeps()
    let rejectFirst!: (e: Error) => void
    let rejectSecond!: (e: Error) => void
    sendMessage
      .mockImplementationOnce(() => new Promise<SendResponse>((_, rej) => { rejectFirst = rej }))
      .mockImplementationOnce(() => new Promise<SendResponse>((_, rej) => { rejectSecond = rej }))
      .mockResolvedValue(receipt())
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    let a!: Promise<unknown>
    let b!: Promise<unknown>
    await act(async () => { a = result.current.send('same'); b = result.current.send('same') })
    await act(async () => { rejectFirst(new Error('net')); await a })
    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(2))
    await act(async () => { rejectSecond(new Error('net')); await b })
    await act(async () => { await result.current.send('same') })
    const ids = sendMessage.mock.calls.map((c) => c[0])
    expect(ids[2]).toBe(ids[1])
    expect(ids[2]).not.toBe(ids[0])
  })

  it('a failed submit is forgotten on run change: the same text gets a fresh id', async () => {
    const first = makeDeps()
    const next = makeDeps()
    first.sendMessage.mockRejectedValueOnce(new Error('net'))
    const { result, rerender } = renderHook(({ runId, d }) => useSideAgent(runId, true, d), {
      initialProps: { runId: 'r1', d: first.deps },
    })
    await act(async () => { await result.current.send('same') })
    rerender({ runId: 'r2', d: next.deps })
    await act(async () => { await result.current.send('same') })
    expect(next.sendMessage.mock.calls[0]?.[0]).not.toBe(first.sendMessage.mock.calls[0]?.[0])
  })

  it('a send failure survives an unrelated claim; a new submit clears it', async () => {
    const { deps, sendMessage, emit } = makeDeps()
    sendMessage.mockRejectedValueOnce(new SideAgentApiError(413, 'message_too_large'))
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { result.current.open() })
    await waitFor(() => expect(deps.openStream).toHaveBeenCalled())
    await act(async () => { await result.current.send('big') })
    expect(result.current.error).toBe('Message too large')
    act(() => emit(wire(1, 'turn_accepted', { message: '', client_message_id: '', message_ids: ['other'] }, 't9')))
    expect(result.current.error).toBe('Message too large')
    await act(async () => { await result.current.send('small') })
    expect(result.current.error).toBeNull()
  })

  it('an interrupt failure is about the turn: the next accepted turn clears it', async () => {
    const { deps, interrupt, emit } = makeDeps()
    interrupt.mockRejectedValue(new SideAgentApiError(409, 'turn_not_running'))
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { result.current.open() })
    await waitFor(() => expect(deps.openStream).toHaveBeenCalled())
    act(() => emit(wire(1, 'turn_accepted', { message: 'q', client_message_id: 'c1' }, 't1')))
    await act(async () => { result.current.interrupt() })
    await waitFor(() => expect(result.current.error).not.toBeNull())
    act(() => emit(wire(2, 'turn_completed', {}, 't1')))
    act(() => emit(wire(3, 'turn_accepted', { message: 'n', client_message_id: 'c2' }, 't2')))
    expect(result.current.error).toBeNull()
  })

  it('the Remove conflict survives a failed catch-up and the winning claim arriving through SSE', async () => {
    const queued = parsed(1, 'message_queued', { messageId: 'm', clientMessageId: 'cid', message: 'B', stageId: '' }, '')
    let afterClaim = false
    const { deps, removeQueued, getConversation, emit } = makeDeps(() => emptyConversation({ events: [queued], lastSeq: 1, nextCursor: 1 }))
    const loadQueued = getConversation.getMockImplementation()!
    getConversation.mockImplementation(async () => {
      if (afterClaim) throw new Error('temporary network loss')
      return loadQueued()
    })
    removeQueued.mockImplementation(async () => { afterClaim = true; throw new SideAgentApiError(409, 'message_not_queued') })
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { result.current.open() })
    await waitFor(() => expect(deps.openStream).toHaveBeenCalled())
    await act(async () => { result.current.removeQueued('m') })
    await waitFor(() => expect(result.current.error).toBe('Message is already being processed'))
    act(() => emit(wire(2, 'turn_accepted', { message: '', client_message_id: '', message_ids: ['m'] }, 't1')))
    expect(result.current.queue).toHaveLength(0)
    expect(result.current.error).toBe('Message is already being processed')
  })

  it('the Remove conflict survives the winning claim arriving through SSE after a successful catch-up', async () => {
    const queued = parsed(1, 'message_queued', { messageId: 'm', clientMessageId: 'cid', message: 'B', stageId: '' }, '')
    const { deps, removeQueued, emit } = makeDeps(() => emptyConversation({ events: [queued], lastSeq: 1, nextCursor: 1 }))
    removeQueued.mockRejectedValue(new SideAgentApiError(409, 'message_not_queued'))
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { result.current.open() })
    await waitFor(() => expect(deps.openStream).toHaveBeenCalled())
    await act(async () => { result.current.removeQueued('m') })
    await waitFor(() => expect(result.current.error).toBe('Message is already being processed'))
    act(() => emit(wire(2, 'turn_accepted', { message: '', client_message_id: '', message_ids: ['m'] }, 't1')))
    expect(result.current.error).toBe('Message is already being processed')
  })
})
