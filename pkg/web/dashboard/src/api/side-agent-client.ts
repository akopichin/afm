// Типизированный клиент к /api/side-agent[/...] — «боковой» разговор с агентом.
// Чистые функции: вызывать их должен только код, у которого capability
// side_agent включён (гейт держит хук/провайдер, см. use-side-agent); сам клиент
// это не проверяет. На не-2xx читается {error:<code>} и бросается типизированная
// ошибка, несущая код, чтобы UI смог смапить его в текст (errorCodeToMessage).

import { parseSideAgentEvent, type SideAgentEvent } from '../types/side-agent'

export class SideAgentApiError extends Error {
  code: string | undefined
  status: number

  constructor(status: number, code: string | undefined) {
    super(`side-agent request failed: ${status}${code ? ` (${code})` : ''}`)
    this.name = 'SideAgentApiError'
    this.status = status
    this.code = code
  }
}

export type SideAgentConversationState = 'idle' | 'running'

// ConversationResponse — форма GET /api/side-agent. next_cursor продвигается ТОЛЬКО
// по возвращённым событиям (с него возобновляют поллинг); last_seq — хвост журнала;
// has_more = next_cursor < last_seq.
export type ConversationResponse = {
  conversationId: string
  runId: string
  command: string
  unavailable: boolean
  state: SideAgentConversationState
  events: SideAgentEvent[]
  lastSeq: number
  nextCursor: number
  hasMore: boolean
}

// SendResponse — форма 202 POST /api/side-agent/messages.
export type SendResponse = {
  turnId: string
  state: SideAgentConversationState
  cursor: number
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object'
}

async function readErrorCode(response: Response): Promise<string | undefined> {
  try {
    const body: unknown = await response.json()
    return isRecord(body) && typeof body.error === 'string' ? body.error : undefined
  } catch {
    return undefined
  }
}

export async function getConversation(afterSeq = 0, limit = 0): Promise<ConversationResponse> {
  const params = new URLSearchParams()
  if (afterSeq > 0) params.set('after_seq', String(afterSeq))
  if (limit > 0) params.set('limit', String(limit))
  const qs = params.toString()
  const response = await fetch(qs ? `/api/side-agent?${qs}` : '/api/side-agent')
  if (!response.ok) throw new SideAgentApiError(response.status, await readErrorCode(response))
  return normalizeConversation(await response.json())
}

// normalizeConversation — единственная точка приведения внешнего JSON к
// ConversationResponse. События проходят через parseSideAgentEvent (битые/с
// неизвестным типом отбрасываются).
export function normalizeConversation(raw: unknown): ConversationResponse {
  const obj = isRecord(raw) ? raw : {}
  const events = Array.isArray(obj.events)
    ? obj.events.map(parseSideAgentEvent).filter((e): e is SideAgentEvent => e !== null)
    : []
  return {
    conversationId: typeof obj.conversation_id === 'string' ? obj.conversation_id : '',
    runId: typeof obj.run_id === 'string' ? obj.run_id : '',
    command: typeof obj.command === 'string' ? obj.command : '',
    unavailable: obj.unavailable === true,
    state: obj.state === 'running' ? 'running' : 'idle',
    events,
    lastSeq: typeof obj.last_seq === 'number' ? obj.last_seq : 0,
    nextCursor: typeof obj.next_cursor === 'number' ? obj.next_cursor : 0,
    hasMore: obj.has_more === true,
  }
}

// sendMessage отправляет сообщение пользователя. Клиент управляет ТОЛЬКО
// client_message_id и text — команда/CWD задаются сервером. Same-origin Origin
// браузер проставляет сам. Повтор с тем же client_message_id идемпотентен (бэкенд
// вернёт тот же ход).
export async function sendMessage(clientMessageId: string, text: string): Promise<SendResponse> {
  const response = await fetch('/api/side-agent/messages', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ client_message_id: clientMessageId, text }),
  })
  if (!response.ok) throw new SideAgentApiError(response.status, await readErrorCode(response))
  const body = (await response.json()) as { turn_id?: unknown; state?: unknown; cursor?: unknown }
  return {
    turnId: typeof body.turn_id === 'string' ? body.turn_id : '',
    state: body.state === 'running' ? 'running' : 'idle',
    cursor: typeof body.cursor === 'number' ? body.cursor : 0,
  }
}

// interrupt прерывает ровно указанный ход. Устаревший turn_id → 409 stale_turn.
export async function interrupt(turnId: string): Promise<void> {
  const response = await fetch('/api/side-agent/interrupt', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ turn_id: turnId }),
  })
  if (!response.ok) throw new SideAgentApiError(response.status, await readErrorCode(response))
}

// streamUrl — адрес SSE-потока журнала. after_seq добирает пропущенный хвост при
// первичном подключении; при реконнекте EventSource шлёт Last-Event-ID (он у
// сервера в приоритете над after_seq).
export function streamUrl(afterSeq = 0): string {
  return afterSeq > 0 ? `/api/side-agent/stream?after_seq=${afterSeq}` : '/api/side-agent/stream'
}
