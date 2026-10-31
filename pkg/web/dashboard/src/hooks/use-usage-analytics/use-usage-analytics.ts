import { useCallback, useEffect, useState } from 'react'
import { getUsageAnalytics, UsageAnalyticsRequestError } from '../../api/usage-analytics-client'
import {
  metricsForMode,
  type UsageAnalytics,
  type UsageAnalyticsMode,
  type UsageAnalyticsStatus,
  type UsageDir,
  type UsageGroup,
  type UsageMetric,
  type UsageScope,
  type UsageSort,
  type UsageView,
} from '../../types/usage-analytics'
import {
  buildRequest,
  initialQuery,
  matchesRequest,
  queryReducer,
  resolveQuery,
  type QueryAction,
  type ResolvedQuery,
  type UsageQueryState,
} from './usage-analytics-state'

// FetchState: idle — запросов нет; loading — данных для текущего запроса ещё нет; ready — data отвечает ему;
// stale — обновление не удалось или сервер потерян, data прежняя; error — не удалось и данных нет.
export type FetchState = 'idle' | 'loading' | 'ready' | 'stale' | 'error'

export type UsageAnalyticsController = {
  mode: UsageAnalyticsMode
  metrics: UsageMetric[]
  // Выбор пользователя после нормализации под режим сервера.
  query: ResolvedQuery
  // Страница jobs (с 0) показанного ответа и число страниц; 0 страниц, пока jobs ни разу не загружены.
  // Пока грузится соседняя страница, это номер видимых строк, а не запрошенной.
  page: number
  pageCount: number
  data: UsageAnalytics | null
  // Последний ответ этого рана на любой запрос: фильтры и итоги не мигают, пока грузится новый. null после смены рана.
  lastData: UsageAnalytics | null
  state: FetchState
  error: string
  setView: (view: UsageView) => void
  setGroup: (group: UsageGroup) => void
  setScope: (scope: UsageScope, stageId?: string) => void
  setAgent: (agent: string) => void
  setModel: (model: string) => void
  setSort: (key: UsageSort, dir: UsageDir) => void
  sortBy: (key: UsageSort) => void
  setMetric: (metric: UsageMetric) => void
  clearFilters: () => void
  drill: (target: { agent?: string; model?: string }) => void
  focusJob: (jobId: string) => void
  nextPage: () => void
  prevPage: () => void
  retry: () => void
}

type Loaded = { key: string; data: UsageAnalytics; page: number }
type Failure = { key: string; message: string }

// Held — всё, что принадлежит одному рану: выбор, последний ответ, последний сбой.
type Held = { epoch: number; query: UsageQueryState; loaded: Loaded | null; failure: Failure | null; reloads: number }

function freshHeld(epoch: number): Held {
  return { epoch, query: initialQuery, loaded: null, failure: null, reloads: 0 }
}

// pageOf — страница ответа: у ответа с выделенным job она следует из индекса job, иначе это запрошенная страница.
function pageOf(requestedPage: number, jobs: UsageAnalytics['jobs']): number {
  const focusIndex = jobs?.focus?.status === 'found' ? jobs.focus.index : null
  return focusIndex !== null && jobs !== null && jobs.limit > 0 ? Math.floor(focusIndex / jobs.limit) : requestedPage
}

type Props = {
  // open — показаны детали «Models & agents»; status — блок analytics из /api/status (null — capability нет).
  open: boolean
  status: Pick<UsageAnalyticsStatus, 'revision' | 'mode'> | null
  runEpoch: number
  // statusAvailable — свежесть /api/status. Без сервера ревизия не меняется и запрос не падает, поэтому
  // устаревание прежнего ответа видно только по ней.
  statusAvailable?: boolean
}

// useUsageAnalytics владеет состоянием «Models & agents» выше условного рендера: выбор переживает
// закрытие и переключение вкладок и сбрасывается при смене рана. Запросов нет, пока детали закрыты или
// capability нет; обновление — по ревизии из /api/status, а не по собственному таймеру.
export function useUsageAnalytics({ open, status, runEpoch, statusAvailable = true }: Props): UsageAnalyticsController {
  const [stored, setStored] = useState<Held>(() => freshHeld(runEpoch))
  // Смена рана: прежние данные не показываются ни в одном кадре.
  if (stored.epoch !== runEpoch) setStored(freshHeld(runEpoch))
  const held = stored.epoch === runEpoch ? stored : freshHeld(runEpoch)

  const active = open && status !== null
  const mode: UsageAnalyticsMode = status?.mode ?? 'time_only'
  const revision = status?.revision ?? ''
  const query = resolveQuery(held.query, mode)
  const request = buildRequest(query)
  // Режим входит в ключ: смена режима при той же ревизии и том же запросе тоже требует нового ответа.
  const key = JSON.stringify({ mode, request })

  useEffect(() => {
    if (!active) return
    const controller = new AbortController()
    const failWith = (message: string): void =>
      setStored((prev) => (prev.epoch === runEpoch ? { ...prev, failure: { key, message } } : prev))

    getUsageAnalytics(request, controller.signal).then(
      (data) => {
        if (controller.signal.aborted) return
        if (!matchesRequest(request, data.query)) {
          failWith('Usage analytics answered a different request')
          return
        }
        const loaded = { key, data, page: pageOf(query.page, data.jobs) }
        setStored((prev) => (prev.epoch === runEpoch ? { ...prev, loaded, failure: null } : prev))
      },
      (error: unknown) => {
        if (controller.signal.aborted) return
        // Курсор привязан к ревизии: устарел — возвращаемся на первую страницу или к выделенному job.
        if (error instanceof UsageAnalyticsRequestError && error.code === 'stale_cursor' && request.cursor !== '') {
          setStored((prev) => (prev.epoch === runEpoch ? { ...prev, query: queryReducer(prev.query, { type: 'cursorExpired' }) } : prev))
          return
        }
        failWith(error instanceof Error ? error.message : 'Usage analytics request failed')
      },
    )
    return () => controller.abort()
    // request выводится из key, остальные зависимости — условия повторного запроса.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, key, revision, held.reloads, runEpoch])

  const dispatch = useCallback(
    (action: QueryAction) =>
      setStored((prev) => (prev.epoch === runEpoch ? { ...prev, query: queryReducer(prev.query, action) } : prev)),
    [runEpoch],
  )

  const data = held.loaded?.key === key ? held.loaded.data : null
  const failure = held.failure?.key === key ? held.failure : null
  let state: FetchState = 'loading'
  if (!active) state = 'idle'
  else if (failure !== null) state = data !== null ? 'stale' : 'error'
  else if (data !== null) state = statusAvailable ? 'ready' : 'stale'

  const jobs = data?.jobs ?? null
  const page = held.loaded?.page ?? query.page
  // Число страниц берётся из последнего ответа: пока грузится соседняя страница, пейджер не падает в «2 / 0».
  const lastJobs = held.loaded?.data.jobs ?? null
  const pageCount = lastJobs !== null && lastJobs.limit > 0 ? Math.max(1, Math.ceil(lastJobs.total / lastJobs.limit)) : 0

  return {
    mode,
    metrics: metricsForMode(mode),
    query,
    page,
    pageCount,
    data,
    lastData: held.loaded?.data ?? null,
    state,
    error: failure?.message ?? '',
    setView: (view) => dispatch({ type: 'setView', view }),
    setGroup: (group) => dispatch({ type: 'setGroup', group }),
    setScope: (scope, stageId = '') => dispatch({ type: 'setScope', scope, stageId }),
    setAgent: (agent) => dispatch({ type: 'setAgent', agent }),
    setModel: (model) => dispatch({ type: 'setModel', model }),
    setSort: (sortKey, dir) => dispatch({ type: 'setSort', key: sortKey, dir }),
    sortBy: (sortKey) => dispatch({ type: 'sortBy', key: sortKey, current: query.sort }),
    setMetric: (metric) => dispatch({ type: 'setMetric', metric }),
    clearFilters: () => dispatch({ type: 'clearFilters' }),
    drill: (target) => dispatch({ type: 'drill', ...target }),
    focusJob: (jobId) => dispatch({ type: 'focusJob', jobId }),
    nextPage: () => {
      if (jobs?.nextCursor) dispatch({ type: 'setPage', cursor: jobs.nextCursor, page: page + 1 })
    },
    prevPage: () => {
      if (jobs?.prevCursor) dispatch({ type: 'setPage', cursor: jobs.prevCursor, page: Math.max(0, page - 1) })
    },
    retry: () => setStored((prev) => (prev.epoch === runEpoch ? { ...prev, failure: null, reloads: prev.reloads + 1 } : prev)),
  }
}
