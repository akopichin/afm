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

// StreamHandlers/StreamHandle/StreamOpener — SSE-подписка за инъектируемой
// функцией: прод открывает EventSource, тесты (jsdom без EventSource) подставляют
// управляемый опенер и «присылают» события вручную. onEvent получает СЫРОЙ кадр
// (хук сам зовёт parseSideAgentEvent) — опенеру не нужно знать схему.
export type StreamHandlers = {
  onEvent: (raw: unknown) => void
  onError: () => void
}
export type StreamHandle = { close: () => void }
export type StreamOpener = (afterSeq: number, handlers: StreamHandlers) => StreamHandle

export type SideAgentDeps = {
  getConversation: (afterSeq?: number, limit?: number) => Promise<ConversationResponse>
  sendMessage: (clientMessageId: string, text: string) => Promise<SendResponse>
  interrupt: (turnId: string) => Promise<void>
  openStream: StreamOpener
}

// defaultStreamOpener — прод-подписка через EventSource. Сервер шлёт ИМЕНОВАННЫЕ
// SSE-события (event: <type>), поэтому onmessage не срабатывает — слушатель
// вешается на каждый известный тип. EventSource сам переподключается, отдавая
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

// SideAgentApi — то, что потребляет F-wave (модалка/шорткат/App): читаемый
// разговор (events/requestState/unavailable/command), черновик композера
// (draft/setDraft), окно (isOpen/open/close), флаг непросмотренного результата
// (unseenResult) и действия (send/interrupt). error — последняя человекочитаемая
// ошибка (для показа в UI), null если ошибок нет.
export type SideAgentApi = {
  events: SideAgentEvent[]
  requestState: 'idle' | 'running'
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
  interrupt: () => void
}

function newClientMessageId(): string {
  const c = globalThis.crypto
  if (typeof c?.randomUUID === 'function') return c.randomUUID()
  return `m-${Date.now()}-${Math.random().toString(16).slice(2)}`
}

// useSideAgent — app-level source of truth для «бокового» разговора. Держит
// черновик, события, состояние запроса, курсор, флаг непросмотренного результата,
// клиентский id сообщения (идемпотентность) и поколение, привязанное к run_id.
//
// Ленивое соединение: SSE живёт, пока окно открыто ИЛИ идёт запрос. Закрытие окна
// при простое рвёт соединение (но НЕ инвалидирует принятую сервером работу — курсор
// и события сохраняются); следующее открытие догоняет хвост журнала через
// getConversation и переоткрывает стрим с курсора. Смена run_id инвалидирует
// прежний разговор целиком (поколение, чистка состояния).
export function useSideAgent(runId: string, enabled: boolean, deps: SideAgentDeps = DEFAULT_DEPS): SideAgentApi {
  const [events, setEvents] = useState<SideAgentEvent[]>([])
  const [requestState, setRequestState] = useState<'idle' | 'running'>('idle')
  const [unavailable, setUnavailable] = useState(false)
  const [command, setCommand] = useState('')
  const [draft, setDraft] = useState('')
  const [isOpen, setIsOpen] = useState(false)
  const [unseenResult, setUnseenResult] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const cursorRef = useRef(0)
  const genRef = useRef(0)
  // opRef — монотонная ревизия операций в пределах ОДНОЙ беседы. Каждый принятый
  // send её поднимает; асинхронный догон истории (syncHistory) запоминает op на
  // старте и применяет снапшот, только если op ещё актуален — иначе протухший
  // idle-снапшот мог бы перезатереть уже начавшийся новый running-ход (gen один
  // и тот же, поэтому gen-проверки недостаточно).
  const opRef = useRef(0)
  const runIdRef = useRef(runId)
  // Клиентский id текущего (ещё не подтверждённого сервером) сообщения. Держится до
  // успешного POST — повтор после сетевой ошибки переиспользует тот же id, бэкенд
  // возвращает тот же ход без дубля.
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

  // Смена run_id = другой прогон → другой разговор. Поднимаем поколение (хвосты
  // старых запросов/стрима отбросятся по проверке gen), закрываем стрим и чистим всё
  // состояние прежнего разговора. layout-эффект (а не passive) — чтобы инвалидация
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
  }, [runId])

  // ingest — применяет пачку событий с дедупом по монотонному seq (курсор).
  // Обновляет requestState по стартовым/терминальным событиям хода; терминал при
  // закрытом окне поднимает флаг непросмотренного результата.
  const ingest = useCallback((incoming: SideAgentEvent[]): void => {
    const fresh = incoming.filter((e) => e.seq > cursorRef.current)
    if (fresh.length === 0) return
    for (const e of fresh) if (e.seq > cursorRef.current) cursorRef.current = e.seq
    setEvents((prev) => [...prev, ...fresh])
    for (const e of fresh) {
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

  // syncHistory догоняет хвост журнала с курсора и применяет авторитетное
  // состояние из ответа (unavailable/command/события/requestState). shouldApply
  // гейтит запись устаревшего ответа (смена поколения / размонтирование / более
  // новый локальный send через op). Единый примитив для connection-эффекта И для
  // догона после idle-идемпотентного send.
  const syncHistory = useCallback(async (shouldApply: () => boolean): Promise<void> => {
    const resp = await depsRef.current.getConversation(cursorRef.current)
    // Протухший снапшот: его хвост (lastSeq) ПОЗАДИ уже применённого курсора —
    // значит live-события (SSE) ушли вперёд (новый ход пришёл по стриму раньше,
    // чем вернулся его POST — op тогда ещё не поднят, одного op-гейта мало).
    // Применять resp.state было бы вредно (откатило бы running→idle); сами
    // события всё равно отсеялись бы дедупом по seq в ingest.
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
    // Догоняем хвост журнала с курсора, затем открываем live-подписку. Догон не
    // удался — всё равно поднимаем подписку (деградация), чтобы события доходили.
    // op-проверка: снапшот не применяем, если его обогнал новый send.
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
      try {
        const resp = await depsRef.current.sendMessage(clientMessageId, text)
        if (gen !== genRef.current) return
        // Принятый send — новейшая операция: поднимаем op, чтобы любой ещё
        // летящий догон истории (начатый до этого send) не применил свой снапшот.
        opRef.current += 1
        const op = opRef.current
        // Принято сервером: ход заведён. Запоминаем turn_id из ответа СРАЗУ —
        // чтобы Stop адресовал именно этот ход, не дожидаясь первого SSE/GET-
        // события (иначе клик Stop сразу после 202 был бы no-op).
        if (resp.turnId !== '') lastTurnIdRef.current = resp.turnId
        // Чистим id (следующий свежий send возьмёт новый).
        inflightIdRef.current = null
        // Чистим ТОЛЬКО черновик, равный отправленному: если пользователь успел
        // напечатать следующее сообщение, пока 202 был в полёте, его текст
        // сохраняется (поле редактируемо во время работы агента).
        setDraft((cur) => (cur === text ? '' : cur))
        // Состояние берём ИЗ ОТВЕТА, не форсируя running: идемпотентный повтор
        // уже ЗАВЕРШЁННОГО хода возвращает state:'idle' — агент не запускался,
        // нового терминального события не будет.
        setRequestState(resp.state)
        if (resp.state === 'running') {
          // keepAlive поднимется (running) → connection-эффект догонит историю и
          // откроет стрим. Отдельный догон тут не нужен.
        } else {
          // idle: терминал SSE не придёт — догоняем историю сами, чтобы
          // восстановить пропущенный хвост/вывод завершённого хода. op-проверка:
          // если следующий send обгонит этот догон, его idle-снапшот не применится.
          void syncHistory(() => gen === genRef.current && op === opRef.current)
        }
      } catch (err: unknown) {
        if (gen !== genRef.current) return
        // id СОХРАНЯЕМ — повтор после сетевой ошибки переиспользует его.
        const code = err instanceof SideAgentApiError ? err.code : undefined
        setError(code !== undefined ? errorCodeToMessage(code) : 'Request failed')
      }
    },
    [enabled, syncHistory],
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

  return {
    events,
    requestState,
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
    interrupt: interruptTurn,
  }
}
