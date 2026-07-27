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
  'supervisor_decision',
] as const

export type AfmEventType = (typeof AFM_EVENT_TYPES)[number]

// Событие, приходящее через WebSocket /ws или из истории /api/events.
//   payload   — произвольные данные события (соответствует полю data сервера; тип зависит от type);
//   stageId   — стадия, к которой относится событие (поле stage_id сервера);
//   timestamp — реальное время события в ISO 8601, если его прислал сервер
//               (только реплей истории из /api/events — Task 4); иначе время
//               приёма на клиенте (live WS-сообщения timestamp не несут).
//   seq       — стабильный ключ дедупликации для событий, производных от
//               реальной FSM-transition (только история из /api/events).
export type AfmEvent = {
  type: string
  payload: unknown
  stageId: string
  timestamp: string
  seq?: number
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
