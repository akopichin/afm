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

// Idle — стадия ждёт действия ПОЛЬЗОВАТЕЛЯ: открытый диалог, ожидание
// одобрения плана, или упавшая стадия (ждёт ручного retry). Backoff —
// автоматическая пауза перед авто-ретраем, без участия пользователя.
// Оба набора питают useStatusDuration в app/App.tsx (см. дизайн-документ
// docs/superpowers/specs/2026-07-29-dashboard-event-feed-ui-fixes-design.md).
export const IDLE_STATUSES: ReadonlySet<StageStatus> = new Set(['awaiting_user_input', 'awaiting_approval', 'failed'])
export const BACKOFF_STATUSES: ReadonlySet<StageStatus> = new Set(['retrying'])
