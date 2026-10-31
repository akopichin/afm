import { vi } from 'vitest'
import type { UsageRequest } from '../../api/usage-analytics-client'
import type { UsageAnalyticsController } from '../../hooks/use-usage-analytics'
import { initialQuery, resolveQuery } from '../../hooks/use-usage-analytics/usage-analytics-state'
import { parseUsageAnalytics, type UsageAnalytics } from '../../types/usage-analytics'

// Golden JSON of the Go contract: the views are tested against the exact bytes the server tests pin.
const fixtureFiles = import.meta.glob('../../../../../server/testdata/usage-analytics/*.json', {
  eager: true,
  query: '?raw',
  import: 'default',
}) as Record<string, string>

export function fixtureJSON(name: string): unknown {
  const path = Object.keys(fixtureFiles).find((p) => p.endsWith(`/${name}.json`))
  if (path === undefined) throw new Error(`missing fixture ${name}`)
  return JSON.parse(fixtureFiles[path] as string)
}

export function fixture(name: string): UsageAnalytics {
  const data = parseUsageAnalytics(fixtureJSON(name))
  if (data === null) throw new Error(`fixture ${name} did not parse`)
  return data
}

// answerFor is the server's answer to request, taken from the named golden fixture with the echoed query rewritten.
export function answerFor(request: UsageRequest, name: string): UsageAnalytics {
  const query = {
    view: request.view, group: request.group, scope: request.scope, stage_id: request.stageId || undefined,
    agent: request.agent || undefined, model: request.model || undefined,
    sort: request.sort ?? 'time', dir: request.dir ?? 'desc', limit: request.limit ?? undefined,
  }
  const data = parseUsageAnalytics({ ...(fixtureJSON(name) as object), query })
  if (data === null) throw new Error(`fixture ${name} did not parse`)
  return data
}

// controllerFor builds the controller the views get: the query is what the fixture answered, every action is a spy.
export function controllerFor(data: UsageAnalytics | null, overrides: Partial<UsageAnalyticsController> = {}): UsageAnalyticsController {
  const answered = data?.query
  const mode = data?.mode ?? 'full'
  const query = resolveQuery(
    {
      ...initialQuery,
      view: answered?.view ?? 'breakdown',
      group: answered?.group ?? 'model',
      scope: answered?.scope ?? 'all',
      stageId: answered?.stageId ?? '',
      agent: answered?.agent ?? '',
      model: answered?.model ?? '',
      sort: answered === undefined ? null : { key: answered.sort, dir: answered.dir },
    },
    mode,
  )
  const jobs = data?.jobs ?? null
  return {
    mode,
    metrics: data?.metrics ?? [],
    query,
    page: 0,
    pageCount: jobs !== null && jobs.limit > 0 ? Math.max(1, Math.ceil(jobs.total / jobs.limit)) : 0,
    data,
    lastData: data,
    state: data === null ? 'loading' : 'ready',
    error: '',
    setView: vi.fn(),
    setGroup: vi.fn(),
    setScope: vi.fn(),
    setAgent: vi.fn(),
    setModel: vi.fn(),
    setSort: vi.fn(),
    sortBy: vi.fn(),
    setMetric: vi.fn(),
    clearFilters: vi.fn(),
    drill: vi.fn(),
    focusJob: vi.fn(),
    nextPage: vi.fn(),
    prevPage: vi.fn(),
    retry: vi.fn(),
    ...overrides,
  }
}
