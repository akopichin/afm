import type { BudgetOverride, BudgetPolicy, BudgetSpec, BudgetView } from '../../types/budget'

// Черновик окна бюджета: что пользователь ввёл, и его проверка. Лимиты не пересчитываются — только разбор ввода.

export type DraftMode = 'declared' | 'custom' | 'unlimited'

export type BudgetDraft = {
  mode: DraftMode
  tokens: string
  usd: string
  policy: BudgetPolicy
}

export type DraftErrors = { tokens?: string; usd?: string; form?: string }

const MAX_TOKENS = Number.MAX_SAFE_INTEGER
const MAX_USD = 1e9

const TOKENS_HINT = 'Use a number like 500k, 1.5m or 120000'
const USD_HINT = 'Use a USD amount like 10 or 12.50'

// parseTokensInput — «500k», «1.5m» или целое число; пустая строка — метрика без лимита.
export function parseTokensInput(text: string): { value: number | undefined; error?: string } {
  const t = text.trim().toLowerCase()
  if (t === '') return { value: undefined }
  const plain = /^\d+$/.exec(t)
  const suffixed = /^(\d+)(?:\.(\d{1,6}))?([km])$/.exec(t)
  let value: number
  if (plain !== null) {
    value = Number(t)
  } else if (suffixed !== null) {
    const [, whole, frac = '', unit] = suffixed
    const digits = unit === 'k' ? 3 : 6
    if (frac.length > digits) return { value: undefined, error: unit === 'k' ? 'At most 3 digits after the point for k' : TOKENS_HINT }
    value = Number(whole) * 10 ** digits + Number(frac.padEnd(digits, '0'))
  } else {
    return { value: undefined, error: TOKENS_HINT }
  }
  if (!Number.isSafeInteger(value) || value <= 0 || value > MAX_TOKENS) return { value: undefined, error: 'Tokens must be a positive whole number' }
  return { value }
}

// parseUsdInput — «10», «$10», «12.50»; пустая строка — метрика без лимита.
export function parseUsdInput(text: string): { value: number | undefined; error?: string } {
  const t = text.trim().replace(/^\$/, '').replace(/\$$/, '')
  if (text.trim() === '') return { value: undefined }
  if (!/^\d+(\.\d+)?$/.test(t)) return { value: undefined, error: USD_HINT }
  const value = Number(t)
  if (!(value > 0) || value > MAX_USD) return { value: undefined, error: 'USD must be positive and at most 1,000,000,000' }
  return { value }
}

export function tokensToInput(tokens: number | undefined): string {
  if (tokens === undefined) return ''
  if (tokens % 1_000_000 === 0) return `${tokens / 1_000_000}m`
  if (tokens % 1000 === 0) return `${tokens / 1000}k`
  return String(tokens)
}

function specToDraft(spec: BudgetSpec | null | undefined): Pick<BudgetDraft, 'tokens' | 'usd' | 'policy'> {
  return { tokens: tokensToInput(spec?.tokens), usd: spec?.moneyUsd === undefined ? '' : String(spec.moneyUsd), policy: spec?.stopPolicy ?? 'hard' }
}

// draftFromView — черновик, равный текущему override области; поля custom засеяны действующим лимитом.
export function draftFromView(view: BudgetView): BudgetDraft {
  const seed = specToDraft(view.override.spec ?? view.effective ?? view.base)
  switch (view.override.kind) {
    case 'limit':
      return { mode: 'custom', ...seed }
    case 'unlimited':
      return { mode: 'unlimited', ...seed }
    default:
      return { mode: 'declared', ...seed }
  }
}

export function resolveDraft(draft: BudgetDraft): { override: BudgetOverride | null; errors: DraftErrors } {
  if (draft.mode === 'declared') return { override: { kind: 'none' }, errors: {} }
  if (draft.mode === 'unlimited') return { override: { kind: 'unlimited' }, errors: {} }
  const tokens = parseTokensInput(draft.tokens)
  const usd = parseUsdInput(draft.usd)
  const errors: DraftErrors = {}
  if (tokens.error !== undefined) errors.tokens = tokens.error
  if (usd.error !== undefined) errors.usd = usd.error
  if (Object.keys(errors).length === 0 && tokens.value === undefined && usd.value === undefined) errors.form = 'Set Tokens or USD (at least one)'
  if (Object.keys(errors).length > 0) return { override: null, errors }
  const spec: BudgetSpec = {
    ...(tokens.value !== undefined ? { tokens: tokens.value } : {}),
    ...(usd.value !== undefined ? { moneyUsd: usd.value } : {}),
    stopPolicy: draft.policy,
  }
  return { override: { kind: 'limit', spec }, errors: {} }
}

export function sameSpec(a: BudgetSpec | null | undefined, b: BudgetSpec | null | undefined): boolean {
  if (a === null || a === undefined || b === null || b === undefined) return (a ?? null) === (b ?? null)
  return a.tokens === b.tokens && a.moneyUsd === b.moneyUsd && a.stopPolicy === b.stopPolicy
}

export function sameOverride(a: BudgetOverride, b: BudgetOverride): boolean {
  return a.kind === b.kind && (a.kind !== 'limit' || sameSpec(a.spec, b.spec))
}

// isDraftClean — черновик не меняет сохранённый override; невалидный ввод чистым не считается.
export function isDraftClean(draft: BudgetDraft, view: BudgetView): boolean {
  const { override } = resolveDraft(draft)
  return override !== null && sameOverride(override, view.override)
}
