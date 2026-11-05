// Типизированный клиент к /api/side-agent[/...] — «боковой» разговор с агентом.

import { parseSideAgentEvent, type SideAgentEvent } from '../types/side-agent'

export class SideAgentApiError extends Error {
  code: string | undefined
  status: number

  constructor(status: number, code: string | undefined) {
    super(`assistant request failed: ${status}${code ? ` (${code})` : ''}`)
    this.name = 'SideAgentApiError'
    this.status = status
    this.code = code
  }
}

export type SideAgentConversationState = 'idle' | 'running'

export type MessageState = 'queued' | 'running' | 'completed' | 'failed' | 'interrupted' | 'cancelled'

export type ActiveSourceWire = { kind: 'modal' } | { kind: 'mesh'; senderId: string; senderName: string } | null

// QueuedSnapshot — запись очереди из GET-снимка.
export type QueuedSnapshot = {
  messageId: string
  clientMessageId: string
  text: string
  stageId: string
  assistantTaskId: string
  seq: number
}

// ConversationResponse — форма GET /api/side-agent.
export type ConversationResponse = {
  conversationId: string
  runId: string
  command: string
  commandName?: string
  unavailable: boolean
  state: SideAgentConversationState
  activeTurnId: string
  activeSource: ActiveSourceWire
  queued: QueuedSnapshot[]
  events: SideAgentEvent[]
  lastSeq: number
  nextCursor: number
  hasMore: boolean
}

// SendResponse — форма 202 POST /api/side-agent/messages. messageState — состояние самой записи, state —
// разговора; failed (errorCode) — приём состоялся, но сообщение не будет обработано.
export type SendResponse = {
  messageId: string
  messageState: MessageState
  errorCode: string
  turnId: string
  state: SideAgentConversationState
  cursor: number
}

// RemoveResponse — форма 200 DELETE /api/side-agent/messages/{id}.
export type RemoveResponse = {
  messageId: string
  messageState: MessageState
  cursor: number
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object'
}

const MESSAGE_STATES: readonly MessageState[] = ['queued', 'running', 'completed', 'failed', 'interrupted', 'cancelled']

function str(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function toMessageState(value: unknown): MessageState {
  return MESSAGE_STATES.find((s) => s === value) ?? 'queued'
}

function toActiveSource(raw: unknown): ActiveSourceWire {
  if (!isRecord(raw)) return null
  if (raw.kind === 'modal') return { kind: 'modal' }
  if (raw.kind === 'mesh') return { kind: 'mesh', senderId: str(raw.sender_id), senderName: str(raw.sender_name) }
  return null
}

function toQueued(raw: unknown): QueuedSnapshot[] {
  if (!Array.isArray(raw)) return []
  return raw.filter(isRecord).map((q) => ({
    messageId: str(q.message_id),
    clientMessageId: str(q.client_message_id),
    text: str(q.text),
    stageId: str(q.stage_id),
    assistantTaskId: str(q.assistant_task_id),
    seq: typeof q.seq === 'number' ? q.seq : 0,
  }))
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

// normalizeConversation — единственная точка приведения внешнего JSON к ConversationResponse. События
// проходят через parseSideAgentEvent (битые/с неизвестным типом отбрасываются).
export function normalizeConversation(raw: unknown): ConversationResponse {
  const obj = isRecord(raw) ? raw : {}
  const events = Array.isArray(obj.events)
    ? obj.events.map(parseSideAgentEvent).filter((e): e is SideAgentEvent => e !== null)
    : []
  return {
    conversationId: typeof obj.conversation_id === 'string' ? obj.conversation_id : '',
    runId: typeof obj.run_id === 'string' ? obj.run_id : '',
    command: typeof obj.command === 'string' ? obj.command : '',
    commandName: typeof obj.command_name === 'string' ? obj.command_name : undefined,
    unavailable: obj.unavailable === true,
    state: obj.state === 'running' ? 'running' : 'idle',
    activeTurnId: str(obj.active_turn_id),
    activeSource: toActiveSource(obj.active_source),
    queued: toQueued(obj.queued),
    events,
    lastSeq: typeof obj.last_seq === 'number' ? obj.last_seq : 0,
    nextCursor: typeof obj.next_cursor === 'number' ? obj.next_cursor : 0,
    hasMore: obj.has_more === true,
  }
}

// sendMessage отправляет сообщение пользователя. Клиент управляет client_message_id, text и опционально
// stageId — какой стадии касается сообщение (T4.3, для отображения «Context: …»; пусто → не отправляется
// вовсе, тот же body, что и раньше). Команда/CWD/depth/root_stage_id по-прежнему задаются сервером.
export async function sendMessage(clientMessageId: string, text: string, stageId = ''): Promise<SendResponse> {
  const reqBody: Record<string, string> = { client_message_id: clientMessageId, text }
  if (stageId !== '') reqBody.stage_id = stageId
  const response = await fetch('/api/side-agent/messages', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(reqBody),
  })
  if (!response.ok) throw new SideAgentApiError(response.status, await readErrorCode(response))
  const body: unknown = await response.json()
  const b = isRecord(body) ? body : {}
  return {
    messageId: str(b.message_id),
    messageState: toMessageState(b.message_state),
    errorCode: str(b.error_code),
    turnId: str(b.turn_id),
    state: b.state === 'running' ? 'running' : 'idle',
    cursor: typeof b.cursor === 'number' ? b.cursor : 0,
  }
}

// removeQueued удаляет ещё не взятое в работу сообщение. Повтор идемпотентен (200); уже взятое → 409
// message_not_queued, неизвестное → 404 message_not_found.
export async function removeQueued(messageId: string): Promise<RemoveResponse> {
  const response = await fetch(`/api/side-agent/messages/${encodeURIComponent(messageId)}`, { method: 'DELETE' })
  if (!response.ok) throw new SideAgentApiError(response.status, await readErrorCode(response))
  const body: unknown = await response.json()
  const b = isRecord(body) ? body : {}
  return { messageId: str(b.message_id), messageState: toMessageState(b.message_state), cursor: typeof b.cursor === 'number' ? b.cursor : 0 }
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

// streamUrl — адрес SSE-потока журнала. after_seq добирает пропущенный хвост при первичном подключении;
// при реконнекте EventSource шлёт Last-Event-ID (он у сервера в приоритете над after_seq).
export function streamUrl(afterSeq = 0): string {
  return afterSeq > 0 ? `/api/side-agent/stream?after_seq=${afterSeq}` : '/api/side-agent/stream'
}
