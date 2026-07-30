// Полный набор статусов стадии afm (см. statusLabels в текущем app.js).
// done — завершена (не completed).
export const STAGE_STATUSES = [
  'pending',
  'planning',
  'awaiting_approval',
  'revising',
  'ready',
  'running',
  'done',
  'failed',
  'retrying',
  'awaiting_user_input',
  'hook_failed',
] as const

export type StageStatus = (typeof STAGE_STATUSES)[number]

// Данные одной стадии afm-флоу. Источник — поле stages ответа GET /api/status
// (объект по идентификатору стадии); имя берётся из stage_names, время — из updated_at.
export type Stage = {
  id: string
  name: string
  status: StageStatus
  updatedAt: string
  interactive: boolean
  autonomous: boolean
  autoApprove: boolean
}

// Человекочитаемые подписи статусов для списка стадий и панели деталей.
export const STAGE_STATUS_LABELS: Record<StageStatus, string> = {
  pending: 'Pending',
  planning: 'Planning',
  awaiting_approval: 'Awaiting approval',
  revising: 'Revising',
  ready: 'Ready',
  running: 'Running',
  done: 'Done',
  failed: 'Failed',
  retrying: 'Retrying',
  awaiting_user_input: 'Awaiting reply',
  hook_failed: 'Hook failed',
}

// Статусы, считающиеся «активными» (стадия в работе) — основа автовыбора и логики
// обновления в корневой композиции. Соответствует ACTIVE_STATUSES в текущем app.js.
export const ACTIVE_STAGE_STATUSES: ReadonlySet<StageStatus> = new Set([
  'running',
  'planning',
  'revising',
  'retrying',
  'awaiting_user_input',
])

// Backoff — автоматическая пауза перед авто-ретраем, без участия
// пользователя. Питает useStatusDuration в app/App.tsx. Idle считается
// отдельным хуком useIdleTime (не простой суммой по набору статусов — см.
// его комментарий), т.к. failed-стадия не должна копить Idle, пока активен
// другой агент (реальный баг: каскадные blocked_by_dep failed-стадии копили
// Idle, пока пользователь ретраил и агент реально работал).
export const BACKOFF_STATUSES: ReadonlySet<StageStatus> = new Set(['retrying'])
