import { act, fireEvent, render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import type { UsageRequest } from '../../api/usage-analytics-client'
import { useUsageAnalytics } from '../../hooks/use-usage-analytics'
import type { UsageAnalytics, UsageAnalyticsStatus } from '../../types/usage-analytics'
import { UsageDetails } from './UsageDetails'
import { answerFor } from './usage-test-support'

const getUsageAnalytics = vi.fn<(request: UsageRequest, signal?: AbortSignal) => Promise<UsageAnalytics>>()

vi.mock('../../api/usage-analytics-client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/usage-analytics-client')>()),
  getUsageAnalytics: (request: UsageRequest, signal?: AbortSignal) => getUsageAnalytics(request, signal),
}))

type Call = { request: UsageRequest; resolve: (data: UsageAnalytics) => void; reject: (error: unknown) => void }
let calls: Call[] = []

type Props = { status: Pick<UsageAnalyticsStatus, 'revision' | 'mode'>; runEpoch: number }

function Harness({ status, runEpoch }: Props) {
  const analytics = useUsageAnalytics({ open: true, status, runEpoch })
  return <UsageDetails analytics={analytics} onBack={() => {}} live={false} />
}

beforeEach(() => {
  calls = []
  getUsageAnalytics.mockReset()
  getUsageAnalytics.mockImplementation((request) => new Promise<UsageAnalytics>((resolve, reject) => { calls.push({ request, resolve, reject }) }))
})

async function loadRunA() {
  const view = render(<Harness status={{ revision: 'r1', mode: 'full' }} runEpoch={0} />)
  await act(async () => calls[0]?.resolve(answerFor(calls[0].request, 'full-breakdown')))
  expect(screen.getByLabelText('Filtered usage totals')).toHaveTextContent('$1.92')
  return view
}

function expectNothingOfRunA() {
  expect(screen.queryByLabelText('Filtered usage totals')).toBeNull()
  expect(screen.queryByText(/Outside run total/)).toBeNull()
  expect(screen.queryByLabelText('Stage')).toBeNull()
  expect(screen.queryByText('How usage is counted')).toBeNull()
  expect(screen.queryByRole('table')).toBeNull()
}

describe('a run change', () => {
  test('shows nothing of the previous run while the new run loads', async () => {
    const { rerender } = await loadRunA()
    rerender(<Harness status={{ revision: 'r1', mode: 'full' }} runEpoch={1} />)
    expect(screen.getByRole('status')).toHaveTextContent(/loading/i)
    expectNothingOfRunA()
  })

  test('shows nothing of the previous run when the new run request fails', async () => {
    const { rerender } = await loadRunA()
    rerender(<Harness status={{ revision: 'r1', mode: 'full' }} runEpoch={1} />)
    await act(async () => calls.at(-1)?.reject(new Error('boom')))
    expect(screen.getByRole('alert')).toHaveTextContent('boom')
    expectNothingOfRunA()
  })

  test('a failed new filter of the same run keeps the toolbar so the filter can be changed back', async () => {
    await loadRunA()
    fireEvent.change(screen.getByLabelText('Stage'), { target: { value: 'stage:review' } })
    await act(async () => calls.at(-1)?.reject(new Error('boom')))
    expect(screen.getByRole('alert')).toHaveTextContent('boom')
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
    expect(screen.getByLabelText('Stage')).toHaveValue('stage:review')
    // All-stages totals under the selected Review stage would read as the Review totals.
    expect(screen.queryByLabelText('Filtered usage totals')).toBeNull()
    expect(screen.queryByText('$1.92')).toBeNull()

    fireEvent.change(screen.getByLabelText('Stage'), { target: { value: 'all' } })
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.getByLabelText('Filtered usage totals')).toHaveTextContent('$1.92')
  })

  test('a failed new filter of the same view shows no table rows of the previous filter', async () => {
    await loadRunA()
    expect(screen.getByRole('table', { name: 'Usage by agent' })).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Stage'), { target: { value: 'stage:review' } })
    // While the Review answer is awaited the All-stages table stays, so the sort button keeps focus.
    expect(screen.getByRole('table', { name: 'Usage by agent' })).toBeInTheDocument()
    await act(async () => calls.at(-1)?.reject(new Error('boom')))
    expect(screen.getByRole('alert')).toHaveTextContent('boom')
    expect(screen.queryByRole('table')).toBeNull()
    expect(screen.queryByRole('row')).toBeNull()
    expect(screen.queryByLabelText('Filtered usage totals')).toBeNull()
  })
})

// Enter/Space on a focused button is a click; the test gives it focus first, as Tab would.
function activate(button: HTMLElement): void {
  act(() => button.focus())
  fireEvent.click(button)
}

async function resolveLast(name: string): Promise<void> {
  await act(async () => calls.at(-1)?.resolve(answerFor(calls.at(-1)!.request, name)))
}

async function openJobs() {
  await loadRunA()
  fireEvent.click(screen.getByRole('button', { name: 'Agent jobs' }))
  await resolveLast('full-jobs')
  expect(screen.getByRole('table', { name: 'Agent jobs' })).toBeInTheDocument()
}

const contentBusy = (): string | null => document.querySelector('.usage-content')?.getAttribute('aria-busy') ?? null

const sortOf = (header: string): string | null => screen.getByRole('columnheader', { name: header }).getAttribute('aria-sort')

// The focused control is the same node before, while the reload is in flight, and after it resolves.
describe('a control the user activated survives the reload it triggers', () => {
  test.each([
    ['Agent jobs', 'Duration', 'full-jobs'],
    ['Breakdown', 'Agent time', 'full-breakdown'],
  ])('%s: the %s header keeps focus through two activations', async (_view, header, fixtureName) => {
    if (fixtureName === 'full-jobs') await openJobs()
    else await loadRunA()
    const button = screen.getByRole('button', { name: header })
    expect(sortOf(header)).toBe('none')

    expect(contentBusy()).toBe('false')

    activate(button)
    expect(button.isConnected).toBe(true)
    expect(button).toHaveFocus()
    expect(sortOf(header)).toBe('descending')
    expect(contentBusy()).toBe('true')
    await resolveLast(fixtureName)
    expect(button.isConnected).toBe(true)
    expect(button).toHaveFocus()
    expect(contentBusy()).toBe('false')

    activate(button)
    expect(button).toHaveFocus()
    expect(sortOf(header)).toBe('ascending')
    expect(contentBusy()).toBe('true')
    await resolveLast(fixtureName)
    expect(button).toHaveFocus()
    expect(contentBusy()).toBe('false')
  })

  test('the Next jobs button keeps focus while the next page loads', async () => {
    await openJobs()
    const next = screen.getByRole('button', { name: 'Next jobs' })
    activate(next)
    expect(next.isConnected).toBe(true)
    expect(next).toHaveFocus()
    expect(screen.getByRole('table', { name: 'Agent jobs' })).toBeInTheDocument()
    expect(contentBusy()).toBe('true')
    await resolveLast('full-jobs')
    expect(next).toHaveFocus()
    expect(contentBusy()).toBe('false')
  })

  test('the range and pager keep describing the visible rows while the next page loads', async () => {
    await openJobs()
    const next = screen.getByRole('button', { name: 'Next jobs' })
    expect(screen.getByText('1–4 of 7 jobs')).toBeInTheDocument()
    activate(next)
    expect(screen.getByText('1–4 of 7 jobs')).toBeInTheDocument()
    expect(screen.getByText('1 / 2')).toBeInTheDocument()
    expect(next).toHaveAttribute('aria-disabled', 'true')
    await resolveLast('full-jobs')
    expect(screen.getByText('2 / 2')).toBeInTheDocument()
    expect(next).toHaveAttribute('aria-disabled', 'false')
  })

  test('a view switch shows loading, not the empty state, until the new view answers', async () => {
    await loadRunA()
    fireEvent.click(screen.getByRole('button', { name: 'Agent jobs' }))
    expect(screen.queryByText('No usage yet')).toBeNull()
    expect(screen.queryByRole('table')).toBeNull()
  })
})
