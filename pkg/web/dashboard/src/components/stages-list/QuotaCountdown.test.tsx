import { act, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { MessageTimeZoneProvider } from '../../lib/message-time-zone'
import type { StageQuota } from '../../types'
import { QuotaCountdown } from './QuotaCountdown'

const NOW = new Date('2026-10-23T16:50:00Z')

function makeQuota(overrides: Partial<StageQuota> = {}): StageQuota {
  return {
    provider: 'claude', window: 'five_hour', source: 'structured',
    until: '2026-10-23T17:02:06Z', resetAt: '2026-10-23T17:02:00Z', pauseReason: '', ...overrides,
  }
}

function renderCountdown(quota: StageQuota, props: { live?: boolean; compact?: boolean; timeZone?: string } = {}) {
  const { timeZone = 'UTC', ...rest } = props
  const ui = (q: StageQuota, live: boolean | undefined) => (
    <MessageTimeZoneProvider timeZone={timeZone} utcOffsetMinutes={0}>
      <QuotaCountdown quota={q} live={live} compact={rest.compact} />
    </MessageTimeZoneProvider>
  )
  const view = render(ui(quota, rest.live))
  return { ...view, rerenderWith: (q: StageQuota, live?: boolean) => view.rerender(ui(q, live ?? rest.live)) }
}

describe('QuotaCountdown', () => {
  beforeEach(() => {
    vi.useFakeTimers({ now: NOW })
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  test('shows the absolute until time in the host zone and a ticking countdown', () => {
    const { container } = renderCountdown(makeQuota())
    expect(screen.getByText('Usage limit')).toBeInTheDocument()
    expect(screen.getByText('claude · five_hour')).toBeInTheDocument()
    const time = container.querySelector('time') as HTMLTimeElement
    expect(time).toHaveAttribute('datetime', '2026-10-23T17:02:06.000Z')
    expect(time).toHaveTextContent('Resumes at Oct 23, 17:02')
    const countdown = container.querySelector('.quota-countdown') as HTMLElement
    expect(countdown).toHaveTextContent('12m 06s')
    act(() => { vi.advanceTimersByTime(3000) })
    expect(countdown).toHaveTextContent('12m 03s')
  })

  test('renders the absolute time in the application host zone, not the browser zone', () => {
    const { container } = renderCountdown(makeQuota(), { timeZone: 'Asia/Tokyo' })
    expect(container.querySelector('time')).toHaveTextContent('Resumes at Oct 24, 02:02')
  })

  test('the ticking text is aria-hidden and the static label stays readable', () => {
    const { container } = renderCountdown(makeQuota())
    expect(container.querySelector('.quota-countdown')).toHaveAttribute('aria-hidden', 'true')
    expect(container.querySelector('time')).not.toHaveAttribute('aria-hidden')
    expect(container.querySelector('time')).not.toHaveAttribute('aria-live')
  })

  test('freezes while the status poll is stale and catches up when it is fresh again', () => {
    const { container, rerenderWith } = renderCountdown(makeQuota(), { live: true })
    const countdown = container.querySelector('.quota-countdown') as HTMLElement
    act(() => { vi.advanceTimersByTime(2000) })
    expect(countdown).toHaveTextContent('12m 04s')
    rerenderWith(makeQuota(), false)
    act(() => { vi.advanceTimersByTime(30_000) })
    expect(countdown).toHaveTextContent('12m 04s')
    rerenderWith(makeQuota(), true)
    expect(countdown).toHaveTextContent('11m 34s')
  })

  test('after zero it says "Resuming…" instead of a negative or zero counter', () => {
    const { container } = renderCountdown(makeQuota({ until: '2026-10-23T16:50:02Z' }))
    act(() => { vi.advanceTimersByTime(2000) })
    expect(container.querySelector('.quota-countdown')).toHaveTextContent('Resuming…')
  })

  test('a reload (fresh mount with a past until) shows "Resuming…" right away', () => {
    const { container } = renderCountdown(makeQuota({ until: '2026-10-23T16:00:00Z' }))
    expect(container.querySelector('.quota-countdown')).toHaveTextContent('Resuming…')
  })

  test('source=probe says "Next check", not "Resumes"', () => {
    const { container } = renderCountdown(makeQuota({ source: 'probe', window: '', resetAt: '', until: '2026-10-23T17:05:00Z' }))
    expect(container.querySelector('time')).toHaveTextContent('Next check at Oct 23, 17:05')
    expect(container.textContent).not.toContain('Resumes')
  })

  test('a quota with no known time (no_reset) shows no countdown and no <time>', () => {
    const { container } = renderCountdown(makeQuota({ source: 'text', until: '', resetAt: '', pauseReason: 'no_reset' }))
    expect(container.querySelector('.quota-countdown')).toBeNull()
    expect(container.querySelector('time')).toBeNull()
    expect(screen.getByText('Quota exhausted')).toBeInTheDocument()
  })

  test('the countdown targets until only: a held quota with just resetAt shows the reason, no countdown, no "Resumes"', () => {
    const { container } = renderCountdown(makeQuota({ until: '', resetAt: '2026-10-23T22:00:00Z', pauseReason: 'exceeds_max_wait' }))
    expect(container.querySelector('.quota-countdown')).toBeNull()
    expect(container.querySelector('time')).toBeNull()
    expect(container.textContent).not.toContain('Resumes')
    expect(container.textContent).toContain('later than the maximum wait, so the task was paused')
  })

  test('compact badge of a held quota has no ticking countdown either', () => {
    const { container } = renderCountdown(makeQuota({ until: '', pauseReason: 'max_waits' }), { compact: true })
    expect(container.querySelector('.quota-countdown')).toBeNull()
  })

  test('without until (and no pause_reason) resetAt is not a countdown target', () => {
    const { container } = renderCountdown(makeQuota({ until: '' }))
    expect(container.querySelector('.quota-countdown')).toBeNull()
    expect(container.querySelector('time')).toBeNull()
  })

  test('the interval stops once the countdown reached zero', () => {
    renderCountdown(makeQuota({ until: '2026-10-23T16:50:01Z' }))
    act(() => { vi.advanceTimersByTime(5000) })
    expect(vi.getTimerCount()).toBe(0)
  })

  test('compact variant: icon + countdown are decorative, the title and the screen-reader text carry the label', () => {
    const { container } = renderCountdown(makeQuota(), { compact: true })
    const badge = container.querySelector('.quota-badge') as HTMLElement
    expect(badge).toHaveAttribute('title', 'Usage limit (claude, five_hour): resumes at Oct 23, 17:02')
    expect(badge.querySelector('.quota-countdown')).toHaveAttribute('aria-hidden', 'true')
    expect(badge.querySelector('.quota-sr')).toHaveTextContent('Usage limit (claude, five_hour): resumes at Oct 23, 17:02')
    expect(container.querySelector('time')).toHaveAttribute('datetime', '2026-10-23T17:02:06.000Z')
  })
})
