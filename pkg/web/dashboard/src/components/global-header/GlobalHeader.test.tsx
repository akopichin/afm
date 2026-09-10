import { render, screen, fireEvent } from '@testing-library/react'
import { afterEach, describe, it, expect, vi } from 'vitest'
import { GlobalHeader } from './GlobalHeader'

const baseProps = {
  flowName: 'demo',
  connected: false,
  startedAt: '2026-07-29T10:00:00.000Z',
  elapsedMs: 0,
  idleMs: 0,
  backoffMs: 0,
}

afterEach(() => {
  localStorage.clear()
  document.documentElement.dataset.theme = ''
})

describe('GlobalHeader', () => {
  it('renders flow name, optional description and the run metrics', () => {
    render(<GlobalHeader {...baseProps} description="Login flow" />)
    expect(document.getElementById('flow-name')).toHaveTextContent('demo')
    expect(document.getElementById('flow-description')).toHaveTextContent('Login flow')
    // RunMetrics встроен — метки метрик присутствуют.
    expect(document.getElementById('elapsed')).toBeInTheDocument()
  })

  it('omits the description node when blank', () => {
    render(<GlobalHeader {...baseProps} description="   " />)
    expect(document.getElementById('flow-description')).toBeNull()
  })

  it('shows connection state text', () => {
    const { rerender } = render(<GlobalHeader {...baseProps} connected={false} />)
    expect(screen.getByText('Offline')).toBeInTheDocument()
    rerender(<GlobalHeader {...baseProps} connected />)
    expect(screen.getByText('Agent online')).toBeInTheDocument()
  })

  it('switches theme via the segmented sun/moon control', () => {
    document.documentElement.dataset.theme = 'dark'
    render(<GlobalHeader {...baseProps} />)
    fireEvent.click(screen.getByRole('button', { name: 'Light mode' }))
    expect(document.documentElement.dataset.theme).toBe('light')
    fireEvent.click(screen.getByRole('button', { name: 'Dark mode' }))
    expect(document.documentElement.dataset.theme).toBe('dark')
  })

  it('renders the notifications control only when supported', () => {
    const { rerender } = render(<GlobalHeader {...baseProps} notificationsPermission="unsupported" />)
    expect(screen.queryByRole('button', { name: /notifications/i })).toBeNull()
    rerender(
      <GlobalHeader
        {...baseProps}
        notificationsPermission="default"
        onRequestEnableNotifications={vi.fn()}
      />,
    )
    expect(screen.getByRole('button', { name: /enable desktop notifications/i })).toBeInTheDocument()
  })

  it('renders the Files button only when the capability is on', () => {
    // capabilities.fileBrowser=false → кнопка не рендерится (и useFileBrowser не
    // вызывается вне провайдера — та же причина, что была у FlowHeader).
    render(<GlobalHeader {...baseProps} capabilities={{ fileBrowser: false }} />)
    expect(screen.queryByRole('button', { name: /open project files/i })).toBeNull()
  })
})
