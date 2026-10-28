import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { AssistantActivityStatus } from './AssistantActivityStatus'
import type { AssistantActivityEntry } from '../../types/assistant-activity'

const NOW = Date.UTC(2026, 9, 27, 12, 0, 0)

function entry(over: Partial<AssistantActivityEntry> = {}): AssistantActivityEntry {
  return {
    eventId: 'e1', taskId: 't1', participantId: '', turn: 1,
    occurredAt: new Date(NOW - 18_000).toISOString(), tool: 'WebFetch', detail: 'https://example.com',
    truncated: false, cursor: undefined, ...over,
  }
}

const base = { last: entry(), connected: true, refreshFailing: false, running: true, terminal: false, onRetry: () => {} }

describe('AssistantActivityStatus', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(NOW)
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  test('shows the last tool and its age, and keeps counting', () => {
    render(<AssistantActivityStatus {...base} />)
    expect(screen.getByText('Last action: WebFetch · 18s ago')).toBeInTheDocument()

    act(() => {
      vi.advanceTimersByTime(5_000)
    })
    expect(screen.getByText('Last action: WebFetch · 23s ago')).toBeInTheDocument()
  })

  test('a message is described as a message, not as a tool named text', () => {
    render(<AssistantActivityStatus {...base} last={entry({ tool: 'text', detail: 'hello' })} />)
    expect(screen.getByText('Last action: message · 18s ago')).toBeInTheDocument()
  })

  test('renders nothing before the first action', () => {
    const { container } = render(<AssistantActivityStatus {...base} last={null} />)
    expect(container).toBeEmptyDOMElement()
  })

  test('a long quiet spell of a connected running helper is "no new actions", never "stuck"', () => {
    render(<AssistantActivityStatus {...base} last={entry({ occurredAt: new Date(NOW - 200_000).toISOString() })} />)
    expect(screen.getByText('No new actions for 3m')).toBeInTheDocument()
    expect(screen.queryByText(/stuck|hung|frozen/i)).toBeNull()
  })

  test('a short quiet spell says nothing about silence', () => {
    render(<AssistantActivityStatus {...base} />)
    expect(screen.queryByText(/no new actions/i)).toBeNull()
  })

  test('a paused helper is not described as silent', () => {
    render(<AssistantActivityStatus {...base} running={false} last={entry({ occurredAt: new Date(NOW - 600_000).toISOString() })} />)
    expect(screen.queryByText(/no new actions/i)).toBeNull()
  })

  test('a lost connection is announced and replaces the silence wording', () => {
    render(<AssistantActivityStatus {...base} connected={false} last={entry({ occurredAt: new Date(NOW - 600_000).toISOString() })} />)
    expect(screen.getByRole('status')).toHaveTextContent(/live updates paused/i)
    expect(screen.getByRole('status')).toHaveTextContent(/reconnecting/i)
    expect(screen.queryByText(/no new actions/i)).toBeNull()
  })

  test('a failing refresh keeps the loaded activity and says so', () => {
    render(<AssistantActivityStatus {...base} refreshFailing />)
    expect(screen.getByRole('status')).toHaveTextContent(/could not refresh activity/i)
    expect(screen.getByText(/Last action: WebFetch/)).toBeInTheDocument()
    expect(screen.queryByText(/no new actions/i)).toBeNull()
  })

  test('a running helper says it is retrying and has no Retry button', () => {
    render(<AssistantActivityStatus {...base} refreshFailing />)
    expect(screen.getByRole('status')).toHaveTextContent(/retrying/i)
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
  })

  test('a finished helper whose refresh failed offers Retry instead of promising a retry', () => {
    const onRetry = vi.fn()
    render(<AssistantActivityStatus {...base} running={false} terminal refreshFailing onRetry={onRetry} />)
    expect(screen.getByRole('status')).toHaveTextContent('Could not refresh activity. Showing the activity loaded so far.')
    expect(screen.getByRole('status')).not.toHaveTextContent(/retrying/i)
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  test('a finished helper shows when it acted, as a clock time, and stops ticking', () => {
    render(<AssistantActivityStatus {...base} running={false} terminal />)
    const line = screen.getByText(/^Last action: WebFetch · /)
    expect(line.textContent).not.toMatch(/ago/)
    const before = line.textContent
    act(() => {
      vi.advanceTimersByTime(60_000)
    })
    expect(screen.getByText(/^Last action: WebFetch · /).textContent).toBe(before)
  })

  test('a finished helper does not announce a lost connection', () => {
    render(<AssistantActivityStatus {...base} running={false} terminal connected={false} />)
    expect(screen.queryByRole('status')).toBeNull()
  })

  test('an invalid timestamp still names the action without a time', () => {
    render(<AssistantActivityStatus {...base} last={entry({ occurredAt: 'not-a-date' })} />)
    expect(screen.getByText('Last action: WebFetch')).toBeInTheDocument()
  })

  test('a clock running ahead of the server never shows a negative age', () => {
    render(<AssistantActivityStatus {...base} last={entry({ occurredAt: new Date(NOW + 30_000).toISOString() })} />)
    expect(screen.getByText('Last action: WebFetch · 0s ago')).toBeInTheDocument()
  })
})
