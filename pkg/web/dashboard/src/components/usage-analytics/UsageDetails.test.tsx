import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import type { UsageAnalyticsController } from '../../hooks/use-usage-analytics'
import type { UsageAnalytics } from '../../types/usage-analytics'
import { UsageDetails } from './UsageDetails'
import { controllerFor, fixture } from './usage-test-support'

function renderDetails(data: UsageAnalytics | null, overrides: Partial<UsageAnalyticsController> = {}, live = false) {
  const onBack = vi.fn()
  const analytics = controllerFor(data, overrides)
  const view = render(<UsageDetails analytics={analytics} onBack={onBack} live={live} />)
  return { analytics, onBack, ...view }
}

// renderLive renders the details with a fresh status; loseStatus re-renders the same data without it.
function renderLive(data: UsageAnalytics) {
  const analytics = controllerFor(data)
  const view = render(<UsageDetails analytics={analytics} onBack={() => {}} live />)
  return { loseStatus: () => view.rerender(<UsageDetails analytics={analytics} onBack={() => {}} live={false} />) }
}

afterEach(() => {
  vi.useRealTimers()
})

describe('UsageDetails shell', () => {
  test('has a heading and a way back to the Cost overview', () => {
    const { onBack } = renderDetails(null)
    expect(screen.getByRole('heading', { level: 2, name: 'Models & agents' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Back to Cost overview' }))
    expect(onBack).toHaveBeenCalledTimes(1)
  })

  test('loading is announced politely', () => {
    renderDetails(null, { state: 'loading' })
    expect(screen.getByRole('status')).toHaveTextContent(/loading/i)
  })

  test('an error with no data offers Retry', () => {
    const retry = vi.fn()
    renderDetails(null, { state: 'error', error: 'boom', retry })
    expect(screen.getByRole('alert')).toHaveTextContent('boom')
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(retry).toHaveBeenCalledTimes(1)
  })

  test('stale data stays on screen and says it may be out of date', () => {
    renderDetails(fixture('full-breakdown'), { state: 'stale', error: 'boom' })
    expect(screen.getByRole('status')).toHaveTextContent(/out of date/i)
    expect(screen.getByRole('table', { name: 'Usage by agent' })).toBeInTheDocument()
  })

  test('the subtitle names only what the mode shows', () => {
    const subtitleOf = (name: string): string | null => {
      const { container, unmount } = renderDetails(fixture(name))
      const text = container.querySelector('.usage-details-subtitle')?.textContent ?? null
      unmount()
      return text
    }
    expect(subtitleOf('full-breakdown')).toBe('Time, tokens and estimated cost')
    expect(subtitleOf('no-money-breakdown')).toBe('Time and tokens')
    expect(subtitleOf('time-only-breakdown')).toBe('Agent time')
  })
})

describe('view navigation', () => {
  test('offers the three views and marks the current one', () => {
    renderDetails(fixture('full-breakdown'))
    const nav = screen.getByRole('navigation', { name: 'Usage views' })
    expect(within(nav).getByRole('button', { name: 'Breakdown' })).toHaveAttribute('aria-current', 'page')
    expect(within(nav).getByRole('button', { name: 'Agent jobs' })).not.toHaveAttribute('aria-current', 'page')
    expect(within(nav).getByRole('button', { name: 'Charts' })).toBeInTheDocument()
  })

  test('a view button asks the controller to switch', () => {
    const { analytics } = renderDetails(fixture('full-breakdown'))
    fireEvent.click(screen.getByRole('button', { name: 'Agent jobs' }))
    expect(analytics.setView).toHaveBeenCalledWith('jobs')
    fireEvent.click(screen.getByRole('button', { name: 'Charts' }))
    expect(analytics.setView).toHaveBeenLastCalledWith('charts')
  })

  test('renders the content of the answered view', () => {
    renderDetails(fixture('full-jobs'))
    expect(screen.getByRole('table', { name: 'Agent jobs' })).toBeInTheDocument()
    expect(screen.queryByRole('table', { name: /Usage by/ })).toBeNull()
  })

  test('when the view changed under a lost focus, focus lands on the active view button', () => {
    const data = fixture('full-breakdown')
    const first = controllerFor(data)
    const { rerender } = render(<UsageDetails analytics={first} onBack={() => {}} live={false} />)
    const jobsData = fixture('full-jobs')
    const second = controllerFor(jobsData)
    rerender(<UsageDetails analytics={second} onBack={() => {}} live={false} />)
    expect(screen.getByRole('button', { name: 'Agent jobs' })).toHaveFocus()
  })
})

describe('summary', () => {
  test('shows estimated cost, tokens, agent time and jobs of the filtered set', () => {
    renderDetails(fixture('full-breakdown'))
    const summary = screen.getByLabelText('Filtered usage totals')
    expect(within(summary).getByText('$1.92')).toBeInTheDocument()
    expect(within(summary).getByText('40.5K')).toBeInTheDocument()
    expect(within(summary).getByText('13m 30s')).toBeInTheDocument()
    expect(within(summary).getByText('6')).toBeInTheDocument()
  })

  test('the token total opens its composition, which adds up to the total', () => {
    renderDetails(fixture('full-breakdown'))
    const toggle = screen.getByRole('button', { name: /40\.5K tokens/ })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    const mix = document.getElementById(toggle.getAttribute('aria-controls')!)!
    expect(mix).not.toHaveAttribute('hidden')
    expect(within(mix).getByText('Uncached input').nextSibling).toHaveTextContent('10K')
    expect(within(mix).getByText('Cache read').nextSibling).toHaveTextContent('25K')
    expect(within(mix).getByText('Cache write').nextSibling).toHaveTextContent('1.4K')
    expect(within(mix).getByText('Output').nextSibling).toHaveTextContent('4.1K')
    expect(within(mix).getByText(/Output includes reasoning/)).toBeInTheDocument()
  })

  test('says which data is partial and why, in words', () => {
    renderDetails(fixture('full-breakdown'))
    const list = screen.getByRole('list', { name: 'Data completeness' })
    expect(within(list).getByText('Partial time')).toBeInTheDocument()
    expect(within(list).getByText('Partial tokens')).toBeInTheDocument()
    expect(within(list).getByText('Partial cost')).toBeInTheDocument()
    expect(list.textContent).toContain('a job was interrupted, its duration is unknown')
  })

  test('a legacy run says its time is unknown', () => {
    renderDetails(fixture('legacy-breakdown'))
    const list = screen.getByRole('list', { name: 'Data completeness' })
    expect(within(list).getByText('Time unknown')).toBeInTheDocument()
    expect(list.textContent).toContain('this run has no agent jobs journal')
  })

  test('names the work that is outside the run total', () => {
    renderDetails(fixture('full-breakdown'))
    expect(screen.getByText(/Outside run total: 1 agent job, 0m 15s agent time/)).toBeInTheDocument()
  })

  test('without money the summary has no cost and no dollar sign anywhere on the page', () => {
    const { container } = renderDetails(fixture('no-money-breakdown'))
    expect(container.querySelector('.usage-scroll')?.textContent).not.toMatch(/\$|cost|price|estimated/i)
    expect(container.querySelector('.usage-details-subtitle')?.textContent).not.toMatch(/cost|price|estimated/i)
    expect(container.querySelector('.usage-scroll')?.innerHTML).not.toMatch(/(aria-label|title)="[^"]*(cost|price|\$)/i)
  })

  test('in time-only mode there are neither tokens nor money anywhere on the page', () => {
    const { container } = renderDetails(fixture('time-only-breakdown'))
    expect(container.querySelector('.usage-scroll')?.textContent).not.toMatch(/\$|cost|price|token|estimated/i)
    expect(container.querySelector('.usage-details-subtitle')?.textContent).not.toMatch(/cost|token|estimated/i)
    expect(container.querySelector('.usage-scroll')?.innerHTML).not.toMatch(/(aria-label|title)="[^"]*(cost|token|\$)/i)
  })
})

describe('toolbar', () => {
  test('breakdown groups by model or agent', () => {
    const { analytics } = renderDetails(fixture('full-breakdown'))
    expect(screen.getByRole('button', { name: 'By agent' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: 'By model' })).toHaveAttribute('aria-pressed', 'false')
    fireEvent.click(screen.getByRole('button', { name: 'By model' }))
    expect(analytics.setGroup).toHaveBeenCalledWith('model')
  })

  test('the stage filter lists the stages and the scopes that have data', () => {
    const { analytics } = renderDetails(fixture('full-breakdown'))
    const select = screen.getByLabelText('Stage')
    expect([...select.querySelectorAll('option')].map((option) => option.textContent)).toEqual(['All stages', 'Build', 'Review', 'Run-wide'])
    fireEvent.change(select, { target: { value: 'stage:build' } })
    expect(analytics.setScope).toHaveBeenCalledWith('stage', 'build')
    fireEvent.change(select, { target: { value: 'run_wide' } })
    expect(analytics.setScope).toHaveBeenLastCalledWith('run_wide')
    fireEvent.change(select, { target: { value: 'all' } })
    expect(analytics.setScope).toHaveBeenLastCalledWith('all')
  })

  test('the sort menu shows the active option and offers both directions', () => {
    const { analytics } = renderDetails(fixture('full-breakdown'))
    const select = screen.getByLabelText('Sort') as HTMLSelectElement
    expect(select.value).toBe('time:desc')
    const labels = [...select.querySelectorAll('option')].map((option) => option.textContent)
    expect(labels).toEqual([
      'Cost: high to low',
      'Cost: low to high',
      'Tokens: high to low',
      'Tokens: low to high',
      'Time: long to short',
      'Time: short to long',
      'Jobs: most first',
      'Jobs: fewest first',
      'Agent: A–Z',
      'Agent: Z–A',
    ])
    fireEvent.change(select, { target: { value: 'cost:asc' } })
    expect(analytics.setSort).toHaveBeenCalledWith('cost', 'asc')
  })

  test('the jobs view adds agent and model filters and a way to clear them', () => {
    const data = fixture('full-jobs')
    const base = controllerFor(data)
    const analytics = controllerFor(data, { query: { ...base.query, agent: 'stage:build' } })
    render(<UsageDetails analytics={analytics} onBack={() => {}} live={false} />)
    expect(screen.getByLabelText('Agent')).toHaveValue('stage:build')
    fireEvent.change(screen.getByLabelText('Model'), { target: { value: 'codex/gpt-5.6-sol' } })
    expect(analytics.setModel).toHaveBeenCalledWith('codex/gpt-5.6-sol')
    fireEvent.change(screen.getByLabelText('Agent'), { target: { value: '' } })
    expect(analytics.setAgent).toHaveBeenCalledWith('')
    fireEvent.click(screen.getByRole('button', { name: 'Clear filters' }))
    expect(analytics.clearFilters).toHaveBeenCalledTimes(1)
  })

  test('without a filter there is nothing to clear', () => {
    renderDetails(fixture('full-jobs'))
    expect(screen.queryByRole('button', { name: 'Clear filters' })).toBeNull()
  })

  test('charts compare by a metric with joined pressed buttons and have no sort menu', () => {
    const { analytics } = renderDetails(fixture('full-charts'))
    const group = screen.getByRole('group', { name: 'Compare' })
    expect(within(group).getAllByRole('button').map((b) => b.textContent)).toEqual(['Estimated cost', 'Agent time', 'Tokens'])
    expect(within(group).getByRole('button', { name: 'Estimated cost' })).toHaveAttribute('aria-pressed', 'true')
    fireEvent.click(within(group).getByRole('button', { name: 'Tokens' }))
    expect(analytics.setMetric).toHaveBeenCalledWith('tokens')
    expect(screen.queryByLabelText('Sort')).toBeNull()
  })

  test('without money the choices are Agent time and Tokens and no sort option mentions cost', () => {
    renderDetails(fixture('no-money-breakdown'))
    expect([...(screen.getByLabelText('Sort') as HTMLSelectElement).options].map((o) => o.textContent).join(' ')).not.toMatch(/cost/i)
    const charts = renderDetails(fixture('no-money-charts'))
    const group = within(charts.container).getByRole('group', { name: 'Compare' })
    expect(within(group).getAllByRole('button').map((b) => b.textContent)).toEqual(['Agent time', 'Tokens'])
    expect(within(group).getByRole('button', { name: 'Tokens' })).toHaveAttribute('aria-pressed', 'true')
  })

  test('in time-only mode there is no single-option metric switch', () => {
    renderDetails(fixture('time-only-charts'))
    expect(screen.queryByRole('group', { name: 'Compare' })).toBeNull()
  })
})

describe('empty states', () => {
  test('a run with no jobs yet says so', () => {
    const data = fixture('full-breakdown')
    renderDetails({ ...data, totals: { ...data.totals, jobs: 0 }, breakdown: { group: 'agent', rows: [] } })
    expect(screen.getByRole('heading', { name: 'No usage yet' })).toBeInTheDocument()
  })

  test('a filter that matches nothing offers to clear it', () => {
    const data = fixture('full-jobs')
    const base = controllerFor(data)
    const empty: UsageAnalytics = { ...data, totals: { ...data.totals, jobs: 0 }, jobs: { ...data.jobs!, items: [], total: 0, nextCursor: null } }
    const analytics = controllerFor(empty, { query: { ...base.query, agent: 'stage:build' } })
    render(<UsageDetails analytics={analytics} onBack={() => {}} live={false} />)
    expect(screen.getByRole('heading', { name: 'No matching jobs' })).toBeInTheDocument()
    const clear = screen.getAllByRole('button', { name: 'Clear filters' })
    fireEvent.click(clear[clear.length - 1]!)
    expect(analytics.clearFilters).toHaveBeenCalled()
  })
})

describe('charts emptiness', () => {
  test('zero measured jobs with a legacy token comparison keeps the comparison', () => {
    const data = fixture('full-charts')
    renderDetails({ ...data, totals: { ...data.totals, jobs: 0 } })
    expect(screen.queryByRole('heading', { name: 'No usage yet' })).toBeNull()
    expect(screen.getByRole('region', { name: /by model/ })).toBeInTheDocument()
  })

  test('Assistant-only chart data is not an empty run', () => {
    const data = fixture('full-charts')
    const charts = { ...data.charts!, comparison: data.charts!.comparison.slice(0, 1), timeline: { ...data.charts!.timeline, intervals: [] } }
    renderDetails({ ...data, totals: { ...data.totals, jobs: 0 }, charts })
    expect(screen.queryByRole('heading', { name: 'No usage yet' })).toBeNull()
    expect(screen.getByRole('region', { name: /by model/ })).toBeInTheDocument()
  })

  test('charts with no comparison, no timeline and no outside-run jobs are empty', () => {
    const data = fixture('full-charts')
    const charts = { ...data.charts!, comparison: [], timeline: { ...data.charts!.timeline, intervals: [], unplaced: 0 } }
    renderDetails({ ...data, totals: { ...data.totals, jobs: 0 }, outsideRunTotal: null, charts })
    expect(screen.getByRole('heading', { name: 'No usage yet' })).toBeInTheDocument()
  })
})

describe('data that the mode no longer shows', () => {
  test('retained token coverage is gone once the mode is time_only, whatever the new response does', () => {
    const data = fixture('full-breakdown')
    const { container } = renderDetails(data, { data: null, lastData: data, state: 'loading', mode: 'time_only', metrics: ['agent_time'] })
    expect(screen.getByRole('list', { name: 'Data completeness' })).toBeInTheDocument()
    expect(container.querySelector('.usage-scroll')?.textContent).not.toMatch(/token|cost|price|\$/i)
  })

  test('retained money coverage is gone once the mode is no_money', () => {
    const data = fixture('full-breakdown')
    const { container } = renderDetails(data, { data: null, lastData: data, state: 'loading', mode: 'no_money', metrics: ['agent_time', 'tokens'] })
    expect(container.querySelector('.usage-scroll')?.textContent).not.toMatch(/cost|price|\$/i)
    expect(screen.getAllByText('Partial tokens').length).toBeGreaterThan(0)
  })
})

describe('counting rules', () => {
  test('are a disclosure that is closed by default', () => {
    renderDetails(fixture('full-breakdown'))
    const details = screen.getByText('How usage is counted').closest('details')!
    expect(details).not.toHaveAttribute('open')
    expect(details.textContent).toContain('Agent job')
  })

  test('do not explain cost or tokens when the mode hides them', () => {
    renderDetails(fixture('time-only-breakdown'))
    const details = screen.getByText('How usage is counted').closest('details')!
    expect(details.textContent).not.toMatch(/cost|token|price/i)
  })
})

describe('running durations', () => {
  test('tick only while the status is fresh', () => {
    vi.useFakeTimers()
    const data = fixture('full-breakdown')
    vi.setSystemTime(Date.parse(data.asOf))
    renderDetails(data, {}, true)
    const summary = screen.getByLabelText('Filtered usage totals')
    expect(within(summary).getByText('13m 30s')).toBeInTheDocument()
    act(() => {
      vi.advanceTimersByTime(5_000)
    })
    expect(within(summary).getByText('13m 35s')).toBeInTheDocument()
  })

  test('stay put when the status is not fresh', () => {
    vi.useFakeTimers()
    const data = fixture('full-breakdown')
    vi.setSystemTime(Date.parse(data.asOf))
    renderDetails(data, {}, false)
    act(() => {
      vi.advanceTimersByTime(5_000)
    })
    expect(within(screen.getByLabelText('Filtered usage totals')).getByText('13m 30s')).toBeInTheDocument()
  })

  test('freeze at the last shown value when the status is lost', () => {
    vi.useFakeTimers()
    const data = fixture('full-breakdown')
    vi.setSystemTime(Date.parse(data.asOf))
    const { loseStatus } = renderLive(data)
    const totals = (): string | null => screen.getByLabelText('Filtered usage totals').textContent
    act(() => {
      vi.advanceTimersByTime(5_000)
    })
    const shown = totals()
    expect(shown).toContain('13m 35s')

    loseStatus()
    expect(totals()).toBe(shown)
    act(() => {
      vi.advanceTimersByTime(5_000)
    })
    expect(totals()).toBe(shown)
  })
})

describe('running Assistant job', () => {
  test('the outside-run duration ticks even when no job of the run is running', () => {
    vi.useFakeTimers()
    const data = fixture('full-breakdown')
    vi.setSystemTime(Date.parse(data.asOf))
    renderDetails({ ...data, totals: { ...data.totals, activeJobs: 0 }, outsideRunTotal: { ...data.outsideRunTotal!, activeJobs: 1 } }, {}, true)
    expect(screen.getByText(/Outside run total: 1 agent job, 0m 15s agent time/)).toBeInTheDocument()
    act(() => {
      vi.advanceTimersByTime(5_000)
    })
    expect(screen.getByText(/Outside run total: 1 agent job, 0m 20s agent time/)).toBeInTheDocument()
  })

  test('the outside-run duration freezes at the last shown value when the status is lost', () => {
    vi.useFakeTimers()
    const data = fixture('full-breakdown')
    vi.setSystemTime(Date.parse(data.asOf))
    const { loseStatus } = renderLive({ ...data, totals: { ...data.totals, activeJobs: 0 }, outsideRunTotal: { ...data.outsideRunTotal!, activeJobs: 1 } })
    const note = (): string | null => screen.getByText(/Outside run total: /).textContent
    act(() => {
      vi.advanceTimersByTime(5_000)
    })
    const shown = note()
    expect(shown).toContain('0m 20s')

    loseStatus()
    expect(note()).toBe(shown)
    act(() => {
      vi.advanceTimersByTime(5_000)
    })
    expect(note()).toBe(shown)
  })
})
