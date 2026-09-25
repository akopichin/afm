import { useCallback, useEffect, useRef, useState } from 'react'
import type { Stage, StageStatus, StageVerify } from '../../types'
import { STAGE_STATUSES } from '../../types'
import type { AccountingState, CostSummary, CoverageIssue } from '../../types/cost'

// Периодический опрос состояния флоу. Соответствует loadState в текущем app.js,
// но в React-форме: поллинг по таймеру + возможность немедленного обновления через
// refresh() (WS-события — канал обновления состояния, см. корневую композицию).
const POLL_INTERVAL_MS = 3000
const STATUS_STALE_MS = 2 * POLL_INTERVAL_MS

export type FlowStatus = {
  flowName: string
  stages: Stage[]
  startedAt: string
  runStatus: 'running' | 'finished' | 'failed' | 'interrupted' | null
  endedAt: string | null
  elapsedAccumulatedMs: number | null
  elapsedSince: string | null
  // Описание флоу (из корня flow.yaml) — опциональное поле GET /api/status для
  // подзаголовка в шапке (см. FlowHeader). Бэкенд пока его не отдаёт, поле
  // читается защитно (undefined, если отсутствует), без нового API-вызова —
  // как только бэкенд начнёт присылать description, подзаголовок появится сам.
  description?: string
  // idle/backoff — накопленное на бэкенде время (пережившее restart) плюс
  // необязательный анкер ТЕКУЩЕГО открытого периода/эпизодов, см.
  // useIdleMs/useBackoffMs. idleSince — null, если флоу не простаивает
  // прямо сейчас; backoffOpenSince — по одному значению на каждую стадию,
  // сейчас находящуюся в retrying (может быть пустым).
  idleAccumulatedMs: number
  idleSince: string | null
  backoffAccumulatedMs: number
  backoffOpenSince: string[]
  // capabilities — фичефлаги бэкенда для текущего запуска (см. Go
  // Server.Capabilities). Сейчас единственный флаг: включён ли файловый
  // браузер проекта (Task 13 UI). Отсутствие поля в ответе — как на старом
  // бэкенде без этого API — трактуется как выключено, а не как ошибка.
  capabilities: { fileBrowser: boolean }
  // flowPauseState/flowPausedStages — состояние flow-wide review-pause (см.
  // Go handlers.go's statusResponse.FlowPauseState/FlowPausedStages): 'none' —
  // обычная работа; 'paused' — активные стадии приостановлены для сбора
  // ревью-заметок (flowPausedStages — их id); 'resuming' — заметки уже
  // инжектированы/отменены, флоу возобновляется. Любое незнакомое значение
  // (в т.ч. отсутствие поля — старый бэкенд без этой фичи) трактуется как
  // 'none', а не как ошибка.
  flowPauseState: 'none' | 'paused' | 'resuming'
  flowPausedStages: string[]
  // runCost/runOverheadCost — сводки по затратам флоу (omitempty, если
  // бэкенд не отдаёт информацию о стоимости).
  runCost?: CostSummary
  runOverheadCost?: CostSummary
  // coverageIssues — проблемы с покрытием прайс-листа (пустой массив, если
  // нет проблем или бэкенд не отдаёт).
  coverageIssues: CoverageIssue[]
  // accounting — состояние учёта затрат: поддержано или нет на бэкенде.
  // Отсутствие поля трактуется как {supported: false} (старый бэкенд).
  accounting: AccountingState
}

const EMPTY_STATUS: FlowStatus = {
  flowName: '',
  stages: [],
  startedAt: '',
  runStatus: null,
  endedAt: null,
  elapsedAccumulatedMs: null,
  elapsedSince: null,
  idleAccumulatedMs: 0,
  idleSince: null,
  backoffAccumulatedMs: 0,
  backoffOpenSince: [],
  capabilities: { fileBrowser: false },
  flowPauseState: 'none',
  flowPausedStages: [],
  coverageIssues: [],
  accounting: { supported: false },
}

// Сырой ответ GET /api/status приводится к FlowStatus в normalizeStatus: stages —
// уже упорядоченный массив StageView (см. pkg/server/stageview.go), маппинг 1:1
// через toStage.

export function useStatus(): FlowStatus & { refresh: () => void; statusAvailable: boolean } {
  const [status, setStatus] = useState<FlowStatus>(EMPTY_STATUS)
  const [statusAvailable, setStatusAvailable] = useState(false)
  const lastStatusSuccessAt = useRef<number | null>(null)
  const cancelledRef = useRef(false)
  // Поллинг и WS-триггерный refresh() issue независимые fetch('/api/status') —
  // несколько запросов могут быть в полёте одновременно (напр. burst значимых
  // событий), а сетевые ответы приходят не обязательно в порядке отправки.
  // latestRequestId — счётчик поколений: каждый load() запоминает свой номер
  // ДО await и применяет ответ, только если он всё ещё самый свежий issued
  // запрос — иначе более старый (но позже резолвившийся) ответ откатил бы
  // состояние назад (реальный баг: пропущенный auto-advance стадии в App).
  const latestRequestId = useRef(0)

  const load = useCallback(async () => {
    const requestId = ++latestRequestId.current

    let response: Response
    try {
      response = await fetch('/api/status')
    } catch {
      if (!cancelledRef.current && requestId === latestRequestId.current) setStatusAvailable(false)
      return
    }

    if (!response.ok) {
      if (!cancelledRef.current && requestId === latestRequestId.current) setStatusAvailable(false)
      return
    }

    // Единственная точка приведения типа для внешнего JSON.
    let data: unknown
    try {
      data = await response.json()
    } catch {
      if (!cancelledRef.current && requestId === latestRequestId.current) setStatusAvailable(false)
      return
    }
    if (cancelledRef.current) return
    if (requestId !== latestRequestId.current) return

    setStatus(normalizeStatus(data))
    lastStatusSuccessAt.current = Date.now()
    setStatusAvailable(true)
  }, [])

  const refresh = useCallback(() => {
    void load()
  }, [load])

  useEffect(() => {
    cancelledRef.current = false
    void load()

    const timer = setInterval(() => {
      void load()
    }, POLL_INTERVAL_MS)
    const freshnessTimer = setInterval(() => {
      const last = lastStatusSuccessAt.current
      if (last !== null && Date.now() - last > STATUS_STALE_MS) setStatusAvailable(false)
    }, 1000)

    return () => {
      cancelledRef.current = true
      clearInterval(timer)
      clearInterval(freshnessTimer)
    }
  }, [load])

  // Фоновая/свёрнутая вкладка троттлит setInterval сильнее, чем доставку
  // WS-сообщений (см. use-event-feed.ts) — поэтому долгий флоу может
  // завершиться, пока опрос выше не тикнул ни разу. Возврат вкладки в фокус —
  // сигнал незамедлительно подтянуть актуальный статус, а не ждать следующего
  // (возможно, отложенного браузером) тика.
  useEffect(() => {
    const onVisible = () => {
      if (document.visibilityState === 'visible') void load()
    }
    // focus не нуждается в проверке visibilityState — сам факт фокуса окна
    // уже означает, что вкладка активна.
    const onFocus = () => void load()
    document.addEventListener('visibilitychange', onVisible)
    window.addEventListener('focus', onFocus)
    return () => {
      document.removeEventListener('visibilitychange', onVisible)
      window.removeEventListener('focus', onFocus)
    }
  }, [load])

  return { ...status, refresh, statusAvailable }
}

export function normalizeStatus(raw: unknown): FlowStatus {
  const obj = isRecord(raw) ? raw : {}

  const flowName = typeof obj.flow_name === 'string' ? obj.flow_name : ''
  const startedAt = typeof obj.started_at === 'string' ? obj.started_at : ''
  const runStatus: FlowStatus['runStatus'] =
    obj.run_status === 'running' || obj.run_status === 'finished' || obj.run_status === 'failed' || obj.run_status === 'interrupted'
      ? obj.run_status : null
  const endedAt = typeof obj.ended_at === 'string' ? obj.ended_at : null
  const elapsedAccumulatedMs = typeof obj.elapsed_accumulated_ms === 'number' ? obj.elapsed_accumulated_ms : null
  const elapsedSince = typeof obj.elapsed_since === 'string' ? obj.elapsed_since : null
  const description = typeof obj.description === 'string' ? obj.description : undefined

  const stages: Stage[] = Array.isArray(obj.stages) ? obj.stages.map(toStage).filter((s): s is Stage => s !== null) : []

  const idleAccumulatedMs = typeof obj.idle_accumulated_ms === 'number' ? obj.idle_accumulated_ms : 0
  const idleSince = typeof obj.idle_since === 'string' ? obj.idle_since : null
  const backoffAccumulatedMs = typeof obj.backoff_accumulated_ms === 'number' ? obj.backoff_accumulated_ms : 0
  const backoffOpenSince = Array.isArray(obj.backoff_open_since)
    ? obj.backoff_open_since.filter((v): v is string => typeof v === 'string')
    : []

  const rawCapabilities = isRecord(obj.capabilities) ? obj.capabilities : {}
  const capabilities = { fileBrowser: rawCapabilities.file_browser === true }

  const flowPauseState: FlowStatus['flowPauseState'] =
    obj.flow_pause_state === 'paused' || obj.flow_pause_state === 'resuming' ? obj.flow_pause_state : 'none'
  const flowPausedStages = Array.isArray(obj.flow_paused_stages)
    ? obj.flow_paused_stages.filter((v): v is string => typeof v === 'string')
    : []

  const runCostResult = isRecord(obj.run_cost) ? toCostSummary(obj.run_cost) : null
  const runCost = runCostResult === null ? undefined : runCostResult

  const runOverheadCostResult = isRecord(obj.run_overhead_cost) ? toCostSummary(obj.run_overhead_cost) : null
  const runOverheadCost = runOverheadCostResult === null ? undefined : runOverheadCostResult

  const coverageIssues: CoverageIssue[] = Array.isArray(obj.coverage_issues)
    ? obj.coverage_issues.map(toCoverageIssue).filter((i): i is CoverageIssue => i !== null)
    : []

  const accounting: AccountingState = isRecord(obj.accounting)
    ? toAccountingState(obj.accounting)
    : { supported: false }

  return {
    flowName,
    stages,
    startedAt,
    runStatus,
    endedAt,
    elapsedAccumulatedMs,
    elapsedSince,
    description,
    idleAccumulatedMs,
    idleSince,
    backoffAccumulatedMs,
    backoffOpenSince,
    capabilities,
    flowPauseState,
    flowPausedStages,
    runCost,
    runOverheadCost,
    coverageIssues,
    accounting,
  }
}

function toStage(raw: unknown): Stage | null {
  const obj = isRecord(raw) ? raw : null
  if (obj === null || typeof obj.id !== 'string') return null

  const status: StageStatus = isStageStatus(obj.status) ? obj.status : 'pending'
  const updatedAt = typeof obj.updated_at === 'string' ? obj.updated_at : ''
  const name = typeof obj.name === 'string' ? obj.name : ''

  const costResult = isRecord(obj.cost) ? toCostSummary(obj.cost) : null
  const cost = costResult === null ? undefined : costResult

  const verify = toVerifyView(obj.verify)

  return {
    id: obj.id,
    name,
    status,
    updatedAt,
    interactive: obj.interactive === true,
    autonomous: obj.autonomous === true,
    autoApprove: obj.auto_approve === true,
    hasDialog: obj.has_dialog === true,
    showPlan: obj.show_plan === true,
    showDialog: obj.show_dialog === true,
    isScript: obj.is_script === true,
    pausedFrom: isStageStatus(obj.paused_from) ? obj.paused_from : '',
    preNote: typeof obj.pre_note === 'string' ? obj.pre_note : '',
    buttons: Array.isArray(obj.buttons) ? obj.buttons.filter((x): x is string => typeof x === 'string') : [],
    cost,
    verify,
  }
}

// toVerifyView normalizes GET /api/status's stage.verify (Go StageView.Verify,
// pkg/server/stageview.go) defensively — the server shape isn't blindly
// trusted: an unrecognized/missing phase or a wrong-typed step/command falls
// back rather than producing a malformed StageVerify.
const VERIFY_PHASES = new Set(['running', 'pass', 'needs_changes', 'inconclusive', 'error'])

function toVerifyView(raw: unknown): StageVerify | undefined {
  const obj = isRecord(raw) ? raw : null
  if (obj === null) return undefined

  const phase = typeof obj.phase === 'string' && VERIFY_PHASES.has(obj.phase) ? (obj.phase as StageVerify['phase']) : null
  if (phase === null) return undefined

  const step = typeof obj.step === 'number' ? obj.step : 0
  const command = typeof obj.command === 'string' ? obj.command : ''

  return { step, command, phase }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object'
}

function isStageStatus(value: unknown): value is StageStatus {
  return typeof value === 'string' && (STAGE_STATUSES as readonly string[]).includes(value)
}

function toCostSummary(raw: Record<string, unknown>): CostSummary | null {
  // Проверяем обязательные поля для распознавания валидного CostView
  if (typeof raw.display_cost !== 'string' || typeof raw.coverage !== 'string') {
    return null
  }

  const displayCost = raw.display_cost
  const coverage = (raw.coverage === 'full' || raw.coverage === 'partial' || raw.coverage === 'none')
    ? raw.coverage
    : 'none'
  const estimatedCostUsd = typeof raw.estimated_cost_usd === 'number' ? raw.estimated_cost_usd : 0
  const metered = typeof raw.metered === 'number' ? raw.metered : 0
  const pricedInvocations = typeof raw.priced_invocations === 'number' ? raw.priced_invocations : 0
  const unpriced = typeof raw.unpriced === 'number' ? raw.unpriced : 0
  const unmetered = typeof raw.unmetered === 'number' ? raw.unmetered : 0
  const models = Array.isArray(raw.models) ? raw.models.filter((m): m is string => typeof m === 'string') : []

  const uncachedInput = typeof raw.uncached_input === 'number' ? raw.uncached_input : 0
  const cacheRead = typeof raw.cache_read === 'number' ? raw.cache_read : 0
  const cacheWrite5m = typeof raw.cache_write_5m === 'number' ? raw.cache_write_5m : 0
  const cacheWrite1h = typeof raw.cache_write_1h === 'number' ? raw.cache_write_1h : 0
  const cacheWriteOther = typeof raw.cache_write_other === 'number' ? raw.cache_write_other : 0
  const output = typeof raw.output === 'number' ? raw.output : 0
  const reasoningOutput = typeof raw.reasoning_output === 'number' ? raw.reasoning_output : 0
  const totalTokens = typeof raw.total_tokens === 'number' ? raw.total_tokens : 0
  const cacheWriteTotal = typeof raw.cache_write_total === 'number' ? raw.cache_write_total : 0
  const cacheHitRatio = typeof raw.cache_hit_ratio === 'number' ? raw.cache_hit_ratio : null

  const phases: Record<string, number> = isRecord(raw.phases)
    ? Object.fromEntries(
        Object.entries(raw.phases).filter((entry): entry is [string, number] => typeof entry[1] === 'number')
      )
    : {}

  return {
    displayCost,
    coverage,
    estimatedCostUsd,
    metered,
    pricedInvocations,
    unpriced,
    unmetered,
    models,
    uncachedInput,
    cacheRead,
    cacheWrite5m,
    cacheWrite1h,
    cacheWriteOther,
    output,
    reasoningOutput,
    totalTokens,
    cacheWriteTotal,
    cacheHitRatio,
    phases,
  }
}

function toCoverageIssue(raw: unknown): CoverageIssue | null {
  const obj = isRecord(raw) ? raw : null
  if (obj === null) return null

  const kind = obj.kind === 'unpriced' || obj.kind === 'unmetered' ? obj.kind : null
  if (kind === null) return null

  const phase = typeof obj.phase === 'string' ? obj.phase : ''
  const channel = typeof obj.channel === 'string' ? obj.channel : ''
  const model = typeof obj.model === 'string' ? obj.model : ''
  const reason = typeof obj.reason === 'string' ? obj.reason : ''
  const count = typeof obj.count === 'number' ? obj.count : 0

  const rawAttribution = isRecord(obj.attribution) ? obj.attribution : {}
  const attributionKind = rawAttribution.kind === 'stage' || rawAttribution.kind === 'run_overhead' || rawAttribution.kind === 'unknown'
    ? rawAttribution.kind
    : 'unknown'

  let attribution
  if (attributionKind === 'stage' && typeof rawAttribution.stage_id === 'string') {
    attribution = { kind: 'stage' as const, stageId: rawAttribution.stage_id }
  } else if (attributionKind === 'run_overhead') {
    attribution = { kind: 'run_overhead' as const }
  } else {
    attribution = { kind: 'unknown' as const }
  }

  return { kind, attribution, phase, channel, model, reason, count }
}

function toAccountingState(raw: Record<string, unknown>): AccountingState {
  // Если health присутствует и valid, это новый бэкенд
  if (typeof raw.health === 'string' && (raw.health === 'ok' || raw.health === 'unavailable')) {
    const hasData = raw.has_data === true
    // show_money отсутствует у старого бэкенда → трактуем как false (деньги
    // спрятаны по умолчанию, показываем только токены).
    const showMoney = raw.show_money === true
    return { supported: true, health: raw.health, hasData, showMoney }
  }
  // Иначе — старый бэкенд без информации о затратах
  return { supported: false }
}
