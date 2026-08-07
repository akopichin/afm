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

// Статусы, считающиеся «активными» — стадия либо в работе, либо блокирует
// прогресс флоу и ждёт решения человека (нужно auto-select, иначе на свежей
// загрузке дашборда стадия в awaiting_approval/hook_failed не выбирается
// сама, и панель Plan/Communication channel с кнопкой действия просто не
// рендерится — пользователь видит пустой экран, хотя баннер "Action needed"
// уже горит). awaiting_user_input уже был в этом множестве по той же причине,
// хоть агент там тоже не «работает», а ждёт ответа.
export const ACTIVE_STAGE_STATUSES: ReadonlySet<StageStatus> = new Set([
  'running',
  'planning',
  'revising',
  'retrying',
  'awaiting_user_input',
  'awaiting_approval',
  'hook_failed',
])

