// Типы для отслеживания стоимости и использования токенов в afm.
// Источник — GET /api/status (поля CostView из pkg/server/stageview.go).

export type Coverage = 'full' | 'partial' | 'none'

export type TokenBreakdown = {
  uncachedInput: number
  cacheRead: number
  cacheWrite5m: number
  cacheWrite1h: number
  cacheWriteOther: number
  output: number
  reasoningOutput: number
  totalTokens: number
  cacheWriteTotal: number
  cacheHitRatio: number | null
}

export type CostSummary = {
  displayCost: string
  coverage: Coverage
  estimatedCostUsd: number
  metered: number
  pricedInvocations: number
  unpriced: number
  unmetered: number
  models: string[]
} & TokenBreakdown & {
  phases: Record<string, number>
}

export type Attribution =
  | { kind: 'stage'; stageId: string }
  | { kind: 'run_overhead' }
  | { kind: 'unknown' }

export type CoverageIssue = {
  kind: 'unpriced' | 'unmetered'
  attribution: Attribution
  phase: string
  channel: string
  model: string
  reason: string
  count: number
}

export type AccountingState =
  | { supported: false }
  // showMoney — показывать ли денежные ($) значения. false (дефолт бэкенда)
  // прячет стоимость всюду в дашборде, оставляя токены. Токены показываются
  // всегда, независимо от этого флага.
  | { supported: true; health: 'ok' | 'unavailable'; hasData: boolean; showMoney: boolean }
