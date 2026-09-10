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

  it('toggles dark/light mode', () => {
    render(<GlobalHeader {...baseProps} />)
    const toggle = screen.getByRole('button', { name: /switch to (light|dark) mode/i })
    fireEvent.click(toggle)
    expect(['dark', 'light']).toContain(document.documentElement.dataset.theme)
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
