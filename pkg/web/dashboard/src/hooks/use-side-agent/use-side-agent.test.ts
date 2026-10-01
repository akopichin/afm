import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useSideAgent, type SideAgentDeps, type StreamHandlers } from './use-side-agent'
import type { ConversationResponse } from '../../api/side-agent-client'
import { SideAgentApiError } from '../../api/side-agent-client'
import type { SideAgentEvent } from '../../types/side-agent'

// Готовый parsed-event (camelCase) для ответа getConversation.
function parsed(seq: number, type: SideAgentEvent['type'], data: unknown, turnId = 't1'): SideAgentEvent {
  return { schemaVersion: 1, seq, conversationId: 'c1', turnId, timestamp: '2026-10-01T10:00:00Z', type, data } as SideAgentEvent
}

// Сырой wire-кадр (snake_case) для стрима — хук сам зовёт parseSideAgentEvent.
function wire(seq: number, type: string, data: unknown, turnId = 't1'): unknown {
  return { schema_version: 1, seq, conversation_id: 'c1', turn_id: turnId, type, timestamp: '2026-10-01T10:00:00Z', data }
}

function emptyConversation(over: Partial<ConversationResponse> = {}): ConversationResponse {
  return {
    conversationId: 'c1', runId: 'r1', command: 'claude', unavailable: false,
    state: 'idle', events: [], lastSeq: 0, nextCursor: 0, hasMore: false, ...over,
  }
}

function makeDeps(convo: () => ConversationResponse = emptyConversation) {
  let handlers: StreamHandlers | null = null
  const close = vi.fn()
  const openStream = vi.fn((_afterSeq: number, h: StreamHandlers) => {
    handlers = h
    return { close }
  })
  // serverState моделирует авторитетное состояние беседы на бэкенде: пока ни
  // одного принятого сообщения — берётся из convo(); успешный sendMessage (202)
  // переводит его в 'running', поэтому последующий getConversation (его зовёт
  // connection-эффект после send) не откатывает requestState назад в idle.
  let serverState: 'idle' | 'running' | null = null
  const getConversation = vi.fn(async () => {
    const c = convo()
    return serverState === null ? c : { ...c, state: serverState }
  })
  const sendMessage = vi.fn(async (_clientMessageId: string, _text: string) => {
    serverState = 'running'
    return { turnId: 't1', state: 'running' as const, cursor: 1 }
  })
  const interrupt = vi.fn(async () => {})
  const deps: SideAgentDeps = { getConversation, sendMessage, interrupt, openStream }
  return { deps, openStream, close, getConversation, sendMessage, interrupt, emit: (raw: unknown) => handlers?.onEvent(raw) }
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

  it('a second send while running is rejected as busy (no second POST)', async () => {
    const { deps, sendMessage } = makeDeps()
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { await result.current.send('first') })
    await act(async () => { await result.current.send('second') })
    expect(sendMessage).toHaveBeenCalledTimes(1)
    expect(result.current.error).toBe('Another request is in progress')
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
      command: 'codex', state: 'idle',
      events: [parsed(1, 'turn_accepted', { message: 'prev', clientMessageId: 'm0' }), parsed(2, 'assistant_text', { text: 'prev answer' })],
      lastSeq: 2, nextCursor: 2,
    })
    const { deps } = makeDeps(convo)
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { result.current.open() })
    await waitFor(() => expect(result.current.events).toHaveLength(2))
    expect(result.current.command).toBe('codex')
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
    act(() => { emit(wire(1, 'turn_started', { command: 'claude', context_format_version: 'v1' }, 'turn-xyz')) })
    act(() => { result.current.interrupt() })
    expect(interrupt).toHaveBeenCalledWith('turn-xyz')
  })

  it('interrupt() right after send targets the turn from the POST response (no event needed)', async () => {
    // Дефолтный sendMessage возвращает turnId 't1' и переводит беседу в running.
    const { deps, interrupt } = makeDeps()
    const { result } = renderHook(() => useSideAgent('r1', true, deps))
    await act(async () => { await result.current.send('q') })
    // Прерываем СРАЗУ, до любого SSE/GET-события: turnId взят из ответа POST.
    act(() => { result.current.interrupt() })
    expect(interrupt).toHaveBeenCalledWith('t1')
  })

  it('a delayed POST response does not erase a draft typed while it was in flight', async () => {
    const { deps, sendMessage } = makeDeps()
    let resolveSend!: (v: { turnId: string; state: 'running'; cursor: number }) => void
    sendMessage.mockReturnValueOnce(new Promise((res) => { resolveSend = res }))
    const { result } = renderHook(() => useSideAgent('r1', true, deps))

    act(() => { result.current.setDraft('first') })
    let sendPromise!: Promise<void>
    act(() => { sendPromise = result.current.send('first') })
    // Пользователь печатает следующее сообщение, пока 202 ещё в полёте.
    act(() => { result.current.setDraft('second message') })
    await act(async () => {
      resolveSend({ turnId: 't1', state: 'running', cursor: 1 })
      await sendPromise
    })
    expect(result.current.draft).toBe('second message') // НЕ затёрт
  })

  it('an idle idempotent response (completed turn, window closed) applies idle state and catches up history', async () => {
    // Повтор уже завершённого хода: POST отвечает state:'idle', нового SSE
    // терминала не будет. Окно закрыто (keepAlive=false) → connection-эффект не
    // бежит; догон должен сделать сам send.
    const completed = [
      parsed(1, 'turn_accepted', { message: 'q', client_message_id: 'c1' }, 'tdone'),
      parsed(2, 'assistant_text', { text: 'the answer' }, 'tdone'),
      parsed(3, 'turn_completed', {}, 'tdone'),
    ]
    const getConversation = vi.fn(async () => emptyConversation({ state: 'idle', events: completed, lastSeq: 3, nextCursor: 3 }))
    const sendMessage = vi.fn(async () => ({ turnId: 'tdone', state: 'idle' as const, cursor: 3 }))
    const deps: SideAgentDeps = {
      getConversation,
      sendMessage,
      interrupt: vi.fn(async () => {}),
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
    // send A → idle (идемпотентный повтор завершённого) с ОТЛОЖЕННЫМ догоном GET;
    // пока он висит, send B принимается как running. Протухший idle-GET A при
    // резолве НЕ должен вернуть состояние в idle или подменить turn_id.
    let resolveStaleGet!: (v: ConversationResponse) => void
    const getConversation = vi
      .fn<(afterSeq?: number, limit?: number) => Promise<ConversationResponse>>()
      .mockImplementationOnce(() => new Promise<ConversationResponse>((res) => { resolveStaleGet = res }))
      .mockImplementation(async () => emptyConversation({ state: 'running', events: [], lastSeq: 0 }))
    let calls = 0
    const sendMessage = vi.fn(async (_id: string, _text: string) => {
      calls += 1
      return calls === 1
        ? { turnId: 'tdone', state: 'idle' as const, cursor: 3 }
        : { turnId: 'tnew', state: 'running' as const, cursor: 5 }
    })
    const interrupt = vi.fn(async () => {})
    const deps: SideAgentDeps = { getConversation, sendMessage, interrupt, openStream: vi.fn(() => ({ close: vi.fn() })) }

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
    act(() => { result.current.interrupt() })
    expect(interrupt).toHaveBeenCalledWith('tnew') // Stop целит в НОВЫЙ ход, не в tdone
  })

  it('a stale idle history snapshot does not override a newer turn observed via SSE', async () => {
    // Окно открыто (SSE активен). send A → idle с ОТЛОЖЕННЫМ догоном GET. Пока он
    // висит, по стриму приходит НОВЫЙ ход B (turn_accepted/turn_started) — его POST
    // ещё не вернулся, поэтому op НЕ поднят. Протухший idle-GET A при резолве не
    // должен откатить running→idle: его отсекает lastSeq < cursor.
    let handlers: StreamHandlers | null = null
    let resolveStaleGet!: (v: ConversationResponse) => void
    const getConversation = vi
      .fn<(afterSeq?: number, limit?: number) => Promise<ConversationResponse>>()
      .mockImplementationOnce(async () => emptyConversation({ state: 'idle', events: [], lastSeq: 0 })) // #1 connection-effect open
      .mockImplementationOnce(() => new Promise<ConversationResponse>((res) => { resolveStaleGet = res })) // #2 send A idle catch-up (deferred)
      .mockImplementation(async () => emptyConversation({ state: 'running', events: [], lastSeq: 7 }))
    const sendMessage = vi.fn(async (_id: string, _text: string) => ({ turnId: 'tdone', state: 'idle' as const, cursor: 3 }))
    const interrupt = vi.fn(async () => {})
    const openStream = vi.fn((_afterSeq: number, h: StreamHandlers) => { handlers = h; return { close: vi.fn() } })
    const deps: SideAgentDeps = { getConversation, sendMessage, interrupt, openStream }

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
})
