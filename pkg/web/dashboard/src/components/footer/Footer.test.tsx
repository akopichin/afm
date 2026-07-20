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
      { id: 's1', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false },
      { id: 's2', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false },
    ]

    render(<Footer stages={stages} startedAt="2026-07-10T10:00:00Z" elapsedMs={65000} />)

    expect(screen.getByText('1 / 2')).toBeInTheDocument()
    expect(screen.getByText('01:05')).toBeInTheDocument()
  })

  test('shows placeholder elapsed and started-at when startedAt is empty', () => {
    render(<Footer stages={[]} startedAt="" elapsedMs={0} />)

    // При пустом startedAt оба поля (started-at и elapsed) показывают плейсхолдер '--'.
    expect(document.getElementById('elapsed')).toHaveTextContent('--')
    expect(document.getElementById('started-at')).toHaveTextContent('--')
  })

  test('shows placeholder started-at when startedAt is not a valid date', () => {
    render(<Footer stages={[]} startedAt="not-a-date" elapsedMs={0} />)

    expect(document.getElementById('started-at')).toHaveTextContent('--')
  })

  test('formats elapsed with hours when duration is 1 hour or more', () => {
    render(<Footer stages={[]} startedAt="2026-07-10T10:00:00Z" elapsedMs={3665000} />)

    expect(document.getElementById('elapsed')).toHaveTextContent('1:01:05')
  })

  test('shows 0 / 0 progress without dividing by zero when stages is empty', () => {
    render(<Footer stages={[]} startedAt="2026-07-10T10:00:00Z" elapsedMs={0} />)

    expect(screen.getByText('0 / 0')).toBeInTheDocument()
    expect(document.getElementById('progress-fill')).toHaveStyle({ width: '0%' })
  })
})
