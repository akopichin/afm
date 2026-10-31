import { useId, type ReactElement } from 'react'
import type { UsageAnalyticsController } from '../../hooks/use-usage-analytics'
import { defaultSortDir } from '../../hooks/use-usage-analytics/usage-analytics-state'
import { allowedSorts, modeAllowsSort, type UsageAnalytics, type UsageDir, type UsageGroup, type UsageSort } from '../../types/usage-analytics'
import { METRIC_NAMES } from './UsageCharts'

type UsageToolbarProps = {
  analytics: UsageAnalyticsController
  // Ответ с каталогом фильтров; во время загрузки нового ответа это последний полученный.
  data: UsageAnalytics
}

// Порядок пунктов меню сортировки — как в прототипе: деньги, токены, время, затем ключи вида.
const SORT_ORDER: UsageSort[] = ['cost', 'tokens', 'time', 'jobs', 'model', 'started', 'name']

function sortLabel(key: UsageSort, dir: UsageDir, view: string, group: UsageGroup): string {
  const asc = dir === 'asc'
  switch (key) {
    case 'cost':
      return asc ? 'Cost: low to high' : 'Cost: high to low'
    case 'tokens':
      return asc ? 'Tokens: low to high' : 'Tokens: high to low'
    case 'time':
      return asc ? 'Time: short to long' : 'Time: long to short'
    case 'jobs':
      return asc ? 'Jobs: fewest first' : 'Jobs: most first'
    case 'model':
      return asc ? 'Model: A–Z' : 'Model: Z–A'
    case 'started':
      return asc ? 'Start: earliest first' : 'Start: latest first'
    case 'name': {
      const noun = view === 'jobs' ? 'Job' : group === 'model' ? 'Model' : 'Agent'
      return `${noun}: ${asc ? 'A–Z' : 'Z–A'}`
    }
  }
}

function Select({ label, value, options, onChange }: { label: string; value: string; options: [string, string][]; onChange: (value: string) => void }): ReactElement {
  const id = useId()
  return (
    <div className="usage-filter">
      <label htmlFor={id}>{label}</label>
      <select id={id} value={value} onChange={(event) => onChange(event.target.value)}>
        {options.map(([optionValue, text]) => <option key={optionValue} value={optionValue}>{text}</option>)}
      </select>
    </div>
  )
}

export function UsageToolbar({ analytics, data }: UsageToolbarProps): ReactElement {
  const { query } = analytics
  const compareId = useId()
  const jobsView = query.view === 'jobs'
  const chartsView = query.view === 'charts'
  const { catalog } = data

  const stageOptions: [string, string][] = [['all', 'All stages'], ...catalog.stages.map((stage): [string, string] => [`stage:${stage.id}`, stage.title || stage.id])]
  if (catalog.scopes.runWide) stageOptions.push(['run_wide', 'Run-wide'])
  if (catalog.scopes.unattributed) stageOptions.push(['unattributed', 'Unattributed'])
  const stageValue = query.scope === 'stage' ? `stage:${query.stageId}` : query.scope

  const sortKeys = SORT_ORDER.filter((key) => allowedSorts(query.view).includes(key) && modeAllowsSort(analytics.mode, key))
  // Естественное направление ключа идёт первым: у имён A–Z, у чисел «больше — раньше».
  const sortOptions = sortKeys.flatMap((key): [string, string][] => {
    const natural = defaultSortDir(key)
    return [natural, natural === 'asc' ? 'desc' : 'asc'].map((dir) => [`${key}:${dir}`, sortLabel(key, dir as UsageDir, query.view, query.group)])
  })

  const filtered = query.scope !== 'all' || (jobsView && (query.agent !== '' || query.model !== ''))
  const showMetrics = chartsView && analytics.metrics.length > 1

  function changeStage(value: string): void {
    if (value.startsWith('stage:')) analytics.setScope('stage', value.slice('stage:'.length))
    else analytics.setScope(value === 'run_wide' || value === 'unattributed' ? value : 'all')
  }

  return (
    <div className={`usage-toolbar${chartsView ? ' usage-toolbar-charts' : ''}`}>
      {!jobsView && (
        <div className="usage-segmented" role="group" aria-label="Group usage">
          <button type="button" aria-pressed={query.group === 'model'} onClick={() => analytics.setGroup('model')}>By model</button>
          <button type="button" aria-pressed={query.group === 'agent'} onClick={() => analytics.setGroup('agent')}>By agent</button>
        </div>
      )}
      {showMetrics && (
        <div className="usage-filter usage-compare">
          <span id={compareId}>Compare</span>
          <div className="usage-metric-switch" role="group" aria-labelledby={compareId}>
            {analytics.metrics.map((metric) => (
              <button key={metric} type="button" aria-pressed={query.metric === metric} onClick={() => analytics.setMetric(metric)}>{METRIC_NAMES[metric]}</button>
            ))}
          </div>
        </div>
      )}
      <div className="usage-filters">
        {jobsView && (
          <>
            <Select label="Agent" value={query.agent} options={[['', 'All agents'], ...catalog.agents.map((agent): [string, string] => [agent.id, agent.title])]} onChange={analytics.setAgent} />
            <Select label="Model" value={query.model} options={[['', 'All models'], ...catalog.models.map((model): [string, string] => [model.key, model.model])]} onChange={analytics.setModel} />
          </>
        )}
        <Select label="Stage" value={stageValue} options={stageOptions} onChange={changeStage} />
        {!chartsView && (
          <Select
            label="Sort"
            value={query.sort === null ? '' : `${query.sort.key}:${query.sort.dir}`}
            options={sortOptions}
            onChange={(value) => {
              const [key, dir] = value.split(':') as [UsageSort, UsageDir]
              analytics.setSort(key, dir)
            }}
          />
        )}
      </div>
      {filtered && <button type="button" className="usage-clear" onClick={analytics.clearFilters}>Clear filters</button>}
    </div>
  )
}
