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
  // isScript: true, если стадия скриптовая (Stage.IsScript() в Go) — для неё нет кнопок стадии, заметки и
  // Pause во время выполнения (нет graceful mid-script stop).
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
  // awaitingAssistantsBlocked (Этап 7, T7.9) — значим только при status === 'awaiting_assistants': true, если
  // блокирующая required-задача требует решения человека (failed/cancelled/paused помощник либо
  // needs_changes/inconclusive вердикт), а не просто ещё работает. Зеркало Go
  // StageView.AwaitingAssistantsBlocked (omitempty на бэке). Опционально и здесь же — чтобы существующие
  // Stage-фикстуры в тестах не пришлось поголовно трогать (как cost/verify выше).
  awaitingAssistantsBlocked?: boolean
  // quota — durable-ожидание сброса лимита подписки (Go StageView.Quota). Есть только у awaiting_quota и у
  // paused, припаркованной по лимиту; у остальных стадий undefined.
  quota?: StageQuota
  // timing — учёт времени стадии (Go StageView.Timing). undefined — старый сервер без поля.
  timing?: StageTiming
}

// StageQuota — малый общий вид quota-парковки стадии или helper-задачи (сервер отдаёт RFC3339 в UTC; не вся
// state.QuotaWait). until — когда продолжит сам афм, resetAt — когда лимит сбросит провайдер; у source=probe
// until — время следующей проверки. pauseReason пуст при ручной паузе и пока стадия просто ждёт.
export const QUOTA_SOURCES = ['structured', 'text', 'probe', 'provider_hold'] as const
export const QUOTA_PAUSE_REASONS = ['exceeds_max_wait', 'no_reset', 'max_waits'] as const
export type StageQuota = {
  provider: string
  window: string
  source: (typeof QUOTA_SOURCES)[number]
  until: string
  resetAt: string
  pauseReason: (typeof QUOTA_PAUSE_REASONS)[number] | ''
}

// parseStageQuota приводит объект `quota` из API к StageQuota. Без provider объект бесполезен (undefined);
// неизвестные source/pause_reason заменяются безопасными значениями, как и прочие enum'ы из API.
export function parseStageQuota(raw: unknown): StageQuota | undefined {
  if (raw === null || typeof raw !== 'object') return undefined
  const obj = raw as Record<string, unknown>
  if (typeof obj.provider !== 'string' || obj.provider === '') return undefined
  const source = (QUOTA_SOURCES as readonly unknown[]).includes(obj.source) ? (obj.source as StageQuota['source']) : 'text'
  const pauseReason = (QUOTA_PAUSE_REASONS as readonly unknown[]).includes(obj.pause_reason)
    ? (obj.pause_reason as StageQuota['pauseReason'])
    : ''
  return {
    provider: obj.provider,
    window: typeof obj.window === 'string' ? obj.window : '',
    source,
    until: typeof obj.until === 'string' ? obj.until : '',
    resetAt: typeof obj.reset_at === 'string' ? obj.reset_at : '',
    pauseReason,
  }
}

// quotaTitle — «Usage limit» для временного лимита, «Quota exhausted» для insufficient_quota (пауза no_reset:
// пополнение квоты автоматически не ждётся).
export function quotaTitle(quota: StageQuota | undefined): string {
  return quota?.pauseReason === 'no_reset' ? 'Quota exhausted' : 'Usage limit'
}

export const STAGE_TIME_KINDS = ['work', 'approval', 'helpers', 'backoff', 'scheduling', 'paused'] as const
export type StageTimeKind = (typeof STAGE_TIME_KINDS)[number]

// StageTiming — время стадии из /api/status. ms (закрытые интервалы по видам, мс) есть только у стартовавшей
// стадии с полными данными; open — интервал, который сервер ещё не закрыл, живую часть добавляет клиент.
// complete=false — данные неполные (журнал без run-событий): сумм нет, нуль не выдумываем.
export type StageTiming = {
  started: boolean
  complete: boolean
  ms?: Record<StageTimeKind, number>
  open?: { kind: StageTimeKind; sinceMs: number }
}

// parseStageTiming приводит `timing` из API к StageTiming. Сломанная форма — undefined (как у старого сервера);
// стартовавшая стадия с полными данными, но без числовых сумм, считается неполной.
export function parseStageTiming(raw: unknown): StageTiming | undefined {
  if (raw === null || typeof raw !== 'object') return undefined
  const obj = raw as Record<string, unknown>
  if (typeof obj.started !== 'boolean' || typeof obj.complete !== 'boolean') return undefined
  if (!obj.started) return { started: false, complete: obj.complete }
  if (!obj.complete) return { started: true, complete: false }

  const ms = {} as Record<StageTimeKind, number>
  for (const kind of STAGE_TIME_KINDS) {
    const value = obj[`${kind}_ms`]
    if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) return { started: true, complete: false }
    ms[kind] = value
  }
  return { started: true, complete: true, ms, open: parseStageTimeOpen(obj.open) }
}

function parseStageTimeOpen(raw: unknown): StageTiming['open'] {
  if (raw === null || typeof raw !== 'object') return undefined
  const obj = raw as Record<string, unknown>
  const kind = (STAGE_TIME_KINDS as readonly unknown[]).includes(obj.kind) ? (obj.kind as StageTimeKind) : null
  const sinceMs = typeof obj.since === 'string' ? Date.parse(obj.since) : NaN
  if (kind === null || Number.isNaN(sinceMs)) return undefined
  return { kind, sinceMs }
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
  // awaiting_assistants (Этап 7, T7.9) — главный executor стадии уже закончил, стадия ждёт, пока required-
  // помощник закроет свой гейт. Сам по себе ярлык не различает «помощник ещё работает» и «нужно решение
  // человека» — это несёт stage.awaitingAssistantsBlocked (см. use-attention/attentionKindFor).
  awaiting_assistants: 'Awaiting helpers',
  // awaiting_peer (Agent Mesh Phase 5, T5.1) — the stage's executor registered a wait on a colleague and
  // exited; it resumes automatically once the colleague replies. Machine work, not a human decision.
  awaiting_peer: 'Awaiting colleague',
  // awaiting_quota — стадия припаркована до сброса лимита подписки провайдера и продолжит сама; это машинное
  // ожидание, не решение человека. Причина и время — в stage.quota.
  awaiting_quota: 'Usage limit',
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
  'awaiting_assistants',
  'awaiting_peer',
  'awaiting_quota',
])

