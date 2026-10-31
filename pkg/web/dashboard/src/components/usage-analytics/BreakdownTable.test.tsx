import { fireEvent, render, screen, within } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import type { UsageAnalytics } from '../../types/usage-analytics'
import { BreakdownTable } from './BreakdownTable'
import { controllerFor, fixture } from './usage-test-support'

function renderTable(data: UsageAnalytics, since = 0) {
  const analytics = controllerFor(data)
  render(<BreakdownTable analytics={analytics} data={data} sinceAsOfMs={since} />)
  return analytics
}

// The golden breakdown groups by agent; model rows come from the comparison rows of the charts fixture.
function byModel(name: string): UsageAnalytics {
  const charts = fixture(name)
  return { ...charts, view: 'breakdown', query: { ...charts.query, view: 'breakdown' }, breakdown: { group: 'model', rows: charts.charts?.comparison ?? [] }, charts: null }
}

describe('BreakdownTable by agent', () => {
  test('has one row per agent and labels the Assistant as outside the run total', () => {
    renderTable(fixture('full-breakdown'))
    const rows = within(screen.getByRole('table', { name: 'Usage by agent' })).getAllByRole('row').slice(1)
    expect(rows).toHaveLength(5)
    expect(within(rows[4]!).getByText('Outside run total')).toBeInTheDocument()
    expect(screen.getAllByText('Outside run total')).toHaveLength(1)
  })

  test('shows jobs, time, tokens and the estimated cost with its share of the total', () => {
    renderTable(fixture('full-breakdown'))
    const build = screen.getByRole('button', { name: /^Build/ }).closest('tr')!
    expect(within(build).getByText('3')).toBeInTheDocument()
    expect(within(build).getByText('7m 00s')).toBeInTheDocument()
    expect(within(build).getByText('33K')).toBeInTheDocument()
    expect(build.textContent).toMatch(/\$\d+\.\d\d/)
    expect(build.textContent).toMatch(/\d+%/)
    expect(screen.getByRole('columnheader', { name: /Est\. cost/ })).toBeInTheDocument()
  })

  test('a row opens the jobs of that agent', () => {
    const analytics = renderTable(fixture('full-breakdown'))
    fireEvent.click(screen.getByRole('button', { name: /^Build/ }))
    expect(analytics.drill).toHaveBeenCalledWith({ agent: 'stage:build' })
  })

  test('the drill arrow of a row is a separate named button', () => {
    const analytics = renderTable(fixture('full-breakdown'))
    fireEvent.click(screen.getByRole('button', { name: 'View agent jobs for Reviewer' }))
    expect(analytics.drill).toHaveBeenCalledWith({ agent: 'helper:t-1' })
  })

  test('marks a mixed or unknown model in words and colours a single one', () => {
    renderTable(fixture('full-breakdown'))
    const build = screen.getByRole('button', { name: /^Build/ }).closest('tr')!
    expect(build.querySelector('.usage-model-dot')).not.toBeNull()
    const assistant = screen.getByRole('button', { name: /^Assistant/ }).closest('tr')!
    expect(assistant.querySelector('.usage-model-dot')).toBeNull()
    expect(within(assistant).getByText(/Model unknown/)).toBeInTheDocument()
  })

  test('the active column carries aria-sort and the others say none', () => {
    renderTable(fixture('full-breakdown'))
    expect(screen.getByRole('columnheader', { name: /Agent time/ })).toHaveAttribute('aria-sort', 'descending')
    expect(screen.getByRole('columnheader', { name: /Tokens/ })).toHaveAttribute('aria-sort', 'none')
    expect(screen.getByRole('columnheader', { name: /Jobs/ })).toHaveAttribute('aria-sort', 'none')
  })

  test('a header asks the controller to sort by its column', () => {
    const analytics = renderTable(fixture('full-breakdown'))
    fireEvent.click(within(screen.getByRole('columnheader', { name: /Tokens/ })).getByRole('button'))
    expect(analytics.sortBy).toHaveBeenCalledWith('tokens')
  })

  test('keeps the horizontal padding of header and body cells equal by sharing one cell class', () => {
    renderTable(fixture('full-breakdown'))
    const header = screen.getByRole('columnheader', { name: /Agent time/ })
    expect(header.className).toContain('usage-numeric')
    const cell = screen.getAllByText('7m 00s')[0]!.closest('td')!
    expect(cell.className).toContain('usage-numeric')
  })
})

describe('BreakdownTable by model', () => {
  test('colours each model and drills by the model key', () => {
    const analytics = renderTable(byModel('full-charts'))
    const row = screen.getByRole('button', { name: /^claude-opus-4-8/ }).closest('tr')!
    expect(row.querySelector('.usage-model-dot')).toHaveStyle({ '--model-color': 'var(--model-color-0)' })
    fireEvent.click(screen.getByRole('button', { name: /^claude-opus-4-8/ }))
    expect(analytics.drill).toHaveBeenCalledWith({ model: 'anthropic/claude-opus-4-8' })
  })

  test('says the model was configured, not observed, when only a hint is known', () => {
    renderTable(byModel('full-charts'))
    const glm = screen.getByRole('button', { name: /^glm-5\.3/ }).closest('tr')!
    expect(within(glm).getByText(/configured/)).toBeInTheDocument()
  })
})

describe('BreakdownTable model meta', () => {
  // withAgents sets the per-row agents count and channel of the first model row, as the server sends them.
  function withFirstRow(agents: number | null, channel: string): UsageAnalytics {
    const data = byModel('full-charts')
    const rows = (data.breakdown?.rows ?? []).map((row, i) =>
      i === 0 && row.model !== null ? { ...row, agents, model: { ...row.model, channel } } : row)
    return { ...data, breakdown: { group: 'model', rows } }
  }

  test.each([
    [2, 'anthropic', 'Anthropic · 2 agents'],
    [1, 'codex', 'Codex · 1 agent'],
    [3, '', '3 agents'],
    [null, 'anthropic', 'Anthropic'],
  ])('%s agents on channel "%s" read "%s"', (agents, channel, meta) => {
    renderTable(withFirstRow(agents, channel))
    const first = screen.getAllByRole('row')[1]!
    expect(within(first).getByText(meta)).toBeInTheDocument()
  })
})

describe('BreakdownTable honest unknowns', () => {
  test('a legacy run shows an em dash with a label instead of 0s', () => {
    renderTable(fixture('legacy-breakdown'))
    const build = screen.getByRole('button', { name: /^Build/ }).closest('tr')!
    expect(within(build).getByText('Time unknown')).toBeInTheDocument()
    expect(build.textContent).not.toContain('0s')
  })

  test('a row with some unknown durations is labelled partial', () => {
    renderTable(fixture('full-breakdown'))
    const build = screen.getByRole('button', { name: /^Build/ }).closest('tr')!
    expect(within(build).getByText('Partial time')).toBeInTheDocument()
  })

  test('a running row says so and its time keeps ticking from as_of', () => {
    renderTable(fixture('full-breakdown'), 5_000)
    const review = screen.getByRole('button', { name: /^Review Stage agent/ }).closest('tr')!
    expect(within(review).getByText('1 running')).toBeInTheDocument()
    expect(within(review).getByText('4m 05s')).toBeInTheDocument()
  })
})

describe('BreakdownTable money and token modes', () => {
  test('without money the table has no cost column, no dollar sign and no cost word', () => {
    renderTable(fixture('no-money-breakdown'))
    const table = screen.getByRole('table', { name: 'Usage by agent' })
    expect(table.textContent).not.toMatch(/\$|cost|price/i)
    expect(within(table).queryByRole('columnheader', { name: /cost/i })).toBeNull()
    expect(within(table).getByRole('columnheader', { name: /Tokens/ })).toBeInTheDocument()
    expect(table.innerHTML).not.toMatch(/aria-label="[^"]*(cost|\$)/i)
  })

  test('in time-only mode there are no token or money columns at all', () => {
    renderTable(fixture('time-only-breakdown'))
    const table = screen.getByRole('table', { name: 'Usage by agent' })
    expect(within(table).queryByRole('columnheader', { name: /Tokens/ })).toBeNull()
    expect(table.textContent).not.toMatch(/\$|cost|token/i)
    expect(within(table).getByRole('columnheader', { name: /Agent time/ })).toBeInTheDocument()
  })
})
