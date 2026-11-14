// Типы бюджетов — JSON-формы contracts/budget (View, Spec, Spend, Override, Notice) и ответов /api/budgets*.
// Клиент только показывает решения engine и ничего не пересчитывает сам.

export type BudgetPolicy = 'hard' | 'medium' | 'info'
export type BudgetMetric = 'tokens' | 'money'
export type BudgetLevel = '' | 'ok' | 'warning' | 'reached'
export type BudgetViewKind = 'run' | 'stage' | 'helper' | 'run_agent'
export type BudgetSource = 'declaration' | 'profile' | 'request' | 'none'
export type BudgetOverrideKind = 'none' | 'limit' | 'unlimited'
export type BudgetHoldReason = 'limit_reached' | 'parent_limit_reached' | 'rates_missing' | 'accounting_unavailable'
export type BudgetAction = 'update' | 'resume' | 'save_to_yaml'
export type BudgetNoticeKind = 'warning' | 'limit_reached' | 'updated' | 'resumed'

// BudgetSpec — лимит области: хотя бы одно из tokens/moneyUsd задано (undefined — метрика без лимита).
export type BudgetSpec = {
  tokens?: number
  moneyUsd?: number
  stopPolicy: BudgetPolicy
}

export type BudgetSpend = {
  tokens: number
  moneyNanoUsd: number
  unmetered: number
  unpriced: number
  notExplicit: number
}

export type BudgetEvaluation = { tokens: BudgetLevel; money: BudgetLevel }

export type BudgetHold = { reason: BudgetHoldReason; scope: string; metric?: BudgetMetric }

export type BudgetParentLimit = {
  scope: string
  limit: BudgetSpec
  spent: BudgetSpend
  evaluation: BudgetEvaluation
}

export type BudgetOverride = { kind: BudgetOverrideKind; spec?: BudgetSpec }

export type BudgetView = {
  scope: string
  kind: BudgetViewKind
  revision: number
  base: BudgetSpec | null
  baseSource: BudgetSource
  // declarationProfile — профиль помощника из declaration (kind stage_assistant); пусто, если в декларации его нет.
  declarationProfile?: string
  override: BudgetOverride
  effective: BudgetSpec | null
  spent: BudgetSpend
  evaluation: BudgetEvaluation
  parentLimits: BudgetParentLimit[]
  hold: BudgetHold | null
  draining: boolean
  actions: BudgetAction[]
}

// BudgetYaml — блок «Also save to flow.yaml» ответа GET /api/budgets/view; declared — значение в файле сейчас.
export type BudgetYaml = {
  available: boolean
  reason: string
  file: string
  declared: BudgetSpec | null
}

export type BudgetExport = { status: 'saved' | 'failed' | 'unavailable'; path: string; error: string }

export type BudgetResumeError = { code: string; message: string }

export type BudgetUpdateOutcome = {
  view: BudgetView
  export: BudgetExport | null
  resumeError: BudgetResumeError | null
}

export type BudgetNotice = {
  kind: BudgetNoticeKind
  scope: string
  metric?: BudgetMetric
  limit: BudgetSpec | null
  spent: BudgetSpend
  policy?: BudgetPolicy
  revision: number
}

// Тело POST /api/budgets/update (snake_case — формат contracts/budget.UpdateRequest).
export type BudgetUpdateRequest = {
  scope: string
  expectedRevision: number
  override: BudgetOverride
  saveToYaml: boolean
  resume: boolean
}

export const BUDGET_EVENT_TYPES: Record<string, BudgetNoticeKind> = {
  budget_warning: 'warning',
  budget_limit_reached: 'limit_reached',
  budget_updated: 'updated',
  budget_resumed: 'resumed',
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}

function num(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

function str(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function oneOf<T extends string>(value: unknown, allowed: readonly T[], fallback: T): T {
  return typeof value === 'string' && (allowed as readonly string[]).includes(value) ? (value as T) : fallback
}

const POLICIES = ['hard', 'medium', 'info'] as const
const LEVELS = ['', 'ok', 'warning', 'reached'] as const

export function parseBudgetSpec(raw: unknown): BudgetSpec | null {
  if (!isRecord(raw)) return null
  const tokens = num(raw.tokens)
  const moneyUsd = num(raw.money_usd)
  return {
    ...(tokens > 0 ? { tokens } : {}),
    ...(moneyUsd > 0 ? { moneyUsd } : {}),
    stopPolicy: oneOf(raw.stop_policy, POLICIES, 'hard'),
  }
}

export function parseBudgetSpend(raw: unknown): BudgetSpend {
  const obj = isRecord(raw) ? raw : {}
  return {
    tokens: num(obj.tokens),
    moneyNanoUsd: num(obj.money_nano_usd),
    unmetered: num(obj.unmetered),
    unpriced: num(obj.unpriced),
    notExplicit: num(obj.not_explicit),
  }
}

function parseEvaluation(raw: unknown): BudgetEvaluation {
  const obj = isRecord(raw) ? raw : {}
  return { tokens: oneOf(obj.tokens, LEVELS, ''), money: oneOf(obj.money, LEVELS, '') }
}

function parseHold(raw: unknown): BudgetHold | null {
  if (!isRecord(raw)) return null
  const metric = raw.metric === 'tokens' || raw.metric === 'money' ? raw.metric : undefined
  return {
    reason: oneOf(raw.reason, ['limit_reached', 'parent_limit_reached', 'rates_missing', 'accounting_unavailable'] as const, 'limit_reached'),
    scope: str(raw.scope),
    ...(metric !== undefined ? { metric } : {}),
  }
}

function parseOverride(raw: unknown): BudgetOverride {
  const obj = isRecord(raw) ? raw : {}
  const kind = oneOf(obj.kind, ['none', 'limit', 'unlimited'] as const, 'none')
  if (kind !== 'limit') return { kind }
  const spec = parseBudgetSpec(obj.spec)
  return spec === null ? { kind: 'none' } : { kind, spec }
}

function parseParentLimit(raw: unknown): BudgetParentLimit | null {
  if (!isRecord(raw)) return null
  const limit = parseBudgetSpec(raw.limit)
  if (limit === null || str(raw.scope) === '') return null
  return { scope: str(raw.scope), limit, spent: parseBudgetSpend(raw.spent), evaluation: parseEvaluation(raw.evaluation) }
}

// parseBudgetView — null для ответа без scope: такой view показать нечем.
export function parseBudgetView(raw: unknown): BudgetView | null {
  if (!isRecord(raw) || str(raw.scope) === '') return null
  const actions = Array.isArray(raw.actions)
    ? raw.actions.filter((a): a is BudgetAction => a === 'update' || a === 'resume' || a === 'save_to_yaml')
    : []
  return {
    scope: str(raw.scope),
    kind: oneOf(raw.kind, ['run', 'stage', 'helper', 'run_agent'] as const, 'run'),
    revision: num(raw.revision),
    base: parseBudgetSpec(raw.base),
    baseSource: oneOf(raw.base_source, ['declaration', 'profile', 'request', 'none'] as const, 'none'),
    declarationProfile: isRecord(raw.declaration) ? str(raw.declaration.profile) : undefined,
    override: parseOverride(raw.override),
    effective: parseBudgetSpec(raw.effective),
    spent: parseBudgetSpend(raw.spent),
    evaluation: parseEvaluation(raw.evaluation),
    parentLimits: Array.isArray(raw.parent_limits)
      ? raw.parent_limits.map(parseParentLimit).filter((p): p is BudgetParentLimit => p !== null)
      : [],
    hold: parseHold(raw.hold),
    draining: raw.draining === true,
    actions,
  }
}

export function parseBudgetYaml(raw: unknown): BudgetYaml | null {
  if (!isRecord(raw)) return null
  return { available: raw.available === true, reason: str(raw.reason), file: str(raw.file), declared: parseBudgetSpec(raw.declared) }
}

export function parseBudgetExport(raw: unknown): BudgetExport | null {
  if (!isRecord(raw)) return null
  return { status: oneOf(raw.status, ['saved', 'failed', 'unavailable'] as const, 'unavailable'), path: str(raw.path), error: str(raw.error) }
}

export function parseBudgetUpdateOutcome(raw: unknown): BudgetUpdateOutcome | null {
  if (!isRecord(raw)) return null
  const view = parseBudgetView(raw.view)
  if (view === null) return null
  const resume = isRecord(raw.resume_error) ? { code: str(raw.resume_error.code), message: str(raw.resume_error.message) } : null
  return { view, export: parseBudgetExport(raw.export), resumeError: resume }
}

// parseBudgetNotice — полезная нагрузка budget_*-события; kind берётся из payload, иначе из типа события.
export function parseBudgetNotice(eventType: string, raw: unknown): BudgetNotice | null {
  const kind = BUDGET_EVENT_TYPES[eventType]
  if (kind === undefined || !isRecord(raw)) return null
  const scope = str(raw.scope)
  if (scope === '') return null
  const payloadKind = oneOf(raw.kind, ['warning', 'limit_reached', 'updated', 'resumed'] as const, kind)
  const metric = raw.metric === 'tokens' || raw.metric === 'money' ? raw.metric : undefined
  const policy = oneOf(raw.policy, ['', ...POLICIES] as const, '')
  return {
    kind: payloadKind,
    scope,
    ...(metric !== undefined ? { metric } : {}),
    limit: parseBudgetSpec(raw.limit),
    spent: parseBudgetSpend(raw.spent),
    ...(policy !== '' ? { policy } : {}),
    revision: num(raw.revision),
  }
}

// budgetSpecToWire — Spec в форме contracts/budget (money_usd, пустая метрика опущена).
export function budgetSpecToWire(spec: BudgetSpec): Record<string, unknown> {
  return {
    ...(spec.tokens !== undefined ? { tokens: spec.tokens } : {}),
    ...(spec.moneyUsd !== undefined ? { money_usd: spec.moneyUsd } : {}),
    stop_policy: spec.stopPolicy,
  }
}

export function budgetUpdateToWire(req: BudgetUpdateRequest): Record<string, unknown> {
  return {
    scope: req.scope,
    expected_revision: req.expectedRevision,
    override: {
      kind: req.override.kind,
      ...(req.override.kind === 'limit' && req.override.spec !== undefined ? { spec: budgetSpecToWire(req.override.spec) } : {}),
    },
    ...(req.saveToYaml ? { save_to_yaml: true } : {}),
    ...(req.resume ? { resume: true } : {}),
  }
}

// budgetEventScope — область budget_*-события; null для событий других типов и для payload без области.
export function budgetEventScope(event: { type: string; payload: unknown }): string | null {
  if (!(event.type in BUDGET_EVENT_TYPES) || !isRecord(event.payload)) return null
  const scope = str(event.payload.scope)
  return scope === '' ? null : scope
}

// isRunBudgetNotice — notice самого рана: у него нет стадии, поэтому его показывают в ленте каждой стадии.
export function isRunBudgetNotice(event: { type: string; payload: unknown; stageId: string }): boolean {
  return event.stageId === '' && budgetEventScope(event) === 'run'
}

// isRunAgentBudgetNotice — notice run agent (область task: без стадии): ему место в Team › Activity.
export function isRunAgentBudgetNotice(event: { type: string; payload: unknown; stageId: string }): boolean {
  return event.stageId === '' && (budgetEventScope(event)?.startsWith('task:') ?? false)
}
