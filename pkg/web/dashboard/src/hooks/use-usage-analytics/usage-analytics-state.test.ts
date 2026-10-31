import { describe, expect, test } from 'vitest'
import {
  buildRequest,
  initialQuery,
  matchesRequest,
  PAGE_SIZE,
  queryReducer,
  resolveQuery,
  type QueryAction,
  type UsageQueryState,
} from './usage-analytics-state'
import type { UsageQuery } from '../../types/usage-analytics'

function apply(state: UsageQueryState, ...actions: QueryAction[]): UsageQueryState {
  return actions.reduce(queryReducer, state)
}

describe('defaults and normalization by mode', () => {
  test('full: cost first', () => {
    const q = resolveQuery(initialQuery, 'full')
    expect(q).toMatchObject({ view: 'breakdown', group: 'model', sort: { key: 'cost', dir: 'desc' }, metric: 'cost' })
  })

  test('no_money: tokens high to low', () => {
    expect(resolveQuery(initialQuery, 'no_money')).toMatchObject({ sort: { key: 'tokens', dir: 'desc' }, metric: 'tokens' })
  })

  test('time_only: time', () => {
    expect(resolveQuery(initialQuery, 'time_only')).toMatchObject({ sort: { key: 'time', dir: 'desc' }, metric: 'agent_time' })
  })

  test('a stored cost sort or metric is replaced before any request when money disappears', () => {
    const stored = apply(initialQuery, { type: 'setSort', key: 'cost', dir: 'asc' }, { type: 'setMetric', metric: 'cost' })
    expect(resolveQuery(stored, 'full')).toMatchObject({ sort: { key: 'cost', dir: 'asc' }, metric: 'cost' })
    const noMoney = resolveQuery(stored, 'no_money')
    expect(noMoney.sort).toEqual({ key: 'tokens', dir: 'desc' })
    expect(noMoney.metric).toBe('tokens')
    expect(buildRequest(noMoney).sort).toBe('tokens')
    expect(resolveQuery(stored, 'time_only')).toMatchObject({ sort: { key: 'time', dir: 'desc' }, metric: 'agent_time' })
  })

  test('a sort the view does not have falls back to the default; charts have none', () => {
    const started = apply(initialQuery, { type: 'setView', view: 'jobs' }, { type: 'setSort', key: 'started', dir: 'asc' })
    expect(resolveQuery(started, 'full').sort).toEqual({ key: 'started', dir: 'asc' })
    expect(resolveQuery(apply(started, { type: 'setView', view: 'breakdown' }), 'full').sort).toEqual({ key: 'cost', dir: 'desc' })
    expect(resolveQuery(apply(started, { type: 'setView', view: 'charts' }), 'full').sort).toBeNull()
  })

  test('stage scope without a stage id is all', () => {
    const q = apply(initialQuery, { type: 'setScope', scope: 'stage', stageId: '' })
    expect(resolveQuery(q, 'full').scope).toBe('all')
  })
})

describe('requests', () => {
  test('breakdown sends view, group, scope and sort only', () => {
    const q = resolveQuery(apply(initialQuery, { type: 'setAgent', agent: 'stage:x' }), 'full')
    expect(buildRequest(q)).toEqual({
      view: 'breakdown', group: 'model', scope: 'all', stageId: '', agent: '', model: '',
      sort: 'cost', dir: 'desc', limit: null, cursor: '', focusJob: '',
    })
  })

  test('jobs sends filters, page size, cursor', () => {
    const q = resolveQuery(
      apply(initialQuery, { type: 'setScope', scope: 'stage', stageId: 'build' }, { type: 'drill', agent: 'helper:t-1' }, { type: 'setPage', cursor: 'c.4', page: 1 }),
      'full',
    )
    expect(buildRequest(q)).toMatchObject({
      view: 'jobs', scope: 'stage', stageId: 'build', agent: 'helper:t-1', model: '', limit: PAGE_SIZE, cursor: 'c.4', sort: 'cost',
    })
  })

  test('charts send no sort and no page', () => {
    const q = resolveQuery(apply(initialQuery, { type: 'setView', view: 'charts' }), 'full')
    expect(buildRequest(q)).toMatchObject({ view: 'charts', sort: null, dir: null, limit: null, cursor: '' })
  })

  test('an answer matches only the request it was asked for', () => {
    const q = resolveQuery(initialQuery, 'full')
    const request = buildRequest(q)
    const answer: UsageQuery = { view: 'breakdown', group: 'model', scope: 'all', stageId: '', agent: '', model: '', sort: 'cost', dir: 'desc', limit: null }
    expect(matchesRequest(request, answer)).toBe(true)
    expect(matchesRequest(request, { ...answer, group: 'agent' })).toBe(false)
    expect(matchesRequest(request, { ...answer, sort: 'tokens' })).toBe(false)
    expect(matchesRequest(request, { ...answer, dir: 'asc' })).toBe(false)
    expect(matchesRequest(request, { ...answer, scope: 'run_wide' })).toBe(false)
    expect(matchesRequest(request, { ...answer, view: 'jobs' })).toBe(false)
  })
})

describe('transitions', () => {
  const paged = apply(
    initialQuery,
    { type: 'setView', view: 'jobs' },
    { type: 'setPage', cursor: 'c.10', page: 1 },
  )

  test.each<[string, QueryAction]>([
    ['view', { type: 'setView', view: 'charts' }],
    ['group', { type: 'setGroup', group: 'agent' }],
    ['scope', { type: 'setScope', scope: 'run_wide', stageId: '' }],
    ['agent', { type: 'setAgent', agent: 'a' }],
    ['model', { type: 'setModel', model: 'm' }],
    ['sort', { type: 'setSort', key: 'tokens', dir: 'asc' }],
    ['clear', { type: 'clearFilters' }],
    ['drill', { type: 'drill', model: 'm' }],
  ])('changing %s drops the cursor, the page and the highlight', (_name, action) => {
    const highlighted = { ...paged, highlightJobId: 'job-1' }
    const next = queryReducer(highlighted, action)
    expect(next).toMatchObject({ cursor: '', page: 0, focusJobId: '', highlightJobId: '' })
  })

  test('metric and token-agnostic changes keep the page', () => {
    expect(queryReducer(paged, { type: 'setMetric', metric: 'tokens' })).toMatchObject({ cursor: 'c.10', page: 1 })
  })

  test('drill into an agent shows its jobs with the default sort and no other filter', () => {
    const q = apply(initialQuery, { type: 'setSort', key: 'name', dir: 'asc' }, { type: 'setModel', model: 'm' }, { type: 'drill', agent: 'stage:a' })
    expect(q).toMatchObject({ view: 'jobs', agent: 'stage:a', model: '', sort: null })
  })

  test('drill into a model clears the agent filter', () => {
    const q = apply(initialQuery, { type: 'setAgent', agent: 'a' }, { type: 'drill', model: 'anthropic/x' })
    expect(q).toMatchObject({ view: 'jobs', agent: '', model: 'anthropic/x' })
  })

  test('focusJob opens the jobs in start order around that job and highlights it', () => {
    const q = apply(initialQuery, { type: 'setAgent', agent: 'a' }, { type: 'focusJob', jobId: 'job-9' })
    expect(q).toMatchObject({ view: 'jobs', agent: '', model: '', sort: { key: 'started', dir: 'asc' }, focusJobId: 'job-9', highlightJobId: 'job-9', cursor: '' })
  })

  test('paging away from a focused job keeps the highlight but drops the focus request', () => {
    const focused = apply(initialQuery, { type: 'focusJob', jobId: 'job-9' })
    expect(queryReducer(focused, { type: 'setPage', cursor: 'c.20', page: 2 })).toMatchObject({ focusJobId: '', highlightJobId: 'job-9', cursor: 'c.20', page: 2 })
  })

  test('an expired cursor goes back to the first page, or to the highlighted job', () => {
    expect(queryReducer(paged, { type: 'cursorExpired' })).toMatchObject({ cursor: '', page: 0, focusJobId: '' })
    expect(queryReducer({ ...paged, highlightJobId: 'job-1' }, { type: 'cursorExpired' })).toMatchObject({ cursor: '', page: 0, focusJobId: 'job-1' })
  })

  test('toggling the same sort flips direction, another key starts from its natural direction', () => {
    const view = apply(initialQuery, { type: 'setView', view: 'jobs' })
    const once = queryReducer(view, { type: 'sortBy', key: 'name', current: null })
    expect(once.sort).toEqual({ key: 'name', dir: 'asc' })
    expect(queryReducer(once, { type: 'sortBy', key: 'name', current: once.sort }).sort).toEqual({ key: 'name', dir: 'desc' })
    expect(queryReducer(once, { type: 'sortBy', key: 'tokens', current: once.sort }).sort).toEqual({ key: 'tokens', dir: 'desc' })
    expect(queryReducer(once, { type: 'sortBy', key: 'started', current: once.sort }).sort).toEqual({ key: 'started', dir: 'asc' })
  })
})
