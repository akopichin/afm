import { describe, expect, test } from 'vitest'
import {
  allowedSorts,
  metricsForMode,
  modeAllowsSort,
  parseUsageAnalytics,
  parseUsageAnalyticsError,
  parseUsageAnalyticsStatus,
  USAGE_MODES,
  type UsageAnalytics,
} from './usage-analytics'

// Golden JSON of the Go contract (pkg/server/testdata/usage-analytics): the same bytes the server tests pin.
const fixtureFiles = import.meta.glob('../../../../server/testdata/usage-analytics/*.json', {
  eager: true,
  query: '?raw',
  import: 'default',
}) as Record<string, string>

function fixture(name: string): unknown {
  const path = Object.keys(fixtureFiles).find((p) => p.endsWith(`/${name}.json`))
  if (path === undefined) throw new Error(`missing fixture ${name}`)
  return JSON.parse(fixtureFiles[path] as string)
}

function parsed(name: string): UsageAnalytics {
  const result = parseUsageAnalytics(fixture(name))
  if (result === null) throw new Error(`fixture ${name} did not parse`)
  return result
}

const viewFixtures = [
  'full-breakdown', 'full-jobs', 'full-charts',
  'no-money-breakdown', 'no-money-jobs', 'no-money-charts',
  'time-only-breakdown', 'time-only-jobs', 'time-only-charts',
  'legacy-breakdown', 'partial-jobs', 'focus-filtered-out',
]

// keysAt lists every object key at any depth.
function allKeys(value: unknown, into: Set<string> = new Set()): Set<string> {
  if (Array.isArray(value)) value.forEach((v) => allKeys(v, into))
  else if (value !== null && typeof value === 'object') {
    for (const [k, v] of Object.entries(value)) {
      into.add(k)
      allKeys(v, into)
    }
  }
  return into
}

describe('usage analytics contract fixtures', () => {
  test('the fixture directory is found', () => {
    expect(Object.keys(fixtureFiles).length).toBeGreaterThanOrEqual(15)
  })

  test.each(viewFixtures)('%s parses and agrees with its own view, mode and metrics', (name) => {
    const data = parsed(name)
    const raw = fixture(name) as { metrics: string[]; view: string; mode: string }
    expect(data.view).toBe(raw.view)
    expect(data.mode).toBe(raw.mode)
    expect(data.metrics).toEqual(raw.metrics)
    expect(metricsForMode(data.mode)).toEqual(raw.metrics)
    expect(data.query.view).toBe(data.view)
    const body = { breakdown: data.breakdown, jobs: data.jobs, charts: data.charts }[data.view]
    expect(body).toBeDefined()
  })

  test.each(['no-money-breakdown', 'no-money-jobs', 'no-money-charts'])('%s has no money anywhere', (name) => {
    const keys = allKeys(parsed(name))
    expect(keys.has('money')).toBe(false)
    expect(keys.has('price')).toBe(false)
    expect(keys.has('estimatedCostUsd')).toBe(false)
    expect(keys.has('tokens')).toBe(true)
  })

  test.each(['time-only-breakdown', 'time-only-jobs', 'time-only-charts'])('%s has neither money nor tokens anywhere', (name) => {
    const keys = allKeys(parsed(name))
    for (const key of ['money', 'price', 'estimatedCostUsd', 'tokens', 'counts', 'totalTokens']) {
      expect(keys.has(key), key).toBe(false)
    }
  })

  test('money that a server sent despite the mode is dropped, never surfaced', () => {
    const raw = fixture('full-jobs') as { mode: string }
    const asNoMoney = parseUsageAnalytics({ ...raw, mode: 'no_money' })
    const asTimeOnly = parseUsageAnalytics({ ...raw, mode: 'time_only' })
    expect(asNoMoney).not.toBeNull()
    expect(allKeys(asNoMoney).has('money')).toBe(false)
    expect(allKeys(asNoMoney).has('tokens')).toBe(true)
    expect(allKeys(asTimeOnly).has('tokens')).toBe(false)
    expect(allKeys(asTimeOnly).has('money')).toBe(false)
  })

  test('null stays unknown and 0 stays a measured zero', () => {
    const jobs = parsed('partial-jobs').jobs
    const interrupted = jobs?.items.find((j) => j.status === 'interrupted')
    expect(interrupted?.durationMs).toBeNull()
    expect(interrupted?.runOffsetMs).toBeNull()
    expect(interrupted?.tokens?.counts).toBeNull()
    expect(interrupted?.money?.estimatedCostUsd).toBeNull()
    const first = jobs?.items.find((j) => j.id === 'job-1')
    expect(first?.runOffsetMs).toBe(0)
    expect(first?.tokens?.counts?.cacheWrite1h).toBe(0)
  })

  test('full jobs page: totals, outside_run_total, cursor and agent flags', () => {
    const data = parsed('full-jobs')
    expect(data.totals.jobs).toBe(6)
    expect(data.totals.agentTime.durationMs).toBe(810000)
    expect(data.totals.money?.estimatedCostUsd).toBe(1.92)
    expect(data.outsideRunTotal?.jobs).toBe(1)
    expect(data.catalog.agents.find((a) => a.id === 'side:run-1')?.outsideRunTotal).toBe(true)
    expect(data.catalog.agents.find((a) => a.id === 'stage:build')?.outsideRunTotal).toBe(false)
    expect(data.jobs?.nextCursor).toBe('c.4.u7j12m3')
    expect(data.jobs?.prevCursor).toBeNull()
    expect(data.query.limit).toBe(4)
  })

  test('legacy journal: unknown time with a reason, absent jobs store', () => {
    const data = parsed('legacy-breakdown')
    expect(data.health.jobs).toBe('absent')
    expect(data.completeness.time).toMatchObject({ status: 'unknown', reasons: ['legacy_journal'] })
    expect(data.catalog.scopes).toEqual({ runWide: false, unattributed: true })
  })

  test('focus outside the filter is reported, not an error', () => {
    const focus = parsed('focus-filtered-out').jobs?.focus
    expect(focus).toEqual({ status: 'filtered_out', jobId: 'job-4', index: null })
  })

  test('charts: comparison rows, bounded timeline and unplaced count', () => {
    const charts = parsed('full-charts').charts
    expect(charts?.comparison.length).toBeGreaterThan(0)
    expect(charts?.timeline.unplaced).toBe(2)
    expect(charts?.timeline.truncated).toBe(false)
    expect(charts?.timeline.intervals[0]).toMatchObject({ jobId: 'job-1', agentId: 'stage:build', runOffsetMs: 0 })
  })

  test('model rows carry the color ordinal; the Unknown bucket has none', () => {
    const rows = parsed('full-breakdown').breakdown?.rows ?? []
    expect(rows[0]?.model?.colorOrdinal).toBe(0)
    expect(rows.find((r) => r.modelState === 'unknown')?.model).toBeNull()
  })

  test('answers that are not an analytics response do not parse', () => {
    expect(parseUsageAnalytics(null)).toBeNull()
    expect(parseUsageAnalytics({})).toBeNull()
    expect(parseUsageAnalytics({ ...(fixture('full-jobs') as object), view: 'timeline' })).toBeNull()
    expect(parseUsageAnalytics({ ...(fixture('full-jobs') as object), mode: 'free' })).toBeNull()
    expect(parseUsageAnalytics({ ...(fixture('full-jobs') as object), jobs: undefined })).toBeNull()
  })
})

describe('usage analytics status and errors', () => {
  test('status block', () => {
    expect(parseUsageAnalyticsStatus(fixture('status-analytics'))).toEqual({ revision: 'u7.j12.m3', mode: 'no_money', counts: { models: 3, agents: 4 } })
    expect(parseUsageAnalyticsStatus(fixture('status-analytics-off'))).toMatchObject({ revision: 'j12.m3', mode: 'time_only' })
  })

  test('status counts are read as a pair and are null when either is missing or not a count', () => {
    const status = { revision: 'r', mode: 'full' }
    expect(parseUsageAnalyticsStatus({ ...status, models: 2, agents: 4 })?.counts).toEqual({ models: 2, agents: 4 })
    expect(parseUsageAnalyticsStatus({ ...status, models: 0, agents: 0 })?.counts).toEqual({ models: 0, agents: 0 })
    expect(parseUsageAnalyticsStatus(status)?.counts).toBeNull()
    expect(parseUsageAnalyticsStatus({ ...status, models: 2 })?.counts).toBeNull()
    expect(parseUsageAnalyticsStatus({ ...status, models: -1, agents: 4 })?.counts).toBeNull()
    expect(parseUsageAnalyticsStatus({ ...status, models: 2, agents: '4' })?.counts).toBeNull()
  })

  test('golden model rows carry their agents count', () => {
    expect(parsed('full-charts').charts?.comparison.map((row) => row.agents)).toEqual([1, 1, 2])
  })

  test('breakdown row agents is a count, null when the server does not send it', () => {
    const body = fixture('full-charts') as { charts: { comparison: object[] } }
    const rows = [{ ...body.charts.comparison[0], agents: 3 }, { ...body.charts.comparison[1], agents: 'x' }, { ...body.charts.comparison[2], agents: undefined }]
    const parsed = parseUsageAnalytics({ ...body, charts: { ...body.charts, comparison: rows } })
    expect(parsed?.charts?.comparison.map((row) => row.agents)).toEqual([3, null, null])
  })

  test('status block of an old or broken server is no capability', () => {
    expect(parseUsageAnalyticsStatus(undefined)).toBeNull()
    expect(parseUsageAnalyticsStatus({ revision: 'r1' })).toBeNull()
    expect(parseUsageAnalyticsStatus({ revision: 'r1', mode: 'free' })).toBeNull()
    expect(parseUsageAnalyticsStatus({ revision: 7, mode: 'full' })).toBeNull()
  })

  test('error body', () => {
    expect(parseUsageAnalyticsError(fixture('error-stale-cursor'))).toMatchObject({ code: 'stale_cursor' })
    expect(parseUsageAnalyticsError({ code: 'invalid_param', message: 'bad sort' })).toEqual({ code: 'invalid_param', message: 'bad sort' })
    expect(parseUsageAnalyticsError({ code: 'teapot', message: 'x' })).toBeNull()
    expect(parseUsageAnalyticsError('nope')).toBeNull()
  })
})

describe('mode rules', () => {
  test('metrics per mode keep the server order', () => {
    expect(metricsForMode('full')).toEqual(['cost', 'agent_time', 'tokens'])
    expect(metricsForMode('no_money')).toEqual(['agent_time', 'tokens'])
    expect(metricsForMode('time_only')).toEqual(['agent_time'])
    expect(USAGE_MODES).toEqual(['full', 'no_money', 'time_only'])
  })

  test('cost sorting needs money, token sorting needs tokens', () => {
    expect(modeAllowsSort('full', 'cost')).toBe(true)
    expect(modeAllowsSort('no_money', 'cost')).toBe(false)
    expect(modeAllowsSort('no_money', 'tokens')).toBe(true)
    expect(modeAllowsSort('time_only', 'tokens')).toBe(false)
    expect(modeAllowsSort('time_only', 'time')).toBe(true)
  })

  test('sort keys differ between the breakdown and the jobs table', () => {
    expect(allowedSorts('breakdown')).toEqual(['cost', 'tokens', 'time', 'jobs', 'name'])
    expect(allowedSorts('jobs')).toEqual(['cost', 'tokens', 'time', 'model', 'started', 'name'])
    expect(allowedSorts('charts')).toEqual([])
  })
})
