import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import {
  getConversation as defaultGetConversation,
  interrupt as defaultInterrupt,
  removeQueued as defaultRemoveQueued,
  sendMessage as defaultSendMessage,
  streamUrl,
  SideAgentApiError,
  type ConversationResponse,
  type RemoveResponse,
  type SendResponse,
} from '../../api/side-agent-client'
import {
  SIDE_AGENT_EVENT_TYPES,
  errorCodeToMessage,
  parseSideAgentEvent,
  type SideAgentEvent,
} from '../../types/side-agent'
import { buildReplyBody } from '../../lib/quote'
import { projectSideAgent, type ActiveTurnSource, type QueuedMessage } from './side-agent-projection'

// StreamHandlers/StreamHandle/StreamOpener — SSE-подписка за инъектируемой функцией: прод открывает
// EventSource, тесты подставляют управляемый опенер и «присылают» события вручную.
export type StreamHandlers = {
  onEvent: (raw: unknown) => void
  onError: () => void
}
export type StreamHandle = { close: () => void }
export type StreamOpener = (afterSeq: number, handlers: StreamHandlers) => StreamHandle

export type SideAgentDeps = {
  getConversation: (afterSeq?: number, limit?: number) => Promise<ConversationResponse>
  sendMessage: (clientMessageId: string, text: string, stageId?: string) => Promise<SendResponse>
  interrupt: (turnId: string) => Promise<void>
  removeQueued: (messageId: string) => Promise<RemoveResponse>
  openStream: StreamOpener
}

// defaultStreamOpener — прод-подписка через EventSource. EventSource сам переподключается, отдавая
// Last-Event-ID (у сервера он в приоритете над after_seq).
export function defaultStreamOpener(afterSeq: number, handlers: StreamHandlers): StreamHandle {
  const es = new EventSource(streamUrl(afterSeq))
  const listener = (ev: MessageEvent): void => {
    try {
      handlers.onEvent(JSON.parse(ev.data as string))
    } catch {
      /* битый кадр — пропускаем, поток живёт */
    }
  }
  for (const type of SIDE_AGENT_EVENT_TYPES) es.addEventListener(type, listener as EventListener)
  es.onerror = (): void => handlers.onError()
  return { close: () => es.close() }
}

const DEFAULT_DEPS: SideAgentDeps = {
  getConversation: defaultGetConversation,
  sendMessage: defaultSendMessage,
  interrupt: defaultInterrupt,
  removeQueued: defaultRemoveQueued,
  openStream: defaultStreamOpener,
}

// QueueRemoval — состояние удаления записи очереди: ждём API либо показываем ошибку с повтором.
export type QueueRemoval = { status: 'removing' } | { status: 'error'; message: string }

// SideAgentApi — то, что потребляет F-wave: читаемый разговор, черновик композера, окно, флаг
// непросмотренного результата и действия.
export type SideAgentApi = {
  events: SideAgentEvent[]
  requestState: 'idle' | 'running'
  activeSource: ActiveTurnSource
  // queue — принятые сервером, но ещё не взятые в ход сообщения; submitting — идёт только HTTP-приём.
  queue: QueuedMessage[]
  removal: Readonly<Record<string, QueueRemoval>>
  removeQueued: (messageId: string) => void
  submitting: boolean
  unavailable: boolean
  command: string
  commandName: string | undefined
  draft: string
  setDraft: (value: string) => void
  isOpen: boolean
  open: () => void
  close: () => void
  unseenResult: boolean
  error: string | null
  send: (text: string) => Promise<void>
  // sendExact — отправка чужого пакета (файловые комментарии): id и тело задаёт вызывающий и сам повторяет
  // тот же id. Отказ (API-ошибка, failed-квитанция) уходит ему в reject, а не в error модалки; композер не трогается.
  sendExact: (clientMessageId: string, body: string) => Promise<void>
  interrupt: () => void
  quote: string | null
  openWith: (quote: string) => void
  clearQuote: () => void
  // contextStageId — which stage this conversation is "about" right now (T4.3): shown as "Context: …" in
  // the modal header. Defaults to currentStageId (the caller's ambient "currently selected stage", e.g. the
  // dashboard rail selection) and can be overridden via setContextStageId before sending; the override is
  // forgotten once the window closes, so reopening defaults back to the current selection.
  contextStageId: string
  setContextStageId: (stageId: string) => void
}

// failed-квитанция: приём состоялся (202), но сообщение не будет обработано — для вызывающего это отказ.
function receiptFailure(resp: SendResponse): SideAgentApiError {
  return new SideAgentApiError(202, resp.errorCode)
}

function newClientMessageId(): string {
  const c = globalThis.crypto
  if (typeof c?.randomUUID === 'function') return c.randomUUID()
  return `m-${Date.now()}-${Math.random().toString(16).slice(2)}`
}

// useSideAgent — app-level source of truth для «бокового» разговора. Ленивое соединение: SSE живёт, пока
// окно открыто ИЛИ идёт запрос. currentStageId — ambient "currently selected stage" (e.g. the dashboard
// rail selection), used as contextStageId's default (T4.3).
export function useSideAgent(runId: string, enabled: boolean, deps: SideAgentDeps = DEFAULT_DEPS, currentStageId = ''): SideAgentApi {
  const [events, setEvents] = useState<SideAgentEvent[]>([])
  const [requestState, setRequestState] = useState<'idle' | 'running'>('idle')
  const [unavailable, setUnavailable] = useState(false)
  const [command, setCommand] = useState('')
  const [commandName, setCommandName] = useState<string | undefined>(undefined)
  const [draft, setDraftState] = useState('')
  // Ревизия черновика растёт на каждую правку: квитанция чистит поле, только если после отправки правок не было.
  const draftRevRef = useRef(0)
  const setDraft = useCallback((value: string) => {
    draftRevRef.current += 1
    setDraftState(value)
  }, [])
  const [isOpen, setIsOpen] = useState(false)
  const [unseenResult, setUnseenResult] = useState(false)
  // Ошибка помнит свой предмет: 'turn' — ход (её снимает следующий turn_accepted), 'message' — исход действия
  // пользователя над сообщением (отправка, Remove): его снимает только новый submit или смена run.
  const [error, setError] = useState<{ message: string; scope: 'turn' | 'message' } | null>(null)
  const [submitting, setSubmitting] = useState(0)
  const [removal, setRemoval] = useState<Record<string, QueueRemoval>>({})
  const [quote, setQuote] = useState<string | null>(null)
  // contextOverride — explicit choice made in the modal (setContextStageId), null = "follow currentStageId".
  // Cleared whenever the window closes so a later reopen defaults back to the ambient current stage.
  const [contextOverride, setContextOverride] = useState<string | null>(null)

  const cursorRef = useRef(0)
  const genRef = useRef(0)
  // opRef — монотонная ревизия операций в пределах ОДНОЙ беседы.
  const opRef = useRef(0)
  const runIdRef = useRef(runId)
  // Последний submit композера. Повтор с тем же payload переиспользует id, только если именно он провалился
  // без квитанции; запись принадлежит своему submit — более старый, упавший позже, её уже не меняет.
  const lastSubmitRef = useRef<{ id: string; body: string; stageId: string; failed: boolean } | null>(null)
  // Приём из одного окна идёт строго по очереди: порядок POST = порядок вызовов.
  const submitChainRef = useRef<Promise<void>>(Promise.resolve())
  const removingRef = useRef(new Set<string>())
  const streamRef = useRef<StreamHandle | null>(null)
  // deps — через ref, чтобы идентичность объекта не перезапускала connection-эффект
  // (прод-deps константны; тесты передают стабильный объект).
  const depsRef = useRef(deps)
  depsRef.current = deps
  const isOpenRef = useRef(isOpen)
  isOpenRef.current = isOpen
  const requestStateRef = useRef(requestState)
  requestStateRef.current = requestState
  const projection = useMemo(() => projectSideAgent(events), [events])
  const projectionRef = useRef(projection)
  projectionRef.current = projection
  const quoteRef = useRef(quote)
  quoteRef.current = quote
  // quoteGenRef — bumped on every quote change (attach/clear/reset). send() compares THIS, not the quote
  // string itself, before clearing it on success: two different attachments can carry byte-identical text
  // (e.g. the same single-line comment re-sent), so a value comparison alone could wrongly clear a fresh
  // attachment that just happens to match the one that was actually sent.
  const quoteGenRef = useRef(0)

  // contextStageId — derived, not its own piece of independent state: defaults to the ambient
  // currentStageId, overridden only by an explicit setContextStageId call. contextStageIdRef mirrors it so
  // send() can snapshot the value synchronously before its await — the same "freeze at the moment of the
  // call" pattern quoteRef/quoteGenRef use just above, needed so a currentStageId change that arrives WHILE
  // a send is in flight cannot retroactively change what that already-dispatched request carries
  // (dev-plan.md T4.3's TestTurn_TargetFrozenAtSend).
  const contextStageId = contextOverride ?? currentStageId
  const contextStageIdRef = useRef(contextStageId)
  contextStageIdRef.current = contextStageId

  // Closing the window forgets any explicit override — reopening later defaults back to whatever is
  // currently selected in the dashboard, rather than a stale choice from a previous conversation.
  useEffect(() => {
    if (!isOpen) setContextOverride(null)
  }, [isOpen])

  // Смена run_id = другой прогон → другой разговор. layout-эффект (а не passive) — чтобы инвалидация
  // случилась ДО connection-эффекта ниже.
  useLayoutEffect(() => {
    if (runIdRef.current === runId) return
    runIdRef.current = runId
    genRef.current += 1
    streamRef.current?.close()
    streamRef.current = null
    cursorRef.current = 0
    lastSubmitRef.current = null
    submitChainRef.current = Promise.resolve()
    removingRef.current.clear()
    setSubmitting(0)
    setRemoval({})
    setEvents([])
    setRequestState('idle')
    setUnavailable(false)
    setCommand('')
    setCommandName(undefined)
    setDraft('')
    setUnseenResult(false)
    setError(null)
    setQuote(null)
    quoteGenRef.current += 1
  }, [runId])

  // ingest — применяет пачку событий с дедупом по монотонному seq.
  const ingest = useCallback((incoming: SideAgentEvent[]): void => {
    const fresh = incoming.filter((e) => e.seq > cursorRef.current)
    if (fresh.length === 0) return
    for (const e of fresh) if (e.seq > cursorRef.current) cursorRef.current = e.seq
    setEvents((prev) => [...prev, ...fresh])
    for (const e of fresh) {
      if (e.type === 'turn_accepted') setError((cur) => (cur?.scope === 'turn' ? null : cur))
      if (e.type === 'message_failed' && !isOpenRef.current) setUnseenResult(true)
      if (e.type === 'turn_accepted' || e.type === 'turn_started') {
        setRequestState('running')
      } else if (e.type === 'turn_completed' || e.type === 'turn_failed' || e.type === 'turn_interrupted') {
        setRequestState('idle')
        if (!isOpenRef.current) setUnseenResult(true)
        if (e.type === 'turn_failed') setError({ message: errorCodeToMessage(e.data.code), scope: 'turn' })
      }
    }
  }, [])

  // syncHistory догоняет хвост журнала с курсора и применяет авторитетное состояние из ответа. Единый
  // примитив для connection-эффекта И для догона после idle-идемпотентного send.
  const syncHistory = useCallback(async (shouldApply: () => boolean): Promise<void> => {
    // Очередь выводится из полного журнала, поэтому добираем все страницы, а не только первую.
    for (;;) {
      const before = cursorRef.current
      const resp = await depsRef.current.getConversation(before)
      // Протухший снапшот: его хвост ПОЗАДИ уже применённого курсора — значит live-события ушли вперёд.
      if (!shouldApply() || resp.lastSeq < cursorRef.current) return
      setUnavailable(resp.unavailable)
      setCommand(resp.command)
      setCommandName(resp.commandName)
      ingest(resp.events)
      // Авторитетное состояние из ответа — на случай, если пачка событий его не
      // покрыла (напр. running-ход, чьи события уже были за курсором).
      setRequestState(resp.state)
      if (!resp.hasMore || cursorRef.current === before) return
    }
  }, [ingest])

  // keepAlive — соединение живёт, пока окно открыто, идёт запрос ИЛИ в очереди есть сообщения. Закрытие
  // окна его НЕ рвёт (серверно-принятая работа продолжает стримиться).
  const keepAlive = enabled && (isOpen || requestState === 'running' || projection.queue.length > 0)
  useEffect(() => {
    if (!keepAlive) {
      streamRef.current?.close()
      streamRef.current = null
      return
    }
    let active = true
    const gen = genRef.current
    const op = opRef.current
    const connect = (): void => {
      if (!active || gen !== genRef.current || streamRef.current !== null) return
      streamRef.current = depsRef.current.openStream(cursorRef.current, {
        onEvent: (raw) => {
          if (gen !== genRef.current) return
          const ev = parseSideAgentEvent(raw)
          if (ev !== null) ingest([ev])
        },
        onError: () => {
          /* EventSource переподключается сам (Last-Event-ID) */
        },
      })
    }
    // Догоняем хвост журнала с курсора, затем открываем live-подписку. Догон не удался — всё равно
    // поднимаем подписку (деградация), чтобы события доходили.
    syncHistory(() => active && gen === genRef.current && op === opRef.current)
      .catch(() => {})
      .finally(() => {
        if (active && gen === genRef.current) connect()
      })
    return () => {
      active = false
      streamRef.current?.close()
      streamRef.current = null
    }
  }, [keepAlive, runId, syncHistory])

  // acceptReceipt — общий хвост принятого сервером сообщения (обычный send и sendExact). Принятое — новейшая
  // операция: поднимаем op, чтобы летящий догон истории не применил устаревший снапшот; затем один догон
  // показывает очередь/ход/ошибку даже при закрытом окне.
  const acceptReceipt = useCallback(
    (resp: SendResponse, gen: number): void => {
      opRef.current += 1
      const op = opRef.current
      if (resp.state === 'running') setRequestState('running')
      syncHistory(() => gen === genRef.current && op === opRef.current).catch(() => {})
    },
    [syncHistory],
  )

  // serialize ставит приём в общую очередь и считает submitting на время HTTP. Отложенная задача, чей run
  // успел смениться, не выполняется: её payload относится к чужой беседе.
  const serialize = useCallback((task: () => Promise<void>): Promise<void> => {
    const gen = genRef.current
    setSubmitting((n) => n + 1)
    const run = submitChainRef.current.then(() => {
      if (gen !== genRef.current) throw new Error('Run changed')
      return task()
    })
    submitChainRef.current = run.then(() => {}, () => {})
    return run.finally(() => {
      if (gen === genRef.current) setSubmitting((n) => n - 1)
    })
  }, [])

  const send = useCallback(
    (text: string): Promise<void> => {
      if (!enabled) return Promise.resolve()
      const gen = genRef.current
      // Снимок цитаты (и её поколения) на момент ОТПРАВКИ: если за время await пользователь прикрепит
      // новую (напр. ещё один батч file-comments из файл-браузера), успешный ответ ДОЛЖЕН снять только ту
      // цитату, что реально ушла с этим запросом — не свежеприкреплённую.
      const sentQuote = quoteRef.current
      const sentQuoteGen = quoteGenRef.current
      // Snapshot BEFORE the await — see contextStageIdRef's doc comment above.
      const sentStageId = contextStageIdRef.current
      const sentDraftRev = draftRevRef.current
      const body = buildReplyBody(sentQuote, text)
      const prev = lastSubmitRef.current
      const clientMessageId = prev?.failed && prev.body === body && prev.stageId === sentStageId ? prev.id : newClientMessageId()
      const submit = { id: clientMessageId, body, stageId: sentStageId, failed: false }
      lastSubmitRef.current = submit
      setError(null)
      return serialize(async () => {
        let received = false
        try {
          const resp = await depsRef.current.sendMessage(clientMessageId, body, sentStageId)
          if (gen !== genRef.current) return
          received = true
          acceptReceipt(resp, gen)
          if (resp.messageState === 'failed') throw receiptFailure(resp)
          // Чистим черновик, только если его не правили с отправки: следующее сообщение, набранное пока 202
          // был в полёте (даже идентичное по тексту), сохраняется — поле редактируемо во время работы агента.
          if (draftRevRef.current === sentDraftRev) setDraftState((cur) => (cur === text ? '' : cur))
          // Снимаем цитату, только если её поколение не изменилось с момента отправки — иначе успешный
          // ответ стёр бы свежеприкреплённую, в том числе когда новая цитата текстуально совпадает со старой.
          if (quoteGenRef.current === sentQuoteGen) setQuote(null)
        } catch (err: unknown) {
          if (gen !== genRef.current) return
          // Без квитанции id переживает ошибку — повтор того же payload использует его. Принятое сервером
          // (в т.ч. failed) повтором не исправить: следующий submit берёт новый id.
          if (!received && lastSubmitRef.current === submit) lastSubmitRef.current = { ...submit, failed: true }
          const code = err instanceof SideAgentApiError ? err.code : undefined
          setError({ message: code !== undefined ? errorCodeToMessage(code) : 'Request failed', scope: 'message' })
        }
      }).catch(() => {})
    },
    [enabled, serialize, acceptReceipt],
  )

  const sendExact = useCallback(
    async (clientMessageId: string, body: string): Promise<void> => {
      if (!enabled) throw new Error('Assistant unavailable')
      const gen = genRef.current
      await serialize(async () => {
        const resp = await depsRef.current.sendMessage(clientMessageId, body)
        // Ран сменился во время запроса: принятие относится к чужой беседе.
        if (gen !== genRef.current) return
        acceptReceipt(resp, gen)
        if (resp.messageState === 'failed') throw receiptFailure(resp)
      })
    },
    [enabled, serialize, acceptReceipt],
  )

  const removeQueued = useCallback(
    (messageId: string): void => {
      if (removingRef.current.has(messageId)) return
      const gen = genRef.current
      removingRef.current.add(messageId)
      setRemoval((r) => ({ ...r, [messageId]: { status: 'removing' } }))
      const settle = (next: QueueRemoval | null): void => {
        removingRef.current.delete(messageId)
        setRemoval(({ [messageId]: _dropped, ...rest }) => (next === null ? rest : { ...rest, [messageId]: next }))
      }
      const catchUp = (): Promise<void> => {
        opRef.current += 1
        const op = opRef.current
        return syncHistory(() => gen === genRef.current && op === opRef.current).catch(() => {})
      }
      depsRef.current.removeQueued(messageId).then(
        async () => {
          if (gen !== genRef.current) return
          // Запись исчезнет из очереди вместе с событием message_cancelled: ждём догон, чтобы она не мигнула.
          await catchUp()
          if (gen === genRef.current) settle(null)
        },
        async (err: unknown) => {
          if (gen !== genRef.current) return
          const code = err instanceof SideAgentApiError ? err.code : undefined
          const message = code !== undefined ? errorCodeToMessage(code) : 'Request failed'
          settle({ status: 'error', message })
          // Гонка с claim: запись уже в ходе — объясняем причину и подтягиваем журнал.
          if (code === 'message_not_queued') {
            setError({ message, scope: 'message' })
            await catchUp()
          }
        },
      ).catch(() => {})
    },
    [syncHistory],
  )

  const interruptTurn = useCallback((): void => {
    const { activeTurnId, activeSource } = projectionRef.current
    if (requestStateRef.current !== 'running' || activeSource?.kind !== 'modal') return
    const gen = genRef.current
    void depsRef.current.interrupt(activeTurnId).catch((err: unknown) => {
      if (gen !== genRef.current) return
      const code = err instanceof SideAgentApiError ? err.code : undefined
      setError({ message: code !== undefined ? errorCodeToMessage(code) : 'Request failed', scope: 'turn' })
    })
  }, [])

  const open = useCallback((): void => {
    setIsOpen(true)
    setUnseenResult(false)
  }, [])

  const close = useCallback((): void => {
    setIsOpen(false)
  }, [])

  const openWith = useCallback(
    (q: string): void => {
      quoteGenRef.current += 1
      setQuote(q)
      open()
    },
    [open],
  )

  const clearQuote = useCallback((): void => {
    quoteGenRef.current += 1
    setQuote(null)
  }, [])

  return {
    events,
    requestState,
    activeSource: requestState === 'running' ? projection.activeSource : null,
    queue: projection.queue,
    removal,
    removeQueued,
    submitting: submitting > 0,
    unavailable,
    command,
    commandName,
    draft,
    setDraft,
    isOpen,
    open,
    close,
    unseenResult,
    error: error?.message ?? null,
    send,
    sendExact,
    interrupt: interruptTurn,
    quote,
    openWith,
    clearQuote,
    contextStageId,
    setContextStageId: setContextOverride,
  }
}
