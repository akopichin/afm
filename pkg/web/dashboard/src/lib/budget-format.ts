import type {
  BudgetEvaluation,
  BudgetLevel,
  BudgetMetric,
  BudgetNotice,
  BudgetPolicy,
  BudgetSpec,
  BudgetSpend,
  BudgetView,
  BudgetViewKind,
} from '../types/budget'

// Показ бюджетов: подписи областей и цифры лимитов. Не зависит от accounting.supported/show_money — денежный
// лимит виден даже при скрытой аналитике.

const NANO_PER_USD = 1e9

// formatBudgetTokens — «82k» / «1.5m» (как в YAML-лимитах); меньше тысячи — число как есть.
export function formatBudgetTokens(n: number): string {
  if (n < 1000) return String(n)
  const [divisor, unit] = n >= 1_000_000 ? [1_000_000, 'm'] : [1000, 'k']
  const rounded = Math.round((n / divisor) * 10) / 10
  return `${Number.isInteger(rounded) ? rounded : rounded.toFixed(1)}${unit}`
}

// formatBudgetMoney — «$8.20»; доли цента (тестовые тарифы) не теряются в «$0.00».
export function formatBudgetMoney(usd: number): string {
  if (usd === 0 || usd >= 0.01) return `$${usd.toFixed(2)}`
  return `$${Number(usd.toPrecision(2))}`
}

export function spendMoneyUsd(spent: BudgetSpend): number {
  return spent.moneyNanoUsd / NANO_PER_USD
}

const tokensExact = (spent: BudgetSpend): boolean => spent.unmetered === 0
const moneyExact = (spent: BudgetSpend): boolean => spent.unmetered === 0 && spent.unpriced === 0 && spent.notExplicit === 0

const LEVEL_RANK: Record<BudgetLevel, number> = { '': 0, ok: 1, warning: 2, reached: 3 }

export function worstLevel(evaluation: BudgetEvaluation): BudgetLevel {
  return LEVEL_RANK[evaluation.tokens] >= LEVEL_RANK[evaluation.money] ? evaluation.tokens : evaluation.money
}

// worstMetric — лимитированная метрика с самым высоким уровнем; при равенстве — токены.
export function worstMetric(spec: BudgetSpec, evaluation: BudgetEvaluation): BudgetMetric | null {
  const tokens = spec.tokens !== undefined
  const money = spec.moneyUsd !== undefined
  if (tokens && money) return LEVEL_RANK[evaluation.money] > LEVEL_RANK[evaluation.tokens] ? 'money' : 'tokens'
  if (tokens) return 'tokens'
  return money ? 'money' : null
}

function metricFigure(metric: BudgetMetric, spec: BudgetSpec, spent: BudgetSpend): string {
  if (metric === 'tokens') {
    const text = `${formatBudgetTokens(spent.tokens)} / ${formatBudgetTokens(spec.tokens ?? 0)} tokens`
    return tokensExact(spent) ? text : `${text} (incomplete)`
  }
  const text = `${formatBudgetMoney(spendMoneyUsd(spent))} / ${formatBudgetMoney(spec.moneyUsd ?? 0)}`
  return moneyExact(spent) ? text : `${text} (incomplete)`
}

// budgetFigures — «82k / 100k tokens · $8.20 / $10.00»: цифры всех лимитированных метрик, первой — та, что
// сработала (first), иначе худшая по оценке.
export function budgetFigures(spec: BudgetSpec, spent: BudgetSpend, evaluation?: BudgetEvaluation, first?: BudgetMetric): string {
  const metrics: BudgetMetric[] = []
  if (spec.tokens !== undefined) metrics.push('tokens')
  if (spec.moneyUsd !== undefined) metrics.push('money')
  const lead = first ?? (evaluation !== undefined ? worstMetric(spec, evaluation) : null)
  if (lead !== null && metrics[0] !== lead) metrics.sort((a, b) => Number(b === lead) - Number(a === lead))
  return metrics.map((m) => metricFigure(m, spec, spent)).join(' · ')
}

// spendFigures — у области без лимита есть только расход.
export function spendFigures(spent: BudgetSpend): string {
  return `${formatBudgetTokens(spent.tokens)} tokens · ${formatBudgetMoney(spendMoneyUsd(spent))}`
}

// scopeLabelFallback — подпись по одному ID области, когда ни view, ни имён помощников ещё нет.
export function scopeLabelFallback(scope: string, kind?: BudgetViewKind): string {
  if (scope === 'run') return 'Run'
  if (scope.startsWith('stage:')) return `Stage ${scope.slice('stage:'.length)}`
  const taskId = scope.startsWith('task:') ? scope.slice('task:'.length) : scope
  return kind === 'run_agent' ? `Run agent ${taskId}` : `Helper task ${taskId}`
}

export type BudgetScopeNames = ReadonlyMap<string, string>

export const NO_SCOPE_NAMES: BudgetScopeNames = new Map()

export function scopeLabel(scope: string, names: BudgetScopeNames, kind?: BudgetViewKind): string {
  return names.get(scope) ?? scopeLabelFallback(scope, kind)
}

// scopeKindWord — «stage»/«helper»/… для «No limit for this <kind>».
export function scopeKindWord(kind: BudgetViewKind): string {
  return kind === 'run_agent' ? 'run agent' : kind
}

export const POLICY_TITLES: Record<BudgetPolicy, string> = {
  hard: 'Hard stop',
  medium: 'Finish active work',
  info: 'Warn only',
}

export const POLICY_DESCRIPTIONS: Record<BudgetPolicy, string> = {
  hard: 'Interrupts the running work of this scope when the limit is reached.',
  medium: 'Lets work that already started finish; nothing new starts.',
  info: 'Only warns; work continues past the limit.',
}

export type BudgetTone = 'warning' | 'danger' | 'neutral'

export type BudgetNoticeText = { text: string; tone: BudgetTone; editable: boolean }

const LIMIT_ACTION: Record<BudgetPolicy, string> = {
  hard: 'stopped',
  medium: 'finishing active work, nothing new starts',
  info: 'limit exceeded, work continues',
}

// noticeText — строка ленты по notice: цифры берутся из самого notice, а не из текущего view.
export function noticeText(notice: BudgetNotice, names: BudgetScopeNames): BudgetNoticeText {
  const label = scopeLabel(notice.scope, names)
  const figures = notice.limit === null ? spendFigures(notice.spent) : budgetFigures(notice.limit, notice.spent, undefined, notice.metric)
  switch (notice.kind) {
    case 'warning':
      return { text: `Budget 80% · ${label}: ${figures}`, tone: 'warning', editable: true }
    case 'limit_reached': {
      const policy = notice.policy ?? notice.limit?.stopPolicy ?? 'hard'
      return { text: `Budget limit reached · ${label}: ${figures} — ${LIMIT_ACTION[policy]}`, tone: policy === 'hard' ? 'danger' : 'warning', editable: true }
    }
    case 'updated':
      return { text: `Budget updated · ${label}: ${notice.limit === null ? 'no limit' : figures}`, tone: 'neutral', editable: false }
    case 'resumed':
      return { text: `Budget resumed · ${label}`, tone: 'neutral', editable: false }
  }
}

// viewPercent — доля худшей лимитированной метрики, округлённая вниз; null без лимита.
export function viewPercent(view: BudgetView): number | null {
  const spec = view.effective
  if (spec === null) return null
  const ratios: number[] = []
  if (spec.tokens !== undefined) ratios.push(view.spent.tokens / spec.tokens)
  if (spec.moneyUsd !== undefined) ratios.push(spendMoneyUsd(view.spent) / spec.moneyUsd)
  return ratios.length === 0 ? null : Math.floor(Math.max(...ratios) * 100)
}

// isNearLimit — область на 80% лимита или выше (engine уже посчитал уровень, клиент его не пересчитывает).
export function isNearLimit(view: BudgetView): boolean {
  const level = worstLevel(view.evaluation)
  return level === 'warning' || level === 'reached'
}

// holdTitle/holdText — бюджетный текст баннера стадии по причине удержания.
export function holdTitle(view: BudgetView): string {
  return view.hold?.reason === 'rates_missing' || view.hold?.reason === 'accounting_unavailable' ? 'Budget cannot be checked' : 'Stopped by budget'
}

export function holdText(view: BudgetView, names: BudgetScopeNames): string {
  const hold = view.hold
  if (hold === null) return ''
  const owner = scopeLabel(hold.scope !== '' ? hold.scope : view.scope, names)
  switch (hold.reason) {
    case 'limit_reached':
      return hold.scope === 'run' || (hold.scope === '' && view.scope === 'run')
        ? 'The run budget is used up. Change the limit to continue.'
        : `The budget of ${owner} is used up. Change the limit to continue.`
    case 'parent_limit_reached':
      return `The budget of ${owner} is used up, so work here is stopped too. Change that limit to continue.`
    case 'rates_missing':
      return 'A money budget needs explicit rates for this model in config.yaml. Add them or use a token limit.'
    case 'accounting_unavailable':
      return 'Usage accounting is unavailable, so the budget cannot be checked. Restore accounting to continue.'
  }
}

// statusText — короткий текст статуса помощника/агента; null — бюджет не заслуживает слова.
export function statusText(view: BudgetView | undefined): string | null {
  if (view === undefined) return null
  if (view.hold !== null) return 'Stopped by budget'
  if (view.draining) return 'Finishing active work'
  const percent = viewPercent(view)
  return percent !== null && isNearLimit(view) ? `Budget ${percent}%` : null
}

// limitText — лимит без расхода: «100k tokens · $10.00».
export function limitText(spec: BudgetSpec): string {
  const parts: string[] = []
  if (spec.tokens !== undefined) parts.push(`${formatBudgetTokens(spec.tokens)} tokens`)
  if (spec.moneyUsd !== undefined) parts.push(formatBudgetMoney(spec.moneyUsd))
  return parts.join(' · ')
}

// remainingText — остаток или перерасход по каждой лимитированной метрике: «18k tokens left · over by $0.40».
export function remainingText(spec: BudgetSpec, spent: BudgetSpend): string {
  const parts: string[] = []
  if (spec.tokens !== undefined) {
    const left = spec.tokens - spent.tokens
    parts.push(left >= 0 ? `${formatBudgetTokens(left)} tokens left` : `over by ${formatBudgetTokens(-left)} tokens`)
  }
  if (spec.moneyUsd !== undefined) {
    const left = spec.moneyUsd - spendMoneyUsd(spent)
    parts.push(left >= 0 ? `${formatBudgetMoney(left)} left` : `over by ${formatBudgetMoney(-left)}`)
  }
  return parts.join(' · ')
}

// stateText — состояние области одним словом для сводки окна.
export function stateText(view: BudgetView): string {
  if (view.hold !== null) return 'Stopped'
  if (view.draining) return 'Finishing active work'
  if (view.effective === null) return 'No limit'
  const level = worstLevel(view.evaluation)
  if (level === 'reached') return 'Limit exceeded, work continues'
  return level === 'warning' ? 'Near the limit' : 'Within the limit'
}

export const SOURCE_TEXT = {
  declaration: 'declared in the flow',
  profile: 'default of the helper profile',
  request: 'set when the work was requested',
  none: 'nothing declared',
} as const

// sourceText — откуда базовый лимит; для профиля называем его id, чтобы было видно, чей это default.
export function sourceText(view: BudgetView): string {
  const profile = view.declarationProfile ?? ''
  return view.baseSource === 'profile' && profile !== '' ? `Default of profile ${profile}` : SOURCE_TEXT[view.baseSource]
}

// blockingParent — родитель, не дающий работать области: удержание, которое он наложил (оно живёт, пока
// родитель не продолжен, даже если его лимит уже поднят), или исчерпанный лимит (info только предупреждает).
export function blockingParent(view: BudgetView): { scope: string } | undefined {
  if (view.hold?.reason === 'parent_limit_reached' && view.hold.scope !== '' && view.hold.scope !== view.scope) return { scope: view.hold.scope }
  return view.parentLimits.find((p) => worstLevel(p.evaluation) === 'reached' && p.limit.stopPolicy !== 'info')
}

// participantScope — область бюджета участника mesh: helper:<taskId> и run:<taskId> — это task:<taskId>.
export function participantScope(p: { id: string; kind: string }): string | null {
  if (p.kind === 'helper' && p.id.startsWith('helper:')) return `task:${p.id.slice('helper:'.length)}`
  if (p.kind === 'run_agent' && p.id.startsWith('run:')) return `task:${p.id.slice('run:'.length)}`
  return null
}

export type BudgetStatusLevel = 'hold' | 'warning' | 'ok'

// statusLevel — hold: область остановлена или дорабатывает; warning: от 80%; иначе ok.
export function statusLevel(view: BudgetView): BudgetStatusLevel {
  if (view.hold !== null || view.draining) return 'hold'
  return isNearLimit(view) ? 'warning' : 'ok'
}

// compactFigure — «82k / 100k» или «$8.20 / $10.00» для худшей метрики: для тесной плитки шапки.
export function compactFigure(view: BudgetView): string | null {
  const spec = view.effective
  if (spec === null) return null
  const metric = worstMetric(spec, view.evaluation)
  if (metric === 'tokens') return `${formatBudgetTokens(view.spent.tokens)} / ${formatBudgetTokens(spec.tokens ?? 0)}`
  if (metric === 'money') return `${formatBudgetMoney(spendMoneyUsd(view.spent))} / ${formatBudgetMoney(spec.moneyUsd ?? 0)}`
  return null
}
