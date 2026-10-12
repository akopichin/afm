// STAGE_STATUSES/StageStatus сгенерированы из pkg/state.AllStatuses — см. tools/genstagestatus и 'make
// generate'.
export { STAGE_STATUSES, type StageStatus } from './stage-status.generated'
import type { StageStatus } from './stage-status.generated'
import type { CostSummary } from './cost'

// Данные одной стадии afm-флоу.
export type Stage = {
  id: string
  name: string
  status: StageStatus
  updatedAt: string
  interactive: boolean
  autonomous: boolean
  autoApprove: boolean
  hasDialog: boolean
  showPlan: boolean
  showDialog: boolean
  // isScript: true, если стадия скриптовая (Stage.IsScript() в Go) — кебаб- меню скрывает пункт "Pause",
  // пока такая стадия реально выполняется (нет graceful mid-script stop).
  isScript: boolean
  // pausedFrom заполнен только когда status === 'paused' (см. Go StageView.PausedFrom) — пустая строка
  // иначе. Панель паузы использует его для текста причины.
  pausedFrom: StageStatus | ''
  // preNote — текст заметки, прикреплённой к стадии до старта. Пустая строка — заметки нет.
  preNote: string
  // buttons — метки предопределённых в flow.yaml кнопок стадии, в порядке объявления. Пустой массив —
  // кнопок нет.
  buttons: string[]
  // cost — сводка по затратам стадии (omitempty, если нет записи использования).
  cost?: CostSummary
  // verify — durable-индикатор последнего AI-verify прохода стадии. undefined — у стадии ни разу не было
  // verify_started/verify_result.
  verify?: StageVerify
}

// StageVerify — компактное текущее состояние AI-verify одной стадии: verify_started → 'running',
// verify_result → его verdict (pass/needs_changes/inconclusive), иначе — 'error'.
export type StageVerify = {
  step: number
  command: string
  phase: 'running' | 'pass' | 'accepted_stalemate' | 'needs_changes' | 'inconclusive' | 'error'
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
  paused: 'Paused',
  awaiting_user_input: 'Awaiting reply',
  hook_failed: 'Hook failed',
}

// Статусы, считающиеся «активными» — стадия либо в работе, либо блокирует прогресс флоу и ждёт решения
// человека.
export const ACTIVE_STAGE_STATUSES: ReadonlySet<StageStatus> = new Set([
  'running',
  'planning',
  'revising',
  'retrying',
  'paused',
  'awaiting_user_input',
  'awaiting_approval',
  'hook_failed',
])

