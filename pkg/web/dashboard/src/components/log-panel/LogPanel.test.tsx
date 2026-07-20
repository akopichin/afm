import { render } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import type { LogEntry } from '../../types'
import { LogPanel } from './LogPanel'

describe('LogPanel', () => {
  test('renders entries in chronological order without re-sorting', () => {
    const entries: LogEntry[] = [
      { timestamp: '10:00:00', message: 'first line', level: 'info' },
      { timestamp: '09:00:00', message: 'second line', level: 'error' },
      { timestamp: '11:00:00', message: 'third line', level: 'debug' },
    ]

    const { container } = render(<LogPanel entries={entries} />)

    const content = container.querySelector('#log-content')
    expect(content).not.toBeNull()
    expect(content?.textContent).toBe('first line\nsecond line\nthird line')
  })

  test('shows empty-state and hides log content when entries is empty', () => {
    const { container } = render(<LogPanel entries={[]} />)

    const content = container.querySelector('#log-content')
    const empty = container.querySelector('#log-empty')

    expect(content).toHaveClass('hidden')
    expect(empty).not.toHaveClass('hidden')
    expect(empty).toHaveTextContent('Log is empty')
  })
})
