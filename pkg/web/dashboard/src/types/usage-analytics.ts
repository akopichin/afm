// Клиентский контракт GET /api/usage/analytics (pkg/server/usage_analytics_dto.go); тест читает те же golden JSON.
// null — «неизвестно», 0 — измеренный ноль. Деньги и токены необязательны: в режиме, который их скрывает, их нет
// на любой глубине — парсер выбрасывает их, даже если сервер прислал лишнее.

export const USAGE_MODES = ['full', 'no_money', 'time_only'] as const
export type UsageAnalyticsMode = (typeof USAGE_MODES)[number]

export type UsageMetric = 'cost' | 'agent_time' | 'tokens'
export type UsageView = 'breakdown' | 'jobs' | 'charts'
export type UsageGroup = 'model' | 'agent'
export type UsageScope = 'all' | 'stage' | 'run_wide' | 'unattributed'
export type UsageSort = 'cost' | 'tokens' | 'time' | 'jobs' | 'name' | 'model' | 'started'
export type UsageDir = 'asc' | 'desc'

const VIEWS: readonly UsageView[] = ['breakdown', 'jobs', 'charts']
const GROUPS: readonly UsageGroup[] = ['model', 'agent']
const SCOPES: readonly UsageScope[] = ['all', 'stage', 'run_wide', 'unattributed']
const SORTS: readonly UsageSort[] = ['cost', 'tokens', 'time', 'jobs', 'name', 'model', 'started']
const DIRS: readonly UsageDir[] = ['asc', 'desc']

// /api/status: ровно столько, чтобы открыть детали и заметить, что они изменились. revision непрозрачна.
export type UsageAnalyticsStatus = { revision: string; mode: UsageAnalyticsMode; counts: UsageCounts | null }

// Число моделей и агентов run-wide каталога; приходит в /api/status, чтобы вход в детали показал их, не запрашивая аналитику.
export type UsageCounts = { models: number; agents: number }

export type UsageTime = { durationMs: number | null; measuredJobs: number; unknownJobs: number }

export type UsageTokenCounts = {
  uncachedInput: number
  cacheRead: number
  cacheWrite5m: number
  cacheWrite1h: number
  cacheWriteOther: number
  output: number
  reasoningOutput: number
  total: number
}

export type UsageTokens = {
  counts: UsageTokenCounts | null
  meteredJobs: number
  unmeteredJobs: number
  // Запущены, usage ещё не пришёл.
  pendingJobs: number
}

export type UsageMoney = { estimatedCostUsd: number | null; pricedJobs: number; unpricedJobs: number }

// Данные, существующие не во всех режимах: tokens — кроме time_only, money — только в full.
export type UsageSpend = { tokens?: UsageTokens; money?: UsageMoney }

export type UsageMetrics = { jobs: number; activeJobs: number; agentTime: UsageTime } & UsageSpend

export type UsageAgent = { id: string; kind: string; title: string; outsideRunTotal: boolean }

// colorOrdinal — порядок первого появления модели в ране; null у корзины Unknown.
export type UsageModelRef = { key: string; channel: string; model: string; colorOrdinal: number | null }

export type UsageModelState = 'single' | 'mixed' | 'unknown'
export type UsageModelSource = 'observed' | 'hint' | 'legacy' | 'mixed'

// Строка Breakdown/сравнения: одна модель или один агент со всеми его jobs.
export type UsageRow = UsageMetrics & {
  key: string
  label: string
  agent: UsageAgent | null
  model: UsageModelRef | null
  modelState: UsageModelState | null
  modelSource: UsageModelSource | null
  // agents — число разных агентов с job этой модели в границах запроса; у строки агента и у старого сервера null.
  agents: number | null
}

export type UsageJobStatus = 'running' | 'ok' | 'error' | 'interrupted' | 'idle_timeout' | 'usage_limit' | 'cancelled' | 'yielded'

// durationMs — null, когда конец неизвестен, у running — прошедшее время на asOf; runOffsetMs — null, если у job
// нет места на шкале рана.
export type UsageJob = UsageSpend & {
  id: string
  title: string
  agent: UsageAgent
  stageId: string
  model: UsageModelRef | null
  modelSource: UsageModelSource | null
  status: UsageJobStatus
  startedAt: string
  runOffsetMs: number | null
  durationMs: number | null
}

export type UsageFocus = { status: 'found' | 'filtered_out' | 'not_found'; jobId: string; index: number | null }

export type UsageJobsPage = {
  items: UsageJob[]
  total: number
  limit: number
  nextCursor: string | null
  prevCursor: string | null
  focus: UsageFocus | null
}

export type UsageInterval = {
  jobId: string
  title: string
  agentId: string
  modelKey: string
  stageId: string
  status: UsageJobStatus
  runOffsetMs: number
  durationMs: number
}

export type UsageTimeline = {
  rangeStartMs: number
  rangeEndMs: number
  intervals: UsageInterval[]
  total: number
  limit: number
  truncated: boolean
  unplaced: number
}

export type UsageCoverageStatus = 'full' | 'partial' | 'unknown'
export type UsageCoverage = { status: UsageCoverageStatus; known: number; unknown: number; reasons: string[] }

export type UsageCompleteness = { time: UsageCoverage; tokens?: UsageCoverage; price?: UsageCoverage }

export type UsageStoreHealth = 'ok' | 'unavailable' | 'absent' | 'disabled'
export type UsageHealth = { usage: UsageStoreHealth; jobs: UsageStoreHealth; startFailedJobs: number }

// Нормализованный запрос, на который сервер ответил: клиент отбрасывает ответы, не совпавшие с его состоянием.
export type UsageQuery = {
  view: UsageView
  group: UsageGroup
  scope: UsageScope
  stageId: string
  agent: string
  model: string
  sort: UsageSort
  dir: UsageDir
  limit: number | null
}

export type UsageCatalog = {
  stages: { id: string; title: string }[]
  agents: UsageAgent[]
  models: UsageModelRef[]
  scopes: { runWide: boolean; unattributed: boolean }
}

export type UsageAnalytics = {
  view: UsageView
  asOf: string
  revision: string
  mode: UsageAnalyticsMode
  metrics: UsageMetric[]
  health: UsageHealth
  completeness: UsageCompleteness
  query: UsageQuery
  totals: UsageMetrics
  outsideRunTotal: UsageMetrics | null
  catalog: UsageCatalog
  breakdown: { group: UsageGroup; rows: UsageRow[] } | null
  jobs: UsageJobsPage | null
  charts: { group: UsageGroup; comparison: UsageRow[]; timeline: UsageTimeline } | null
}

export type UsageErrorCode = 'invalid_param' | 'stale_cursor'
export type UsageAnalyticsErrorBody = { code: UsageErrorCode; message: string }

// metricsForMode повторяет UsageAnalyticsMode.Metrics на сервере: порядок — порядок кнопок Compare.
export function metricsForMode(mode: UsageAnalyticsMode): UsageMetric[] {
  const metrics: UsageMetric[] = []
  if (mode === 'full') metrics.push('cost')
  metrics.push('agent_time')
  if (mode !== 'time_only') metrics.push('tokens')
  return metrics
}

// modeAllowsSort — сервер отвечает 400 на сортировку, для которой в режиме нет данных.
export function modeAllowsSort(mode: UsageAnalyticsMode, sort: UsageSort): boolean {
  if (sort === 'cost') return mode === 'full'
  if (sort === 'tokens') return mode !== 'time_only'
  return true
}

// allowedSorts — ключи сортировки вида; у Charts сортировки нет.
export function allowedSorts(view: UsageView): UsageSort[] {
  if (view === 'breakdown') return ['cost', 'tokens', 'time', 'jobs', 'name']
  if (view === 'jobs') return ['cost', 'tokens', 'time', 'model', 'started', 'name']
  return []
}

type Obj = Record<string, unknown>

function isObj(value: unknown): value is Obj {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}

function oneOf<T extends string>(allowed: readonly T[], value: unknown): T | null {
  return typeof value === 'string' && (allowed as readonly string[]).includes(value) ? (value as T) : null
}

function str(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function strOrNull(value: unknown): string | null {
  return typeof value === 'string' && value !== '' ? value : null
}

function num(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

// countOrNull — неотрицательное целое; всё остальное (поле отсутствует у старого сервера) — null.
function countOrNull(value: unknown): number | null {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0 ? value : null
}

// numOrNull — null означает «неизвестно» и не превращается в 0.
function numOrNull(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

function list(value: unknown): unknown[] {
  return Array.isArray(value) ? value : []
}

function parseTime(raw: unknown): UsageTime {
  const obj = isObj(raw) ? raw : {}
  return { durationMs: numOrNull(obj.duration_ms), measuredJobs: num(obj.measured_jobs), unknownJobs: num(obj.unknown_jobs) }
}

function parseTokenCounts(raw: unknown): UsageTokenCounts | null {
  if (!isObj(raw)) return null
  return {
    uncachedInput: num(raw.uncached_input),
    cacheRead: num(raw.cache_read),
    cacheWrite5m: num(raw.cache_write_5m),
    cacheWrite1h: num(raw.cache_write_1h),
    cacheWriteOther: num(raw.cache_write_other),
    output: num(raw.output),
    reasoningOutput: num(raw.reasoning_output),
    total: num(raw.total),
  }
}

function parseTokens(raw: unknown): UsageTokens | undefined {
  if (!isObj(raw)) return undefined
  return {
    counts: parseTokenCounts(raw.counts),
    meteredJobs: num(raw.metered_jobs),
    unmeteredJobs: num(raw.unmetered_jobs),
    pendingJobs: num(raw.pending_jobs),
  }
}

function parseMoney(raw: unknown): UsageMoney | undefined {
  if (!isObj(raw)) return undefined
  return { estimatedCostUsd: numOrNull(raw.estimated_cost_usd), pricedJobs: num(raw.priced_jobs), unpricedJobs: num(raw.unpriced_jobs) }
}

// parseSpend оставляет только то, что режим вправе показывать: ключа нет вовсе, а не undefined-значения.
function parseSpend(raw: Obj, mode: UsageAnalyticsMode): UsageSpend {
  const spend: UsageSpend = {}
  const tokens = mode === 'time_only' ? undefined : parseTokens(raw.tokens)
  const money = mode === 'full' ? parseMoney(raw.money) : undefined
  if (tokens !== undefined) spend.tokens = tokens
  if (money !== undefined) spend.money = money
  return spend
}

function parseMetrics(raw: unknown, mode: UsageAnalyticsMode): UsageMetrics {
  const obj = isObj(raw) ? raw : {}
  return { jobs: num(obj.jobs), activeJobs: num(obj.active_jobs), agentTime: parseTime(obj.agent_time), ...parseSpend(obj, mode) }
}

function parseAgent(raw: unknown): UsageAgent | null {
  if (!isObj(raw) || typeof raw.id !== 'string') return null
  return { id: raw.id, kind: str(raw.kind), title: str(raw.title), outsideRunTotal: raw.outside_run_total === true }
}

function parseModelRef(raw: unknown): UsageModelRef | null {
  if (!isObj(raw) || typeof raw.key !== 'string') return null
  return { key: raw.key, channel: str(raw.channel), model: str(raw.model), colorOrdinal: numOrNull(raw.color_ordinal) }
}

function parseRow(raw: unknown, mode: UsageAnalyticsMode): UsageRow | null {
  if (!isObj(raw) || typeof raw.key !== 'string') return null
  return {
    ...parseMetrics(raw, mode),
    key: raw.key,
    label: str(raw.label),
    agent: parseAgent(raw.agent),
    model: parseModelRef(raw.model),
    modelState: oneOf<UsageModelState>(['single', 'mixed', 'unknown'], raw.model_state),
    modelSource: oneOf<UsageModelSource>(['observed', 'hint', 'legacy', 'mixed'], raw.model_source),
    agents: countOrNull(raw.agents),
  }
}

const JOB_STATUSES: readonly UsageJobStatus[] = ['running', 'ok', 'error', 'interrupted', 'idle_timeout', 'usage_limit', 'cancelled', 'yielded']

function parseJob(raw: unknown, mode: UsageAnalyticsMode): UsageJob | null {
  if (!isObj(raw) || typeof raw.id !== 'string') return null
  const agent = parseAgent(raw.agent)
  const status = oneOf(JOB_STATUSES, raw.status)
  if (agent === null || status === null) return null
  return {
    ...parseSpend(raw, mode),
    id: raw.id,
    title: str(raw.title),
    agent,
    stageId: str(raw.stage_id),
    model: parseModelRef(raw.model),
    modelSource: oneOf<UsageModelSource>(['observed', 'hint', 'legacy', 'mixed'], raw.model_source),
    status,
    startedAt: str(raw.started_at),
    runOffsetMs: numOrNull(raw.run_offset_ms),
    durationMs: numOrNull(raw.duration_ms),
  }
}

function parseFocus(raw: unknown): UsageFocus | null {
  if (!isObj(raw)) return null
  const status = oneOf<UsageFocus['status']>(['found', 'filtered_out', 'not_found'], raw.status)
  if (status === null) return null
  return { status, jobId: str(raw.job_id), index: numOrNull(raw.index) }
}

function parseJobsPage(raw: unknown, mode: UsageAnalyticsMode): UsageJobsPage | null {
  if (!isObj(raw)) return null
  return {
    items: list(raw.items).map((item) => parseJob(item, mode)).filter((job): job is UsageJob => job !== null),
    total: num(raw.total),
    limit: num(raw.limit),
    nextCursor: strOrNull(raw.next_cursor),
    prevCursor: strOrNull(raw.prev_cursor),
    focus: parseFocus(raw.focus),
  }
}

function parseInterval(raw: unknown): UsageInterval | null {
  if (!isObj(raw) || typeof raw.job_id !== 'string') return null
  const status = oneOf(JOB_STATUSES, raw.status)
  if (status === null) return null
  return {
    jobId: raw.job_id,
    title: str(raw.title),
    agentId: str(raw.agent_id),
    modelKey: str(raw.model_key),
    stageId: str(raw.stage_id),
    status,
    runOffsetMs: num(raw.run_offset_ms),
    durationMs: num(raw.duration_ms),
  }
}

function parseTimeline(raw: unknown): UsageTimeline {
  const obj = isObj(raw) ? raw : {}
  return {
    rangeStartMs: num(obj.range_start_ms),
    rangeEndMs: num(obj.range_end_ms),
    intervals: list(obj.intervals).map(parseInterval).filter((i): i is UsageInterval => i !== null),
    total: num(obj.total),
    limit: num(obj.limit),
    truncated: obj.truncated === true,
    unplaced: num(obj.unplaced),
  }
}

function parseRows(raw: unknown, mode: UsageAnalyticsMode): UsageRow[] {
  return list(raw).map((row) => parseRow(row, mode)).filter((row): row is UsageRow => row !== null)
}

function parseCoverage(raw: unknown): UsageCoverage | undefined {
  if (!isObj(raw)) return undefined
  const status = oneOf<UsageCoverageStatus>(['full', 'partial', 'unknown'], raw.status)
  if (status === null) return undefined
  return {
    status,
    known: num(raw.known),
    unknown: num(raw.unknown),
    reasons: list(raw.reasons).filter((r): r is string => typeof r === 'string'),
  }
}

function parseCompleteness(raw: unknown, mode: UsageAnalyticsMode): UsageCompleteness {
  const obj = isObj(raw) ? raw : {}
  const completeness: UsageCompleteness = { time: parseCoverage(obj.time) ?? { status: 'unknown', known: 0, unknown: 0, reasons: [] } }
  const tokens = mode === 'time_only' ? undefined : parseCoverage(obj.tokens)
  const price = mode === 'full' ? parseCoverage(obj.price) : undefined
  if (tokens !== undefined) completeness.tokens = tokens
  if (price !== undefined) completeness.price = price
  return completeness
}

const STORE_HEALTH: readonly UsageStoreHealth[] = ['ok', 'unavailable', 'absent', 'disabled']

function parseHealth(raw: unknown): UsageHealth {
  const obj = isObj(raw) ? raw : {}
  return {
    usage: oneOf(STORE_HEALTH, obj.usage) ?? 'unavailable',
    jobs: oneOf(STORE_HEALTH, obj.jobs) ?? 'unavailable',
    startFailedJobs: num(obj.start_failed_jobs),
  }
}

function parseQuery(raw: unknown, view: UsageView): UsageQuery {
  const obj = isObj(raw) ? raw : {}
  return {
    view: oneOf(VIEWS, obj.view) ?? view,
    group: oneOf(GROUPS, obj.group) ?? 'model',
    scope: oneOf(SCOPES, obj.scope) ?? 'all',
    stageId: str(obj.stage_id),
    agent: str(obj.agent),
    model: str(obj.model),
    sort: oneOf(SORTS, obj.sort) ?? 'time',
    dir: oneOf(DIRS, obj.dir) ?? 'desc',
    limit: numOrNull(obj.limit),
  }
}

function parseCatalog(raw: unknown): UsageCatalog {
  const obj = isObj(raw) ? raw : {}
  const scopes = isObj(obj.scopes) ? obj.scopes : {}
  return {
    stages: list(obj.stages)
      .filter((s): s is Obj => isObj(s) && typeof s.id === 'string')
      .map((s) => ({ id: str(s.id), title: str(s.title) })),
    agents: list(obj.agents).map(parseAgent).filter((a): a is UsageAgent => a !== null),
    models: list(obj.models).map(parseModelRef).filter((m): m is UsageModelRef => m !== null),
    scopes: { runWide: scopes.run_wide === true, unattributed: scopes.unattributed === true },
  }
}

// parseUsageAnalytics приводит ответ сервера к клиентским типам; null — это не ответ аналитики (неизвестный
// вид/режим или нет данных своего вида), его надо считать сбоем, а не пустым результатом.
export function parseUsageAnalytics(raw: unknown): UsageAnalytics | null {
  if (!isObj(raw)) return null
  const view = oneOf(VIEWS, raw.view)
  const mode = oneOf(USAGE_MODES, raw.mode)
  if (view === null || mode === null) return null

  const breakdown = isObj(raw.breakdown)
    ? { group: oneOf(GROUPS, raw.breakdown.group) ?? 'model', rows: parseRows(raw.breakdown.rows, mode) }
    : null
  const jobs = parseJobsPage(raw.jobs, mode)
  const charts = isObj(raw.charts)
    ? { group: oneOf(GROUPS, raw.charts.group) ?? 'model', comparison: parseRows(raw.charts.comparison, mode), timeline: parseTimeline(raw.charts.timeline) }
    : null
  const body = { breakdown, jobs, charts }[view]
  if (body === null) return null

  return {
    view,
    asOf: str(raw.as_of),
    revision: str(raw.revision),
    mode,
    metrics: metricsForMode(mode),
    health: parseHealth(raw.health),
    completeness: parseCompleteness(raw.completeness, mode),
    query: parseQuery(raw.query, view),
    totals: parseMetrics(raw.totals, mode),
    outsideRunTotal: isObj(raw.outside_run_total) ? parseMetrics(raw.outside_run_total, mode) : null,
    catalog: parseCatalog(raw.catalog),
    breakdown,
    jobs,
    charts,
  }
}

// parseUsageAnalyticsStatus — блок analytics из /api/status; без валидных revision и mode (старый сервер)
// capability считается отсутствующей.
export function parseUsageAnalyticsStatus(raw: unknown): UsageAnalyticsStatus | null {
  if (!isObj(raw) || typeof raw.revision !== 'string') return null
  const mode = oneOf(USAGE_MODES, raw.mode)
  if (mode === null) return null
  const models = countOrNull(raw.models)
  const agents = countOrNull(raw.agents)
  return { revision: raw.revision, mode, counts: models !== null && agents !== null ? { models, agents } : null }
}

export function parseUsageAnalyticsError(raw: unknown): UsageAnalyticsErrorBody | null {
  if (!isObj(raw)) return null
  const code = oneOf<UsageErrorCode>(['invalid_param', 'stale_cursor'], raw.code)
  return code === null ? null : { code, message: str(raw.message) }
}
