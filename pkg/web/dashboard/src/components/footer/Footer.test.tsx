import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, test } from 'vitest'
import type { Stage } from '../../types'
import { Footer } from './Footer'

describe('Footer', () => {
  beforeEach(() => {
    document.documentElement.removeAttribute('data-theme')
    window.localStorage.clear()
  })

  test('renders done/total progress and formatted elapsed', () => {
    const stages: Stage[] = [
      { id: 's1', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false },
      { id: 's2', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false },
    ]

    render(
      <Footer stages={stages} startedAt="2026-07-10T10:00:00Z" elapsedMs={65000} idleMs={0} backoffMs={0} />,
    )

    expect(screen.getByText('1 / 2')).toBeInTheDocument()
    expect(screen.getByText('01:05')).toBeInTheDocument()
  })

  test('shows placeholder elapsed and started-at when startedAt is empty', () => {
    render(<Footer stages={[]} startedAt="" elapsedMs={0} idleMs={0} backoffMs={0} />)

    // При пустом startedAt все поля (started-at, elapsed, idle, backoff) показывают плейсхолдер '--'.
    expect(document.getElementById('elapsed')).toHaveTextContent('--')
    expect(document.getElementById('started-at')).toHaveTextContent('--')
  })

  test('shows placeholder started-at when startedAt is not a valid date', () => {
    render(<Footer stages={[]} startedAt="not-a-date" elapsedMs={0} idleMs={0} backoffMs={0} />)

    expect(document.getElementById('started-at')).toHaveTextContent('--')
  })

  test('formats elapsed with hours when duration is 1 hour or more', () => {
    render(
      <Footer stages={[]} startedAt="2026-07-10T10:00:00Z" elapsedMs={3665000} idleMs={0} backoffMs={0} />,
    )

    expect(document.getElementById('elapsed')).toHaveTextContent('1:01:05')
  })

  test('shows 0 / 0 progress without dividing by zero when stages is empty', () => {
    render(<Footer stages={[]} startedAt="2026-07-10T10:00:00Z" elapsedMs={0} idleMs={0} backoffMs={0} />)

    expect(screen.getByText('0 / 0')).toBeInTheDocument()
    expect(document.getElementById('progress-fill')).toHaveStyle({ width: '0%' })
  })

  test('renders Idle and Backoff next to Elapsed', () => {
    render(
      <Footer stages={[]} startedAt="2026-07-10T10:00:00Z" elapsedMs={65000} idleMs={5000} backoffMs={125000} />,
    )

    expect(document.getElementById('idle')).toHaveTextContent('00:05')
    expect(document.getElementById('backoff')).toHaveTextContent('02:05')
  })

  test('shows 0:00 for Idle/Backoff once the run has started, even when both are zero', () => {
    render(<Footer stages={[]} startedAt="2026-07-10T10:00:00Z" elapsedMs={0} idleMs={0} backoffMs={0} />)

    expect(document.getElementById('idle')).toHaveTextContent('00:00')
    expect(document.getElementById('backoff')).toHaveTextContent('00:00')
  })

  test('shows placeholder Idle/Backoff when startedAt is empty (run has not started)', () => {
    render(<Footer stages={[]} startedAt="" elapsedMs={0} idleMs={0} backoffMs={0} />)

    expect(document.getElementById('idle')).toHaveTextContent('--')
    expect(document.getElementById('backoff')).toHaveTextContent('--')
  })
})
