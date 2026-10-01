// Типы «бокового» разговора с агентом (side agent): форма события журнала,
// типизированные нагрузки по типу события, состояние разговора и коды ошибок.
// Зеркалит контракт pkg/sideagent (Go) — единственная точка приведения внешнего
// JSON к типам фронта (как normalizeStatus/toEvent для /api/status и /ws).

// Каталог типов событий журнала. Неизвестный тип приходящего события → событие
// отбрасывается (parseSideAgentEvent → null), поток при этом не рвётся.
export const SIDE_AGENT_EVENT_TYPES = [
  'turn_accepted',
  'turn_started',
  'assistant_text',
  'tool_action',
  'turn_completed',
  'turn_failed',
  'turn_interrupted',
] as const

export type SideAgentEventType = (typeof SIDE_AGENT_EVENT_TYPES)[number]

// Коды отказа запроса — часть контракта API (pkg/sideagent ErrorCode плюс коды,
// которые может нести turn_failed.data.code: agent_error/no_answer). Любой
// незнакомый код трактуется errorCodeToMessage как общий отказ, а не как ошибка.
export const SIDE_AGENT_ERROR_CODES = [
  'busy',
  'invalid_message',
  'message_too_large',
  'context_limit',
  'shutting_down',
  'storage_unavailable',
  'stale_turn',
  'agent_error',
  'no_answer',
] as const

export type SideAgentErrorCode = (typeof SIDE_AGENT_ERROR_CODES)[number]

// errorCodeToMessage — короткий человекочитаемый текст для кода ошибки. Принимает
// строку (сервер может прислать и транспортные коды forbidden_origin/invalid_body/
// internal, и совсем незнакомые), не только SideAgentErrorCode; неизвестный код →
// общий текст.
export function errorCodeToMessage(code: string): string {
  switch (code) {
    case 'busy':
      return 'Another request is in progress'
    case 'invalid_message':
      return 'Message is empty or invalid'
    case 'message_too_large':
      return 'Message too large'
    case 'context_limit':
      return 'Conversation too long for one request'
    case 'shutting_down':
      return 'The run is shutting down'
    case 'storage_unavailable':
      return 'Storage is unavailable, try again'
    case 'stale_turn':
      return 'This request is no longer active'
    case 'agent_error':
      return 'The agent failed to answer'
    case 'no_answer':
      return 'The agent returned no answer'
    default:
      return 'Request failed'
  }
}

// --- Типизированные нагрузки по типу события (camelCase, нормализованы из wire) ---

export type TurnAcceptedData = { message: string; clientMessageId: string }
export type TurnStartedData = { command: string; contextFormatVersion: string }
export type AssistantTextData = { text: string }
export type ToolActionData = { tool: string; detail: string; logRef: string }
export type TurnCompletedData = Record<string, never>
export type TurnFailedData = { code: string; message: string }
export type TurnInterruptedData = { reason: string }

// Конверт одной записи журнала + дискриминированная по type нагрузка — после
// parseSideAgentEvent потребитель сужает data по e.type без каста.
type SideAgentEventEnvelope = {
  schemaVersion: number
  seq: number
  conversationId: string
  turnId: string
  timestamp: string
}

export type SideAgentEvent = SideAgentEventEnvelope &
  (
    | { type: 'turn_accepted'; data: TurnAcceptedData }
    | { type: 'turn_started'; data: TurnStartedData }
    | { type: 'assistant_text'; data: AssistantTextData }
    | { type: 'tool_action'; data: ToolActionData }
    | { type: 'turn_completed'; data: TurnCompletedData }
    | { type: 'turn_failed'; data: TurnFailedData }
    | { type: 'turn_interrupted'; data: TurnInterruptedData }
  )

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object'
}

function str(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function isEventType(value: unknown): value is SideAgentEventType {
  return typeof value === 'string' && (SIDE_AGENT_EVENT_TYPES as readonly string[]).includes(value)
}

// parseEventData нормализует нагрузку по типу события (wire snake_case →
// camelCase), защитно подставляя пустые значения для отсутствующих/битых полей.
function parseEventData(type: SideAgentEventType, raw: unknown): SideAgentEvent['data'] {
  const d = isRecord(raw) ? raw : {}
  switch (type) {
    case 'turn_accepted':
      return { message: str(d.message), clientMessageId: str(d.client_message_id) }
    case 'turn_started':
      return { command: str(d.command), contextFormatVersion: str(d.context_format_version) }
    case 'assistant_text':
      return { text: str(d.text) }
    case 'tool_action':
      return { tool: str(d.tool), detail: str(d.detail), logRef: str(d.log_ref) }
    case 'turn_failed':
      return { code: str(d.code), message: str(d.message) }
    case 'turn_interrupted':
      return { reason: str(d.reason) }
    case 'turn_completed':
      return {}
  }
}

// parseSideAgentEvent приводит один сырой конверт (из SSE-кадра или GET-ответа) к
// SideAgentEvent. Неизвестный type или не-число seq → null: событие без стабильного
// seq нельзя дедупить/курсорить, поэтому его отбрасываем, а не аппендим вслепую.
export function parseSideAgentEvent(raw: unknown): SideAgentEvent | null {
  const obj = isRecord(raw) ? raw : null
  if (obj === null) return null
  if (!isEventType(obj.type)) return null
  if (typeof obj.seq !== 'number') return null

  const envelope: SideAgentEventEnvelope = {
    schemaVersion: typeof obj.schema_version === 'number' ? obj.schema_version : 0,
    seq: obj.seq,
    conversationId: str(obj.conversation_id),
    turnId: str(obj.turn_id),
    timestamp: str(obj.timestamp),
  }
  // type + соответствующая ему data собраны вместе — рантайм-корреляция верна,
  // TS о ней судить не может, поэтому единичный каст на границе разбора.
  return { ...envelope, type: obj.type, data: parseEventData(obj.type, obj.data) } as SideAgentEvent
}
