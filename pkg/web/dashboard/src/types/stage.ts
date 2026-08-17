// STAGE_STATUSES/StageStatus сгенерированы из pkg/state.AllStatuses() —
// см. tools/genstagestatus и 'make generate'. Раньше это был отдельный
// вручную поддерживаемый список, который приходилось синхронизировать с Go
// FSM руками при каждом новом статусе.
//
// `export { type StageStatus } from ...` реэкспортирует имя для потребителей
// этого файла, но НЕ вносит его в область видимости самого stage.ts — ниже
// StageStatus используется в собственных аннотациях типов (Stage.status,
// STAGE_STATUS_LABELS, ACTIVE_STAGE_STATUSES), поэтому нужен ещё и обычный
// `import type` рядом.
export { STAGE_STATUSES, type StageStatus } from './stage-status.generated'
import type { StageStatus } from './stage-status.generated'

// Данные одной стадии afm-флоу. Источник — элемент упорядоченного массива
// stages ответа GET /api/status (см. pkg/server/stageview.go's StageView) —
// каждое поле уже лежит прямо на объекте стадии, отдельных parallel-map'ов
// (stage_names/stage_interactive/...) в ответе больше нет.
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
  'paused',
  'awaiting_user_input',
  'awaiting_approval',
  'hook_failed',
])

