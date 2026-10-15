import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useSideAgent, type SideAgentDeps, type StreamHandlers } from './use-side-agent'
import type { ConversationResponse } from '../../api/side-agent-client'
import { SideAgentApiError } from '../../api/side-agent-client'
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
  // serverState после принятого Send становится running, чтобы последующий getConversation не откатил UI в
  // idle.
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
    let resolveSend!: (v: { turnId: string; state: 'running'; cursor: number }) => void
    sendMessage.mockReturnValueOnce(new Promise((res) => { resolveSend = res }))
    const { result, rerender } = renderHook(
      ({ stageId }: { stageId: string }) => useSideAgent('r1', true, deps, stageId),
      { initialProps: { stageId: 'frontend' } },
    )

    let sendPromise!: Promise<void>
    act(() => { sendPromise = result.current.send('hello') })
    // The dashboard's selection changes WHILE the POST is in flight.
    rerender({ stageId: 'backend' })
    expect(result.current.contextStageId).toBe('backend') // UI reflects the new ambient selection...
    await act(async () => {
      resolveSend({ turnId: 't1', state: 'running', cursor: 1 })
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
    // send A → idle с ОТЛОЖЕННЫМ догоном GET; пока он висит, send B принимается как running. Протухший
    // idle-GET A при резолве НЕ должен вернуть состояние в idle или подменить turn_id.
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
    // Окно открыто. send A → idle с ОТЛОЖЕННЫМ догоном GET.
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

    it('does not touch the quote on the early busy return', async () => {
      const { deps, sendMessage } = makeDeps()
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      await act(async () => { await result.current.send('first') }) // goes running (no quote yet, clears to null)
      act(() => { result.current.openWith('Q') }) // attach a quote while a request is already in flight
      sendMessage.mockClear()
      await act(async () => { await result.current.send('second') }) // busy early-exit
      expect(sendMessage).not.toHaveBeenCalled()
      expect(result.current.quote).toBe('Q')
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
      let resolveSend: (v: { turnId: string; state: 'running'; cursor: number }) => void = () => {}
      sendMessage.mockImplementationOnce(() => new Promise((resolve) => { resolveSend = resolve }))
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      act(() => { result.current.openWith('Q1') })
      act(() => { result.current.setDraft('hi') })
      let sendPromise: Promise<void> = Promise.resolve()
      act(() => { sendPromise = result.current.send('hi') })
      // A second batch of file comments is attached while the first send (carrying Q1) is still awaiting
      // its response.
      act(() => { result.current.openWith('Q2') })
      await act(async () => {
        resolveSend({ turnId: 't1', state: 'running', cursor: 1 })
        await sendPromise
      })
      expect(result.current.quote).toBe('Q2')
    })

    it('does not wipe a freshly-attached quote even when it is textually identical to the one that was sent', async () => {
      // Guards against comparing quotes by string value instead of by attachment identity/generation: a
      // second openWith() carrying the exact same text as the in-flight one must still survive.
      const { deps, sendMessage } = makeDeps()
      let resolveSend: (v: { turnId: string; state: 'running'; cursor: number }) => void = () => {}
      sendMessage.mockImplementationOnce(() => new Promise((resolve) => { resolveSend = resolve }))
      const { result } = renderHook(() => useSideAgent('r1', true, deps))
      act(() => { result.current.openWith('same text') })
      act(() => { result.current.setDraft('hi') })
      let sendPromise: Promise<void> = Promise.resolve()
      act(() => { sendPromise = result.current.send('hi') })
      act(() => { result.current.openWith('same text') })
      await act(async () => {
        resolveSend({ turnId: 't1', state: 'running', cursor: 1 })
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
