import { fireEvent, render, screen, within } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import type { UsageAnalyticsController } from '../../hooks/use-usage-analytics'
import type { UsageAnalytics } from '../../types/usage-analytics'
import { JobsTable } from './JobsTable'
import { controllerFor, fixture } from './usage-test-support'

function renderJobs(data: UsageAnalytics, overrides: Partial<UsageAnalyticsController> = {}, since = 0) {
  const analytics = controllerFor(data, overrides)
  render(<JobsTable analytics={analytics} data={data} sinceAsOfMs={since} />)
  return analytics
}

function rowOf(title: RegExp | string, index = 0): HTMLElement {
  return within(screen.getByRole('table', { name: 'Agent jobs' })).getAllByText(title)[index]!.closest('tr')!
}

// Meta line of a job row without its flags (status, Outside run total).
function metaText(row: HTMLElement): string {
  const meta = row.querySelector('.usage-job-name small')!.cloneNode(true) as HTMLElement
  meta.querySelectorAll('.usage-job-flag').forEach((flag) => flag.remove())
  return meta.textContent ?? ''
}

describe('JobsTable rows', () => {
  test('lists title, agent and stage, model, start, duration, tokens and cost', () => {
    renderJobs(fixture('full-jobs'))
    const verify = rowOf('Verify · step 1')
    expect(within(verify).getByText(/Verify · review · Review/)).toBeInTheDocument()
    expect(within(verify).getByText('gpt-5.6-sol')).toBeInTheDocument()
    expect(within(verify).getByText('+6m 40s')).toBeInTheDocument()
    expect(within(verify).getByText('1m 00s')).toBeInTheDocument()
    expect(within(verify).getByText('2.5K')).toBeInTheDocument()
    expect(within(verify).getByText('$0.15')).toBeInTheDocument()
  })

  test('a stage agent job names its stage once and keeps the status flag', () => {
    renderJobs(fixture('full-jobs'))
    const build = rowOf('Build · implementation')
    expect(metaText(build)).toBe('Build')
    expect(within(build).getByText('Interrupted')).toBeInTheDocument()
    const review = rowOf('Review · implementation')
    expect(metaText(review)).toBe('Review')
    expect(within(review).getByText('Running')).toBeInTheDocument()
  })

  test('a helper, verify or Assistant job keeps its agent next to the stage', () => {
    const data = fixture('full-jobs')
    const helper = { id: 'helper:t-1', kind: 'helper', title: 'Reviewer', outsideRunTotal: false }
    const items = data.jobs!.items.map((job) => (job.id === 'job-4' ? { ...job, title: 'Reviewer · answer', agent: helper } : job))
    renderJobs({ ...data, jobs: { ...data.jobs!, items } })
    expect(metaText(rowOf('Reviewer · answer'))).toBe('Reviewer · Review')
    const assistant = rowOf('Assistant · job 1')
    expect(metaText(assistant)).toBe('Assistant · Run-wide')
    expect(within(assistant).getByText('Outside run total')).toBeInTheDocument()
  })

  test('an interrupted job has an em dash for its unknown duration and start, and says why', () => {
    renderJobs(fixture('full-jobs'))
    const interrupted = rowOf('Build · implementation')
    expect(within(interrupted).getByText('Interrupted')).toBeInTheDocument()
    expect(within(interrupted).getAllByText('—').length).toBeGreaterThanOrEqual(3)
    expect(interrupted.textContent).not.toMatch(/0s/)
  })

  test('a running job is marked and its duration ticks from as_of', () => {
    renderJobs(fixture('full-jobs'), {}, 5_000)
    const running = rowOf('Review · implementation')
    expect(within(running).getByText('Running')).toBeInTheDocument()
    expect(within(running).getByText('4m 05s')).toBeInTheDocument()
  })

  test('a configured model says so and a missing model is Unknown', () => {
    renderJobs(fixture('full-jobs'))
    expect(within(rowOf('Review · implementation')).getByText(/configured/)).toBeInTheDocument()
    expect(within(rowOf('Assistant · job 1')).getByText('Unknown model')).toBeInTheDocument()
    expect(within(rowOf('Assistant · job 1')).getByText('Outside run total')).toBeInTheDocument()
  })

  test('colours every model mark from the model ordinal', () => {
    renderJobs(fixture('full-jobs'))
    expect(rowOf('Verify · step 1').querySelector('.usage-model-dot')).toHaveStyle({ '--model-color': 'var(--model-color-1)' })
    expect(rowOf('Assistant · job 1').querySelector('.usage-model-dot')).toHaveStyle({ '--model-color': 'var(--model-color-unknown)' })
  })

  test('cells carry labels for the card layout', () => {
    renderJobs(fixture('full-jobs'))
    const verify = rowOf('Verify · step 1')
    expect([...verify.querySelectorAll('td[data-label]')].map((cell) => cell.getAttribute('data-label'))).toEqual([
      'Start',
      'Duration',
      'Tokens',
      'Est. cost',
    ])
  })
})

describe('JobsTable sorting', () => {
  test('the active header has aria-sort and the others none', () => {
    renderJobs(fixture('full-jobs'))
    expect(screen.getByRole('columnheader', { name: /Start/ })).toHaveAttribute('aria-sort', 'descending')
    for (const name of [/Job/, /Model/, /Duration/, /Tokens/, /Est\. cost/]) {
      expect(screen.getByRole('columnheader', { name })).toHaveAttribute('aria-sort', 'none')
    }
  })

  test('each sortable header asks the controller to sort by its key', () => {
    const analytics = renderJobs(fixture('full-jobs'))
    const keys: [RegExp, string][] = [[/Job/, 'name'], [/Model/, 'model'], [/Start/, 'started'], [/Duration/, 'time'], [/Tokens/, 'tokens'], [/Est\. cost/, 'cost']]
    for (const [name, key] of keys) {
      fireEvent.click(within(screen.getByRole('columnheader', { name })).getByRole('button'))
      expect(analytics.sortBy).toHaveBeenLastCalledWith(key)
    }
  })
})

describe('JobsTable pagination', () => {
  test('shows the range, the page and the start note', () => {
    renderJobs(fixture('full-jobs'))
    expect(screen.getByText('1–4 of 7 jobs')).toBeInTheDocument()
    expect(screen.getByText('1 / 2')).toBeInTheDocument()
    expect(screen.getByText('Start is relative to run start (active time)')).toBeInTheDocument()
  })

  test('next asks for the following page and previous is disabled on the first', () => {
    const analytics = renderJobs(fixture('full-jobs'))
    expect(screen.getByRole('button', { name: 'Previous jobs' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Next jobs' }))
    expect(analytics.nextPage).toHaveBeenCalledTimes(1)
  })

  test('the range follows the current page', () => {
    const data = fixture('full-jobs')
    renderJobs({ ...data, jobs: { ...data.jobs!, items: data.jobs!.items.slice(0, 3), nextCursor: null, prevCursor: 'c.0.x' } }, { page: 1, pageCount: 2 })
    expect(screen.getByText('5–7 of 7 jobs')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Next jobs' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Previous jobs' }))
  })
})

describe('JobsTable highlighted row', () => {
  test('marks the focused job and moves focus to its row once', () => {
    const data = fixture('full-jobs')
    const base = controllerFor(data)
    const analytics = controllerFor(data, { query: { ...base.query, highlightJobId: 'job-5' } })
    const { rerender } = render(<JobsTable analytics={analytics} data={data} sinceAsOfMs={0} />)
    const row = rowOf('Review · implementation')
    expect(row).toHaveAttribute('aria-current', 'true')
    expect(row).toHaveClass('is-highlighted')
    expect(row).toHaveFocus()

    ;(screen.getByRole('button', { name: 'Next jobs' }) as HTMLButtonElement).focus()
    rerender(<JobsTable analytics={analytics} data={{ ...data }} sinceAsOfMs={0} />)
    expect(screen.getByRole('button', { name: 'Next jobs' })).toHaveFocus()
  })

  test('other rows are not highlighted', () => {
    renderJobs(fixture('full-jobs'))
    expect(document.querySelector('.is-highlighted')).toBeNull()
  })
})

describe('JobsTable modes', () => {
  test('without money there is no cost column and no dollar sign', () => {
    renderJobs(fixture('no-money-jobs'))
    const table = screen.getByRole('table', { name: 'Agent jobs' })
    expect(table.textContent).not.toMatch(/\$|cost|price/i)
    expect(within(table).getByRole('columnheader', { name: /Tokens/ })).toBeInTheDocument()
  })

  test('in time-only mode there are no tokens or money', () => {
    renderJobs(fixture('time-only-jobs'))
    const table = screen.getByRole('table', { name: 'Agent jobs' })
    expect(table.textContent).not.toMatch(/\$|cost|token/i)
    expect(within(table).getByRole('columnheader', { name: /Duration/ })).toBeInTheDocument()
  })
})
