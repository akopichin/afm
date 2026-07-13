import { STAGE_STATUSES, type StageStatus } from './stage'

// Известные типы WS-событий afm. type намеренно приведён к строке (см. AfmEvent):
// сервер может присылать и неизвестные типы, и корневая композиция обязана переносить
// их как ленту, не падая (как default-ветка в текущем app.js).
export const AFM_EVENT_TYPES = [
  'stage_status_changed',
  'approved',
  'revised',
  'retry_scheduled',
  'retry_exhausted',
  'manual_retry',
  'ask_user',
  'user_answered',
  'agent_action',
  'agent_completed',
] as const

export type AfmEventType = (typeof AFM_EVENT_TYPES)[number]

// Событие, приходящее через WebSocket /ws.
//   payload  — произвольные данные события (соответствует полю data сервера; тип зависит от type);
//   stageId  — стадия, к которой относится событие (поле stage_id сервера);
//   timestamp — время приёма на клиенте в ISO 8601 (сервер не присылает время события).
export type AfmEvent = {
  type: string
  payload: unknown
  stageId: string
  timestamp: string
}

// Значимые типы событий — канал обновления состояния: по ним корневая композиция
// ре-запрашивает состояние флоу (как handleEvent → loadState в текущем app.js).
export const SIGNIFICANT_EVENT_TYPES: ReadonlySet<string> = new Set([
  'stage_status_changed',
  'approved',
  'revised',
  'retry_scheduled',
  'retry_exhausted',
  'manual_retry',
  'ask_user',
  'user_answered',
  'agent_completed',
])

// Извлекает новый статус стадии из stage_status_changed (data — строка или { status }).
export function extractStageStatus(payload: unknown): StageStatus | null {
  if (typeof payload === 'string') {
    return (STAGE_STATUSES as readonly string[]).includes(payload) ? (payload as StageStatus) : null
  }

  if (payload !== null && typeof payload === 'object' && 'status' in payload) {
    const status = (payload as { status: unknown }).status
    if (typeof status === 'string' && (STAGE_STATUSES as readonly string[]).includes(status)) {
      return status as StageStatus
    }
  }

  return null
}
