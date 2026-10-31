import { fireEvent, render, screen, within } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import type { UsageAnalyticsController } from '../../hooks/use-usage-analytics'
import type { UsageAnalytics } from '../../types/usage-analytics'
import { UsageCharts } from './UsageCharts'
import { controllerFor, fixture } from './usage-test-support'

function renderCharts(data: UsageAnalytics, overrides: Partial<UsageAnalyticsController> = {}, since = 0) {
  const analytics = controllerFor(data, overrides)
  const view = render(<UsageCharts analytics={analytics} data={data} sinceAsOfMs={since} />)
  return { analytics, ...view }
}

function metricQuery(data: UsageAnalytics, metric: 'cost' | 'agent_time' | 'tokens') {
  return { query: { ...controllerFor(data).query, metric } }
}

describe('comparison bars', () => {
  test('headline names the metric and the group, and bars are ordered by the metric', () => {
    const data = fixture('full-charts')
    renderCharts(data, metricQuery(data, 'agent_time'))
    expect(screen.getByRole('heading', { name: 'Agent time by model' })).toBeInTheDocument()
    const bars = screen.getAllByRole('button', { name: /View jobs/ })
    expect(bars.map((bar) => bar.getAttribute('aria-label')?.split(':')[0])).toEqual(['claude-opus-4-8', 'glm-5.3', 'gpt-5.6-sol'])
  })

  test('each bar has a text label, its value and its share, and the first bar is full width', () => {
    const data = fixture('full-charts')
    renderCharts(data, metricQuery(data, 'agent_time'))
    const bar = screen.getByRole('button', { name: /^claude-opus-4-8: 7m 00s, 52 percent\. View jobs\.$/ })
    expect(within(bar).getByText('claude-opus-4-8')).toBeInTheDocument()
    expect(within(bar).getByText('7m 00s')).toBeInTheDocument()
    expect(within(bar).getByText('52%')).toBeInTheDocument()
    expect(bar.querySelector('.usage-bar-fill')).toHaveStyle({ width: '100%' })
  })

  test('the bar takes the colour of its model', () => {
    const data = fixture('full-charts')
    renderCharts(data, metricQuery(data, 'agent_time'))
    const bar = screen.getByRole('button', { name: /^claude-opus-4-8:/ })
    expect(bar.querySelector('.usage-bar-fill')).toHaveStyle({ '--model-color': 'var(--model-color-0)' })
  })

  test('a model without a value for the metric is an em dash with no bar', () => {
    const data = fixture('full-charts')
    renderCharts(data, metricQuery(data, 'cost'))
    const glm = screen.getByRole('button', { name: /^glm-5\.3:/ })
    expect(within(glm).getByText('—')).toBeInTheDocument()
    expect(glm.querySelector('.usage-bar-fill')).toHaveStyle({ width: '0%' })
  })

  test('a bar opens the jobs of its model', () => {
    const data = fixture('full-charts')
    const { analytics } = renderCharts(data, metricQuery(data, 'agent_time'))
    fireEvent.click(screen.getByRole('button', { name: /^glm-5\.3:/ }))
    expect(analytics.drill).toHaveBeenCalledWith({ model: 'anthropic/glm-5.3' })
  })

  test('by agent a bar opens the agent and a mixed model is said in words with a neutral bar', () => {
    const breakdown = fixture('full-breakdown')
    const data: UsageAnalytics = {
      ...breakdown,
      view: 'charts',
      query: { ...breakdown.query, view: 'charts' },
      breakdown: null,
      charts: { group: 'agent', comparison: breakdown.breakdown!.rows, timeline: fixture('full-charts').charts!.timeline },
    }
    const { analytics } = renderCharts(data, metricQuery(data, 'agent_time'))
    fireEvent.click(screen.getByRole('button', { name: /^Build:/ }))
    expect(analytics.drill).toHaveBeenCalledWith({ agent: 'stage:build' })
    const assistant = screen.getByRole('button', { name: /^Assistant:/ })
    expect(within(assistant).getByText('Model unknown')).toBeInTheDocument()
    expect(assistant.querySelector('.usage-bar-fill')).toHaveStyle({ '--model-color': 'var(--model-color-unknown)' })
  })
})

describe('timeline', () => {
  test('has a row per agent and one button per job', () => {
    renderCharts(fixture('full-charts'))
    const timeline = screen.getByRole('region', { name: 'Agent job timeline' })
    expect(within(timeline).getAllByRole('button')).toHaveLength(5)
    expect(within(timeline).getByText('Reviewer')).toBeInTheDocument()
  })

  test('a job is placed by its offset and duration on the range', () => {
    renderCharts(fixture('full-charts'))
    const job = screen.getByRole('button', { name: /^Build · implementation,/ })
    expect(job).toHaveStyle({ left: `${(130_000 / 720_000) * 100}%`, width: `${(300_000 / 720_000) * 100}%` })
    expect(job).toHaveStyle({ '--model-color': 'var(--model-color-0)' })
  })

  test('a job label says what it is without any money', () => {
    renderCharts(fixture('full-charts'))
    const job = screen.getByRole('button', { name: /^Verify · step 1,/ })
    expect(job.getAttribute('aria-label')).toBe('Verify · step 1, gpt-5.6-sol, starts +6m 40s, duration 1m 00s. View job.')
    expect(job.getAttribute('aria-label')).not.toMatch(/\$/)
  })

  test('selecting a job asks the controller to open its page and highlight it', () => {
    const { analytics } = renderCharts(fixture('full-charts'))
    fireEvent.click(screen.getByRole('button', { name: /^Review the diff,/ }))
    expect(analytics.focusJob).toHaveBeenCalledWith('job-3')
  })

  test('the legend names each model that appears with a text label', () => {
    renderCharts(fixture('full-charts'))
    const legend = screen.getByRole('list', { name: 'Models' })
    expect(within(legend).getAllByRole('listitem').map((item) => item.textContent)).toEqual(['claude-opus-4-8', 'gpt-5.6-sol', 'glm-5.3'])
  })

  test('says how many jobs could not be placed', () => {
    renderCharts(fixture('full-charts'))
    expect(screen.getByText('2 jobs have no place on the timeline, for example after an interruption or in an older run.')).toBeInTheDocument()
  })

  test('says when the timeline was cut', () => {
    const data = fixture('full-charts')
    renderCharts({ ...data, charts: { ...data.charts!, timeline: { ...data.charts!.timeline, truncated: true, total: 3000, limit: 2000, unplaced: 0 } } })
    expect(screen.getByText('Showing 2000 of 3000 jobs.')).toBeInTheDocument()
  })

  test('a timeline with nothing to draw says so', () => {
    const data = fixture('full-charts')
    renderCharts({ ...data, charts: { ...data.charts!, timeline: { ...data.charts!.timeline, intervals: [], total: 0, unplaced: 0 } } })
    expect(screen.getByText('No timed jobs to place on the timeline yet.')).toBeInTheDocument()
  })

  test('a running job grows with the time since as_of', () => {
    renderCharts(fixture('full-charts'), {}, 60_000)
    const running = screen.getByRole('button', { name: /^Review · implementation,/ })
    expect(running).toHaveStyle({ width: `${(300_000 / 780_000) * 100}%` })
  })
})

describe('modes', () => {
  test('without money no cost, no dollar sign, and the tokens metric is a bar', () => {
    const data = fixture('no-money-charts')
    const { container } = renderCharts(data, metricQuery(data, 'tokens'))
    expect(container.textContent).not.toMatch(/\$|cost|price/i)
    expect(container.innerHTML).not.toMatch(/aria-label="[^"]*(cost|\$)/i)
    expect(screen.getByRole('heading', { name: 'Tokens by model' })).toBeInTheDocument()
  })

  test('in time-only mode only time is drawn', () => {
    const data = fixture('time-only-charts')
    const { container } = renderCharts(data)
    expect(container.textContent).not.toMatch(/\$|cost|token/i)
    expect(screen.getByRole('heading', { name: 'Agent time by model' })).toBeInTheDocument()
  })
})

describe('many models', () => {
  test('twenty models each get a text label and a colour of their own', () => {
    const data = fixture('full-charts')
    const template = data.charts!.comparison[0]!
    const rows = Array.from({ length: 20 }, (_, i) => ({
      ...template,
      key: `p/model-${i}`,
      label: `model-${i}`,
      model: { key: `p/model-${i}`, channel: 'p', model: `model-${i}`, colorOrdinal: i },
    }))
    renderCharts({ ...data, charts: { ...data.charts!, comparison: rows } }, metricQuery(data, 'agent_time'))
    const bars = screen.getAllByRole('button', { name: /View jobs/ })
    expect(bars).toHaveLength(20)
    const colors = bars.map((bar) => (bar.querySelector('.usage-bar-fill') as HTMLElement).style.getPropertyValue('--model-color'))
    expect(new Set(colors).size).toBe(20)
    expect(colors.slice(12).every((color) => color.startsWith('oklch('))).toBe(true)
    for (let i = 0; i < 20; i++) expect(within(bars[i]!).getByText(`model-${i}`)).toBeInTheDocument()
  })
})

describe('running comparison', () => {
  test('a running job advances in the label and the share by the time since as_of', () => {
    const data = fixture('full-charts')
    renderCharts(data, metricQuery(data, 'agent_time'), 60_000)
    expect(screen.getByRole('button', { name: /^glm-5\.3: 5m 00s, 34 percent\. View jobs\.$/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^claude-opus-4-8: 7m 00s, 48 percent\./ })).toBeInTheDocument()
  })

  test('the advanced duration decides the order of the bars', () => {
    const data = fixture('full-charts')
    renderCharts(data, metricQuery(data, 'agent_time'), 300_000)
    const bars = screen.getAllByRole('button', { name: /View jobs/ })
    expect(bars.map((bar) => bar.getAttribute('aria-label')?.split(':')[0])).toEqual(['glm-5.3', 'claude-opus-4-8', 'gpt-5.6-sol'])
    expect(bars[0]?.querySelector('.usage-bar-fill')).toHaveStyle({ width: '100%' })
  })
})
