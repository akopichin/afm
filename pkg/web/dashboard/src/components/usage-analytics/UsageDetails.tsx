import { useEffect, useRef, type ReactElement } from 'react'
import type { UsageAnalyticsController } from '../../hooks/use-usage-analytics'
import type { UsageAnalytics, UsageView } from '../../types/usage-analytics'
import { BreakdownTable } from './BreakdownTable'
import { CountingRules } from './CountingRules'
import { JobsTable } from './JobsTable'
import { UsageCharts } from './UsageCharts'
import { useSinceAsOf } from './usage-clock'
import { UsageSummary } from './UsageSummary'
import { UsageToolbar } from './UsageToolbar'

type UsageDetailsProps = {
  analytics: UsageAnalyticsController
  onBack: () => void
  // live — /api/status свеж: только тогда длительности running-jobs тикают.
  live?: boolean
}

const VIEWS: [UsageView, string][] = [['breakdown', 'Breakdown'], ['jobs', 'Agent jobs'], ['charts', 'Charts']]

function subtitle(hasTokens: boolean, hasMoney: boolean): string {
  if (hasMoney) return 'Time, tokens and estimated cost'
  return hasTokens ? 'Time and tokens' : 'Agent time'
}

// hasNoContent — в выбранном виде нечего показать. Итог jobs не годится: у legacy-рана и у одного Assistant он 0,
// а строки сравнения есть.
function hasNoContent(data: UsageAnalytics, view: UsageView): boolean {
  if (view === 'breakdown') return (data.breakdown?.rows.length ?? 0) === 0
  if (view === 'jobs') return (data.jobs?.items.length ?? 0) === 0
  const charts = data.charts
  if (charts === null) return true
  const { timeline } = charts
  return charts.comparison.length === 0 && timeline.intervals.length === 0 && timeline.unplaced === 0 && (data.outsideRunTotal?.jobs ?? 0) === 0
}

// UsageDetails — подвид Cost «Models & agents»: Breakdown, Agent jobs и Charts над одним состоянием controller.
export function UsageDetails({ analytics, onBack, live = false }: UsageDetailsProps): ReactElement {
  const { data, query } = analytics
  // Панель фильтров и итоги остаются на месте, пока грузится новый ответ: иначе фокус теряется вместе с элементом.
  // Ответ другого рана сюда не попадает: controller сбрасывает его при смене рана.
  const shown = analytics.lastData
  // Таблица того же вида остаётся на экране, пока грузится новый ответ: иначе нажатая кнопка заголовка или
  // пейджера размонтируется и фокус уходит в body. Чужой вид не показываем — он выглядел бы пустым.
  const content = data ?? (analytics.state === 'loading' && shown?.query.view === query.view ? shown : null)
  // Assistant вне итога рана, но его running-job тоже должен тикать.
  const activeJobs = (shown?.totals.activeJobs ?? 0) + (shown?.outsideRunTotal?.activeJobs ?? 0)
  const sinceAsOfMs = useSinceAsOf(shown?.asOf, live && activeJobs > 0)
  const hasTokens = shown !== null && analytics.metrics.includes('tokens') && shown.metrics.includes('tokens')
  const hasMoney = shown !== null && analytics.metrics.includes('cost') && shown.metrics.includes('cost')

  const rootRef = useRef<HTMLElement>(null)
  const viewRef = useRef(query.view)
  // Строка или полоса, открывшая другой вид, исчезает вместе с фокусом: он переходит на кнопку нового вида.
  useEffect(() => {
    if (viewRef.current === query.view) return
    viewRef.current = query.view
    const active = document.activeElement
    if (active === null || active === document.body || !rootRef.current?.contains(active)) {
      rootRef.current?.querySelector<HTMLButtonElement>('[aria-current="page"]')?.focus({ preventScroll: true })
    }
  }, [query.view])

  const filtered = query.scope !== 'all' || query.agent !== '' || query.model !== ''

  return (
    <section ref={rootRef} className="usage-details" aria-labelledby="usage-details-title">
      <header className="usage-details-head">
        <div className="usage-details-titles">
          <h2 id="usage-details-title" className="usage-details-title">Models & agents</h2>
          {shown !== null && <p className="usage-details-subtitle">{subtitle(hasTokens, hasMoney)}</p>}
        </div>
        <button type="button" className="usage-back" aria-label="Back to Cost overview" onClick={onBack}>
          <span className="usage-back-arrow" aria-hidden="true" />
          <span>Cost overview</span>
        </button>
      </header>
      <div className="usage-scroll">
        <div className="usage-reading">
          <nav className="usage-views" aria-label="Usage views">
            {VIEWS.map(([view, label]) => (
              <button key={view} type="button" aria-current={query.view === view ? 'page' : undefined} onClick={() => analytics.setView(view)}>{label}</button>
            ))}
          </nav>
          {analytics.state === 'loading' && <p role="status" className="usage-state">Loading…</p>}
          {analytics.state === 'stale' && <p role="status" className="usage-state">Showing the last data: the latest update failed, so it may be out of date.</p>}
          {analytics.state === 'error' && (
            <div role="alert" className="usage-state usage-state-error">
              <p>{analytics.error}</p>
              <button type="button" onClick={analytics.retry}>Retry</button>
            </div>
          )}
          {shown !== null && (
            <>
              {/* При ошибке сохранённые итоги относятся к прежнему фильтру и выглядели бы итогами нового. */}
              {analytics.state !== 'error' && <UsageSummary data={shown} hasTokens={hasTokens} hasMoney={hasMoney} sinceAsOfMs={sinceAsOfMs} />}
              <UsageToolbar analytics={analytics} data={shown} />
            </>
          )}
          {content !== null && (
            <div className="usage-content" aria-busy={analytics.state === 'loading'}>
              {hasNoContent(content, query.view) ? (
                <div className="usage-empty">
                  <h3>{filtered ? 'No matching jobs' : 'No usage yet'}</h3>
                  <p>{filtered ? 'Choose another agent, model or stage, or clear the filters.' : 'Usage appears after the first agent job of this run.'}</p>
                  {filtered && <button type="button" className="usage-clear" onClick={analytics.clearFilters}>Clear filters</button>}
                </div>
              ) : (
                <>
                  {query.view === 'breakdown' && <BreakdownTable analytics={analytics} data={content} sinceAsOfMs={sinceAsOfMs} />}
                  {query.view === 'jobs' && <JobsTable analytics={analytics} data={content} sinceAsOfMs={sinceAsOfMs} />}
                  {query.view === 'charts' && <UsageCharts analytics={analytics} data={content} sinceAsOfMs={sinceAsOfMs} />}
                </>
              )}
            </div>
          )}
          {shown !== null && <CountingRules hasTokens={hasTokens} hasMoney={hasMoney} />}
        </div>
      </div>
    </section>
  )
}
