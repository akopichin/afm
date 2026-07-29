// Barrel-фасад клеточки src/types: все общие типы данных дашборда реэкспортируются
// отсюда, чтобы остальные клетки импортировали их через '../../types' (см. dashboard-data-types).
export type { Stage, StageStatus } from './stage'
export type { AfmEvent, AfmEventType } from './afm-event'
export type { DialogQuestion } from './dialog-question'
export type { DialogAnswer } from './dialog-answer'
export type { PlanComment } from './plan-comment'
export type { LogEntry, LogLevel } from './log-entry'

export {
  STAGE_STATUSES,
  STAGE_STATUS_LABELS,
  ACTIVE_STAGE_STATUSES,
  IDLE_STATUSES,
  BACKOFF_STATUSES,
} from './stage'
export { AFM_EVENT_TYPES, SIGNIFICANT_EVENT_TYPES, extractStageStatus } from './afm-event'
