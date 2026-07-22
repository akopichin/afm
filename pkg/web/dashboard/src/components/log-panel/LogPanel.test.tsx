import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import type { LogEntry } from '../../types'
import { MaximizeProvider } from '../layout/Maximizable'
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

  test('has a maximize button (like plan/dialog) that moves the panel into a fullscreen overlay', () => {
    const entries: LogEntry[] = [{ timestamp: '10:00:00', message: 'line', level: 'info' }]
    const { container } = render(
      <MaximizeProvider>
        <LogPanel entries={entries} />
      </MaximizeProvider>,
    )

    expect(container.querySelector('.maximize-overlay')).toBeNull()

    const button = screen.getByRole('button', { name: 'Развернуть' })
    fireEvent.click(button)

    expect(document.querySelector('.maximize-overlay')).not.toBeNull()
    expect(document.querySelector('.maximize-overlay #log-content')).not.toBeNull()
  })

  test('does not truncate log content in the DOM, in either compact or maximized mode', () => {
    const longLine = 'x'.repeat(2000)
    const entries: LogEntry[] = Array.from({ length: 200 }, (_, i) => ({
      timestamp: '10:00:00',
      message: `${longLine}-${i}`,
      level: 'info',
    }))

    const { container } = render(
      <MaximizeProvider>
        <LogPanel entries={entries} />
      </MaximizeProvider>,
    )

    const compactContent = container.querySelector('#log-content')
    expect(compactContent?.textContent).toContain(`${longLine}-0`)
    expect(compactContent?.textContent).toContain(`${longLine}-199`)

    fireEvent.click(screen.getByRole('button', { name: 'Развернуть' }))

    const maximizedContent = document.querySelector('.maximize-overlay #log-content')
    expect(maximizedContent?.textContent).toContain(`${longLine}-0`)
    expect(maximizedContent?.textContent).toContain(`${longLine}-199`)
  })
})
