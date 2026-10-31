import type { UsageRequest } from '../../api/usage-analytics-client'
import {
  allowedSorts,
  metricsForMode,
  modeAllowsSort,
  type UsageAnalyticsMode,
  type UsageDir,
  type UsageGroup,
  type UsageMetric,
  type UsageQuery,
  type UsageScope,
  type UsageSort,
  type UsageView,
} from '../../types/usage-analytics'

export const PAGE_SIZE = 10

export type UsageSortState = { key: UsageSort; dir: UsageDir }

// UsageQueryState — всё, что пользователь выбрал в деталях. sort и metric — null, пока выбор не сделан: тогда
// действует умолчание режима. Значения, недоступные в режиме, нормализует resolveQuery, а не хранилище.
export type UsageQueryState = {
  view: UsageView
  group: UsageGroup
  scope: UsageScope
  stageId: string
  agent: string
  model: string
  sort: UsageSortState | null
  metric: UsageMetric | null
  // Страница jobs: курсор сервера и номер страницы (0 — первая). focusJobId — просьба вернуть страницу с этим
  // job, взаимоисключающая с cursor; highlightJobId — выделенная строка, живёт до смены фильтра.
  cursor: string
  page: number
  focusJobId: string
  highlightJobId: string
}

export const initialQuery: UsageQueryState = {
  view: 'breakdown',
  group: 'model',
  scope: 'all',
  stageId: '',
  agent: '',
  model: '',
  sort: null,
  metric: null,
  cursor: '',
  page: 0,
  focusJobId: '',
  highlightJobId: '',
}

export type QueryAction =
  | { type: 'setView'; view: UsageView }
  | { type: 'setGroup'; group: UsageGroup }
  | { type: 'setScope'; scope: UsageScope; stageId: string }
  | { type: 'setAgent'; agent: string }
  | { type: 'setModel'; model: string }
  | { type: 'setSort'; key: UsageSort; dir: UsageDir }
  // sortBy — клик по заголовку: тот же ключ меняет направление, другой берёт естественное. current — уже
  // нормализованная сортировка, редьюсер режима не знает.
  | { type: 'sortBy'; key: UsageSort; current: UsageSortState | null }
  | { type: 'setMetric'; metric: UsageMetric }
  | { type: 'clearFilters' }
  // drill — строка модели/агента или полоса сравнения открывает jobs с этим фильтром.
  | { type: 'drill'; agent?: string; model?: string }
  | { type: 'focusJob'; jobId: string }
  | { type: 'setPage'; cursor: string; page: number }
  | { type: 'cursorExpired' }

// Смена фильтра, сортировки или вида сбрасывает страницу и выделение.
function fromFirstPage(state: UsageQueryState): UsageQueryState {
  return { ...state, cursor: '', page: 0, focusJobId: '', highlightJobId: '' }
}

export function defaultSortDir(key: UsageSort): UsageDir {
  return key === 'name' || key === 'model' || key === 'started' ? 'asc' : 'desc'
}

function defaultSortKey(mode: UsageAnalyticsMode): UsageSort {
  if (mode === 'full') return 'cost'
  return mode === 'no_money' ? 'tokens' : 'time'
}

function defaultMetric(mode: UsageAnalyticsMode): UsageMetric {
  if (mode === 'full') return 'cost'
  return mode === 'no_money' ? 'tokens' : 'agent_time'
}

export function queryReducer(state: UsageQueryState, action: QueryAction): UsageQueryState {
  switch (action.type) {
    case 'setView':
      return fromFirstPage({ ...state, view: action.view })
    case 'setGroup':
      return fromFirstPage({ ...state, group: action.group })
    case 'setScope':
      return fromFirstPage({ ...state, scope: action.scope, stageId: action.scope === 'stage' ? action.stageId : '' })
    case 'setAgent':
      return fromFirstPage({ ...state, agent: action.agent })
    case 'setModel':
      return fromFirstPage({ ...state, model: action.model })
    case 'setSort':
      return fromFirstPage({ ...state, sort: { key: action.key, dir: action.dir } })
    case 'sortBy': {
      const dir = action.current?.key === action.key ? (action.current.dir === 'asc' ? 'desc' : 'asc') : defaultSortDir(action.key)
      return fromFirstPage({ ...state, sort: { key: action.key, dir } })
    }
    case 'setMetric':
      return { ...state, metric: action.metric }
    case 'clearFilters':
      return fromFirstPage({ ...state, scope: 'all', stageId: '', agent: '', model: '' })
    case 'drill':
      return fromFirstPage({ ...state, view: 'jobs', agent: action.agent ?? '', model: action.model ?? '', sort: null })
    case 'focusJob':
      return {
        ...state,
        view: 'jobs',
        agent: '',
        model: '',
        sort: { key: 'started', dir: 'asc' },
        cursor: '',
        page: 0,
        focusJobId: action.jobId,
        highlightJobId: action.jobId,
      }
    case 'setPage':
      return { ...state, cursor: action.cursor, page: action.page, focusJobId: '' }
    case 'cursorExpired':
      return { ...state, cursor: '', page: 0, focusJobId: state.highlightJobId }
  }
}

// ResolvedQuery — состояние после нормализации под режим сервера: именно оно уходит в запрос и на экран.
export type ResolvedQuery = Omit<UsageQueryState, 'sort' | 'metric'> & { sort: UsageSortState | null; metric: UsageMetric }

export function resolveQuery(state: UsageQueryState, mode: UsageAnalyticsMode): ResolvedQuery {
  const allowed = allowedSorts(state.view).filter((key) => modeAllowsSort(mode, key))
  const chosen = state.sort !== null && allowed.includes(state.sort.key) ? state.sort : null
  const fallback: UsageSortState = { key: defaultSortKey(mode), dir: 'desc' }
  const metric = state.metric !== null && metricsForMode(mode).includes(state.metric) ? state.metric : defaultMetric(mode)
  return {
    ...state,
    scope: state.scope === 'stage' && state.stageId === '' ? 'all' : state.scope,
    sort: state.view === 'charts' ? null : (chosen ?? fallback),
    metric,
  }
}

// buildRequest — фильтры agent/model и страницы есть только у jobs; breakdown и charts отвечают на group/scope.
export function buildRequest(query: ResolvedQuery): UsageRequest {
  const jobs = query.view === 'jobs'
  return {
    view: query.view,
    group: query.group,
    scope: query.scope,
    stageId: query.scope === 'stage' ? query.stageId : '',
    agent: jobs ? query.agent : '',
    model: jobs ? query.model : '',
    sort: query.sort?.key ?? null,
    dir: query.sort?.dir ?? null,
    limit: jobs ? PAGE_SIZE : null,
    cursor: jobs ? query.cursor : '',
    focusJob: jobs ? query.focusJobId : '',
  }
}

// matchesRequest — ответ сервера описывает ровно тот запрос, который отправлен; иначе он чужой и отбрасывается.
export function matchesRequest(request: UsageRequest, answered: UsageQuery): boolean {
  return (
    answered.view === request.view &&
    answered.group === request.group &&
    answered.scope === request.scope &&
    answered.stageId === request.stageId &&
    answered.agent === request.agent &&
    answered.model === request.model &&
    (request.sort === null || answered.sort === request.sort) &&
    (request.dir === null || answered.dir === request.dir)
  )
}
