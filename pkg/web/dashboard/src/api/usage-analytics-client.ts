// Типизированный клиент к GET /api/usage/analytics — деталям «Models & agents». Один запрос = один вид из одной
// ревизии; опрос и устаревшие ответы — забота хука use-usage-analytics.

import {
  parseUsageAnalytics,
  parseUsageAnalyticsError,
  type UsageAnalytics,
  type UsageDir,
  type UsageErrorCode,
  type UsageGroup,
  type UsageScope,
  type UsageSort,
  type UsageView,
} from '../types/usage-analytics'

// Пустая строка / null — параметр не передаётся. focusJob и cursor взаимоисключающи: отправляется focusJob.
export type UsageRequest = {
  view: UsageView
  group: UsageGroup
  scope: UsageScope
  stageId: string
  agent: string
  model: string
  sort: UsageSort | null
  dir: UsageDir | null
  limit: number | null
  cursor: string
  focusJob: string
}

// UsageAnalyticsRequestError: status — HTTP-статус (200 для ответа неверной формы), code — машинный код из тела
// 400/409 либо null.
export class UsageAnalyticsRequestError extends Error {
  constructor(
    public status: number,
    public code: UsageErrorCode | null,
    message: string,
  ) {
    super(message)
    this.name = 'UsageAnalyticsRequestError'
  }
}

// usageAnalyticsUrl кодирует значения фильтров через URLSearchParams: ID — значения параметров, а не пути.
export function usageAnalyticsUrl(request: UsageRequest): string {
  const params = new URLSearchParams({ view: request.view, group: request.group, scope: request.scope })
  const optional: [string, string | number | null][] = [
    ['stage_id', request.stageId],
    ['agent', request.agent],
    ['model', request.model],
    ['sort', request.sort],
    ['dir', request.dir],
    ['limit', request.limit],
    ['focus_job', request.focusJob],
    ['cursor', request.focusJob === '' ? request.cursor : ''],
  ]
  for (const [name, value] of optional) {
    if (value !== null && value !== '') params.set(name, String(value))
  }
  return `/api/usage/analytics?${params}`
}

async function readJson(response: Response): Promise<unknown> {
  try {
    return await response.json()
  } catch {
    return null
  }
}

export async function getUsageAnalytics(request: UsageRequest, signal?: AbortSignal): Promise<UsageAnalytics> {
  const response = await fetch(usageAnalyticsUrl(request), { signal })
  const body = await readJson(response)
  if (!response.ok) {
    const error = parseUsageAnalyticsError(body)
    throw new UsageAnalyticsRequestError(response.status, error?.code ?? null, error?.message ?? `usage analytics request failed: ${response.status}`)
  }
  const data = parseUsageAnalytics(body)
  if (data === null) throw new UsageAnalyticsRequestError(response.status, null, 'usage analytics response has an unexpected shape')
  return data
}
