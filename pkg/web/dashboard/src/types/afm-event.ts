import { STAGE_STATUSES, type StageStatus } from './stage'

// Известные типы WS-событий afm.
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
  'script_output',
  'script_failed',
  'hook_failed',
  'hook_resolved',
  'auto_answered',
  'dialog_question',
  'dialog_answer',
  'agent_note',
  // Agent Mesh (T7.2): additive non-FSM notice types for the mesh Service's own mutations
  // (pkg/orchestrator/bus.go's EventMesh* — the live wire types, distinct from the mesh_*_opened/sent/...
  // journal-only Effect kinds). A participant joining, a thread opening/closing, a message being sent, a
  // delivery/ack receipt, a wait registering/resolving, or a board key being published.
  'mesh_participant',
  'mesh_thread',
  'mesh_message',
  'mesh_receipt',
  'mesh_wait',
  'mesh_board',
] as const

export type AfmEventType = (typeof AFM_EVENT_TYPES)[number]

// Событие, приходящее через WebSocket /ws или из истории /api/events. seq — стабильный ключ дедупликации
// для событий, производных от реальной FSM-transition (только история из /api/events).
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
  'dialog_question',
  'dialog_answer',
  // agent_note сопровождает Revise и публикуется ПОСЛЕ stage_status_changed. Лишний /api/status на заметку
  // безвреден (он и так идёт каждые 3с).
  'agent_note',
  // mesh_* НЕ входят сюда: это информационные нотисы об операциях mesh Service (участник/тред/сообщение/
  // receipt/wait/board), ни одна из них не является FSM-переходом стадии — ни стадия, ни ран не меняют
  // статус из-за них (тот же принцип, что у assistant_task_*/discussion_message/verify_* — тоже не здесь).
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
