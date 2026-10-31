import { afterEach, describe, expect, test, vi } from 'vitest'
import fullJobs from '../../../../server/testdata/usage-analytics/full-jobs.json'
import staleCursor from '../../../../server/testdata/usage-analytics/error-stale-cursor.json'
import { getUsageAnalytics, UsageAnalyticsRequestError, usageAnalyticsUrl, type UsageRequest } from './usage-analytics-client'

const baseRequest: UsageRequest = {
  view: 'jobs', group: 'model', scope: 'all', stageId: '', agent: '', model: '', sort: 'started', dir: 'desc', limit: 10, cursor: '', focusJob: '',
}

function respond(status: number, body: unknown): void {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: status >= 200 && status < 300, status, json: async () => body } as Response)
}

describe('usageAnalyticsUrl', () => {
  test('sends only the parameters that are set', () => {
    expect(usageAnalyticsUrl(baseRequest)).toBe('/api/usage/analytics?view=jobs&group=model&scope=all&sort=started&dir=desc&limit=10')
  })

  test('breakdown without sort and limit', () => {
    expect(usageAnalyticsUrl({ ...baseRequest, view: 'breakdown', sort: null, dir: null, limit: null }))
      .toBe('/api/usage/analytics?view=breakdown&group=model&scope=all')
  })

  test('encodes filter values so an id can never add a parameter or a path segment', () => {
    const url = usageAnalyticsUrl({ ...baseRequest, scope: 'stage', stageId: 'a&b=c/d', agent: 'helper:t 1', model: 'anthropic/x#y', cursor: 'c.4&x', focusJob: '' })
    const params = new URL(url, 'http://afm.test').searchParams
    expect(params.get('stage_id')).toBe('a&b=c/d')
    expect(params.get('agent')).toBe('helper:t 1')
    expect(params.get('model')).toBe('anthropic/x#y')
    expect(params.get('cursor')).toBe('c.4&x')
    expect([...params.keys()].filter((k) => k === 'b')).toEqual([])
  })

  test('focus_job is sent instead of a cursor', () => {
    const params = new URL(usageAnalyticsUrl({ ...baseRequest, focusJob: 'job-4' }), 'http://afm.test').searchParams
    expect(params.get('focus_job')).toBe('job-4')
    expect(params.has('cursor')).toBe(false)
  })
})

describe('getUsageAnalytics', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('parses a successful response and forwards the abort signal', async () => {
    respond(200, fullJobs)
    const controller = new AbortController()
    const data = await getUsageAnalytics(baseRequest, controller.signal)
    expect(data.view).toBe('jobs')
    expect(data.totals.jobs).toBe(6)
    expect(fetch).toHaveBeenCalledWith(expect.stringContaining('/api/usage/analytics?'), { signal: controller.signal })
  })

  test('409 stale_cursor is a typed error', async () => {
    respond(409, staleCursor)
    const error = await getUsageAnalytics({ ...baseRequest, cursor: 'old' }).catch((e: unknown) => e)
    expect(error).toBeInstanceOf(UsageAnalyticsRequestError)
    expect(error).toMatchObject({ status: 409, code: 'stale_cursor' })
  })

  test('400 invalid_param keeps the server message', async () => {
    respond(400, { code: 'invalid_param', message: 'sort cost needs money' })
    await expect(getUsageAnalytics(baseRequest)).rejects.toMatchObject({ status: 400, code: 'invalid_param', message: 'sort cost needs money' })
  })

  test('an error without a known body has no code', async () => {
    respond(500, 'boom')
    await expect(getUsageAnalytics(baseRequest)).rejects.toMatchObject({ status: 500, code: null })
  })

  test('a 200 that is not an analytics response is an error, not an empty result', async () => {
    respond(200, { hello: 'world' })
    await expect(getUsageAnalytics(baseRequest)).rejects.toBeInstanceOf(UsageAnalyticsRequestError)
  })

  test('a network failure and an abort propagate unchanged', async () => {
    const abort = new DOMException('aborted', 'AbortError')
    vi.spyOn(globalThis, 'fetch').mockRejectedValue(abort)
    await expect(getUsageAnalytics(baseRequest)).rejects.toBe(abort)
  })
})
