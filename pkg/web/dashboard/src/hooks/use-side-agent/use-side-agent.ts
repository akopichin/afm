import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import {
  getConversation as defaultGetConversation,
  interrupt as defaultInterrupt,
  sendMessage as defaultSendMessage,
  streamUrl,
  SideAgentApiError,
  type ConversationResponse,
  type SendResponse,
} from '../../api/side-agent-client'
import {
  SIDE_AGENT_EVENT_TYPES,
  errorCodeToMessage,
  parseSideAgentEvent,
  type SideAgentEvent,
} from '../../types/side-agent'
import { buildReplyBody } from '../../lib/quote'

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
  openStream: defaultStreamOpener,
}

// SideAgentApi — то, что потребляет F-wave: читаемый разговор, черновик композера, окно, флаг
// непросмотренного результата и действия.
export type SideAgentApi = {
  events: SideAgentEvent[]
  requestState: 'idle' | 'running'
  activeSource: ActiveTurnSource
  unavailable: boolean
  command: string
  draft: string
  setDraft: (value: string) => void
  isOpen: boolean
  open: () => void
  close: () => void
  unseenResult: boolean
  error: string | null
  send: (text: string) => Promise<void>
  // sendExact — отправка чужого пакета (файловые комментарии): id и тело задаёт вызывающий и сам повторяет
  // тот же id. Отказ (busy, API-ошибка) уходит ему в reject, а не в error модалки; композер не трогается.
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

export type ActiveTurnSource = { kind: 'modal' } | { kind: 'mesh'; sender: string } | null

// The authoritative running flag can arrive before its accepted event. Until that event is known,
// keep the origin unknown so the modal cannot stop a peer's turn.
export function activeTurnSource(events: readonly SideAgentEvent[], state: SideAgentApi['requestState']): ActiveTurnSource {
  if (state !== 'running') return null
  const accepted = [...events].reverse().find((e) => e.type === 'turn_accepted')
  if (accepted?.type !== 'turn_accepted') return null
  // A newer turn may have started while its acceptance frame is still being replayed.
  if (events.some((e) => e.seq > accepted.seq && e.type === 'turn_started' && e.turnId !== accepted.turnId)) return null
  if (events.some((e) => e.turnId === accepted.turnId && e.seq > accepted.seq &&
    (e.type === 'turn_completed' || e.type === 'turn_failed' || e.type === 'turn_interrupted'))) return null
  if (accepted.data.sourceMessageId !== undefined) {
    return { kind: 'mesh', sender: accepted.data.sourceSenderName || accepted.data.sourceSenderId || 'Unknown sender' }
  }
  return { kind: 'modal' }
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
  const [draft, setDraft] = useState('')
  const [isOpen, setIsOpen] = useState(false)
  const [unseenResult, setUnseenResult] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [quote, setQuote] = useState<string | null>(null)
  // contextOverride — explicit choice made in the modal (setContextStageId), null = "follow currentStageId".
  // Cleared whenever the window closes so a later reopen defaults back to the ambient current stage.
  const [contextOverride, setContextOverride] = useState<string | null>(null)

  const cursorRef = useRef(0)
  const genRef = useRef(0)
  // opRef — монотонная ревизия операций в пределах ОДНОЙ беседы.
  const opRef = useRef(0)
  const runIdRef = useRef(runId)
  // Клиентский id текущего (ещё не подтверждённого сервером) сообщения. Держится до успешного POST —
  // повтор после сетевой ошибки переиспользует тот же id, бэкенд возвращает тот же ход без дубля.
  const inflightIdRef = useRef<string | null>(null)
  const lastTurnIdRef = useRef('')
  const streamRef = useRef<StreamHandle | null>(null)
  // deps — через ref, чтобы идентичность объекта не перезапускала connection-эффект
  // (прод-deps константны; тесты передают стабильный объект).
  const depsRef = useRef(deps)
  depsRef.current = deps
  const isOpenRef = useRef(isOpen)
  isOpenRef.current = isOpen
  const requestStateRef = useRef(requestState)
  requestStateRef.current = requestState
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
    inflightIdRef.current = null
    lastTurnIdRef.current = ''
    setEvents([])
    setRequestState('idle')
    setUnavailable(false)
    setCommand('')
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
      if (e.type === 'turn_accepted') setError(null)
      if (e.type === 'turn_accepted' || e.type === 'turn_started') {
        lastTurnIdRef.current = e.turnId
        setRequestState('running')
      } else if (e.type === 'turn_completed' || e.type === 'turn_failed' || e.type === 'turn_interrupted') {
        inflightIdRef.current = null
        setRequestState('idle')
        if (!isOpenRef.current) setUnseenResult(true)
        if (e.type === 'turn_failed') setError(errorCodeToMessage(e.data.code))
      }
    }
  }, [])

  // syncHistory догоняет хвост журнала с курсора и применяет авторитетное состояние из ответа. Единый
  // примитив для connection-эффекта И для догона после idle-идемпотентного send.
  const syncHistory = useCallback(async (shouldApply: () => boolean): Promise<void> => {
    const resp = await depsRef.current.getConversation(cursorRef.current)
    // Протухший снапшот: его хвост ПОЗАДИ уже применённого курсора — значит live-события ушли вперёд.
    if (!shouldApply() || resp.lastSeq < cursorRef.current) return
    setUnavailable(resp.unavailable)
    setCommand(resp.command)
    ingest(resp.events)
    // Авторитетное состояние из ответа — на случай, если пачка событий его не
    // покрыла (напр. running-ход, чьи события уже были за курсором).
    setRequestState(resp.state)
  }, [ingest])

  // keepAlive — соединение живёт, пока окно открыто ИЛИ идёт запрос. Закрытие окна
  // при running его НЕ рвёт (серверно-принятая работа продолжает стримиться).
  const keepAlive = enabled && (isOpen || requestState === 'running')
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

  // acceptTurn — общий хвост принятого сервером сообщения (обычный send и sendExact): ход заведён, состояние
  // берём из ответа, а не форсируем running.
  const acceptTurn = useCallback(
    (resp: SendResponse, gen: number): void => {
      // Принятое сообщение — новейшая операция: поднимаем op, чтобы любой ещё летящий догон истории
      // (начатый до него) не применил свой снапшот.
      opRef.current += 1
      const op = opRef.current
      if (resp.turnId !== '') lastTurnIdRef.current = resp.turnId
      // Идемпотентный повтор уже ЗАВЕРШЁННОГО хода возвращает state:'idle' — агент не запускался, нового
      // терминального события не будет.
      setRequestState(resp.state)
      // running: keepAlive поднимется и connection-эффект догонит историю. idle: терминал SSE не придёт —
      // догоняем сами, чтобы восстановить пропущенный вывод завершённого хода.
      if (resp.state !== 'running') void syncHistory(() => gen === genRef.current && op === opRef.current)
    },
    [syncHistory],
  )

  const send = useCallback(
    async (text: string): Promise<void> => {
      if (!enabled) return
      if (requestStateRef.current === 'running') {
        setError(errorCodeToMessage('busy'))
        return
      }
      const gen = genRef.current
      const clientMessageId = inflightIdRef.current ?? newClientMessageId()
      inflightIdRef.current = clientMessageId
      setError(null)
      // Снимок цитаты (и её поколения) на момент ОТПРАВКИ: если за время await пользователь прикрепит
      // новую (напр. ещё один батч file-comments из файл-браузера), успешный ответ ДОЛЖЕН снять только ту
      // цитату, что реально ушла с этим запросом — не свежеприкреплённую.
      const sentQuote = quoteRef.current
      const sentQuoteGen = quoteGenRef.current
      // Snapshot BEFORE the await — see contextStageIdRef's doc comment above.
      const sentStageId = contextStageIdRef.current
      try {
        const body = buildReplyBody(sentQuote, text)
        const resp = await depsRef.current.sendMessage(clientMessageId, body, sentStageId)
        if (gen !== genRef.current) return
        acceptTurn(resp, gen)
        // Чистим id (следующий свежий send возьмёт новый).
        inflightIdRef.current = null
        // Чистим ТОЛЬКО черновик, равный отправленному: если пользователь успел напечатать следующее
        // сообщение, пока 202 был в полёте, его текст сохраняется (поле редактируемо во время работы агента).
        setDraft((cur) => (cur === text ? '' : cur))
        // Снимаем цитату, только если её поколение не изменилось с момента отправки — иначе успешный
        // ответ стёр бы свежеприкреплённую (гонка с openWith/clearQuote, вызванными, пока этот send ещё в
        // полёте), в том числе когда новая цитата текстуально совпадает со старой.
        if (quoteGenRef.current === sentQuoteGen) setQuote(null)
      } catch (err: unknown) {
        if (gen !== genRef.current) return
        // id СОХРАНЯЕМ — повтор после сетевой ошибки переиспользует его.
        const code = err instanceof SideAgentApiError ? err.code : undefined
        setError(code !== undefined ? errorCodeToMessage(code) : 'Request failed')
      }
    },
    [enabled, syncHistory, acceptTurn],
  )

  const sendExact = useCallback(
    async (clientMessageId: string, body: string): Promise<void> => {
      if (!enabled) throw new Error('Assistant unavailable')
      if (requestStateRef.current === 'running') throw new SideAgentApiError(409, 'busy')
      const gen = genRef.current
      const resp = await depsRef.current.sendMessage(clientMessageId, body)
      // Ран сменился во время запроса: принятие относится к чужой беседе.
      if (gen !== genRef.current) return
      acceptTurn(resp, gen)
    },
    [enabled, acceptTurn],
  )

  const interruptTurn = useCallback((): void => {
    if (requestStateRef.current !== 'running') return
    const turnId = lastTurnIdRef.current
    if (turnId === '') return
    const gen = genRef.current
    void depsRef.current.interrupt(turnId).catch((err: unknown) => {
      if (gen !== genRef.current) return
      const code = err instanceof SideAgentApiError ? err.code : undefined
      setError(code !== undefined ? errorCodeToMessage(code) : 'Request failed')
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
    activeSource: activeTurnSource(events, requestState),
    unavailable,
    command,
    draft,
    setDraft,
    isOpen,
    open,
    close,
    unseenResult,
    error,
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
