import { render } from '@testing-library/react'
import { describe, it, expect } from 'vitest'
import { RunMetrics } from './RunMetrics'

describe('RunMetrics', () => {
  it('formats started clock and mm:ss durations', () => {
    render(<RunMetrics startedAt="2026-07-29T10:00:00.000Z" elapsedMs={65_000} idleMs={5_000} backoffMs={0} />)
    // Started — локальные часы; проверяем формат HH:MM:SS без привязки к TZ.
    expect(document.getElementById('started-at')?.textContent).toMatch(/^\d{2}:\d{2}:\d{2}$/)
    expect(document.getElementById('elapsed')).toHaveTextContent('01:05')
    expect(document.getElementById('idle')).toHaveTextContent('00:05')
    expect(document.getElementById('backoff')).toHaveTextContent('00:00')
  })

  it('uses h:mm:ss once past an hour', () => {
    render(<RunMetrics startedAt="2026-07-29T10:00:00.000Z" elapsedMs={3_661_000} idleMs={0} backoffMs={0} />)
    expect(document.getElementById('elapsed')).toHaveTextContent('1:01:01')
  })

  it('shows placeholders when the run has not started', () => {
    render(<RunMetrics startedAt="" elapsedMs={0} idleMs={0} backoffMs={0} />)
    expect(document.getElementById('started-at')).toHaveTextContent('--')
    expect(document.getElementById('elapsed')).toHaveTextContent('--')
    expect(document.getElementById('idle')).toHaveTextContent('--')
    expect(document.getElementById('backoff')).toHaveTextContent('--')
  })
})
