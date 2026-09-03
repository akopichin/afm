import { render, screen, fireEvent } from '@testing-library/react'
import type { ReactElement } from 'react'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { FlowHeader } from './FlowHeader'
import { FileBrowserProvider } from '../file-browser'
import { FilesApiMock } from '../file-browser/test-support'

function renderWithProvider(ui: ReactElement) {
  return render(<FileBrowserProvider flowName="flow1" startedAt="t1">{ui}</FileBrowserProvider>)
}

describe('FlowHeader', () => {
  beforeEach(() => {
    document.documentElement.removeAttribute('data-theme')
    window.localStorage.clear()
  })

  test('shows LINK when connected and OFFLINE when disconnected', () => {
    const { rerender } = render(<FlowHeader flowName="demo" connected={true} />)

    expect(screen.getByText('LINK')).toBeInTheDocument()
    expect(screen.getByText('LINK')).toHaveClass('connected')

    rerender(<FlowHeader flowName="demo" connected={false} />)

    expect(screen.getByText('OFFLINE')).toBeInTheDocument()
    expect(screen.getByText('OFFLINE')).toHaveClass('disconnected')
  })

  test('shows attention indicator when attention is true', () => {
    render(<FlowHeader flowName="demo" connected={true} attention={true} />)

    expect(screen.getByLabelText('Action needed')).toBeInTheDocument()
  })

  test('hides attention indicator when attention is false or omitted', () => {
    const { rerender } = render(<FlowHeader flowName="demo" connected={true} attention={false} />)

    expect(screen.queryByLabelText('Action needed')).not.toBeInTheDocument()

    rerender(<FlowHeader flowName="demo" connected={true} />)

    expect(screen.queryByLabelText('Action needed')).not.toBeInTheDocument()
  })

  test('renders the passed description under the flow name', () => {
    render(<FlowHeader flowName="demo" connected={true} description="Проект X: очистка изображений" />)

    const description = screen.getByText('Проект X: очистка изображений')
    expect(description).toBeInTheDocument()
    expect(description).toHaveAttribute('id', 'flow-description')
  })

  test('hides the description block when omitted or empty', () => {
    const { container, rerender } = render(<FlowHeader flowName="demo" connected={true} />)
    expect(container.querySelector('#flow-description')).not.toBeInTheDocument()

    rerender(<FlowHeader flowName="demo" connected={true} description="" />)
    expect(container.querySelector('#flow-description')).not.toBeInTheDocument()

    rerender(<FlowHeader flowName="demo" connected={true} description="   " />)
    expect(container.querySelector('#flow-description')).not.toBeInTheDocument()
  })

  test('toggles theme mode on click', () => {
    document.documentElement.dataset.theme = 'dark'
    render(<FlowHeader flowName="demo" connected={true} />)

    const button = screen.getByRole('button', { name: /light mode/i })
    fireEvent.click(button)

    expect(document.documentElement.dataset.theme).toBe('light')
  })

  test('hides notifications button when unsupported', () => {
    render(<FlowHeader flowName="demo" connected={true} notificationsPermission="unsupported" />)
    expect(screen.queryByLabelText(/notifications/i)).not.toBeInTheDocument()
  })

  test('shows disabled notifications button with tooltip when denied', () => {
    render(<FlowHeader flowName="demo" connected={true} notificationsPermission="denied" />)
    const button = screen.getByLabelText('Enable desktop notifications')
    expect(button).toBeDisabled()
    expect(button).toHaveAttribute('title', 'Notifications blocked in browser settings')
  })

  test('clicking the off notifications button calls onRequestEnableNotifications', () => {
    const onRequestEnable = vi.fn()
    render(
      <FlowHeader
        flowName="demo"
        connected={true}
        notificationsPermission="default"
        notificationsEnabled={false}
        onRequestEnableNotifications={onRequestEnable}
      />,
    )
    fireEvent.click(screen.getByLabelText('Enable desktop notifications'))
    expect(onRequestEnable).toHaveBeenCalled()
  })

  test('clicking the on notifications button calls onDisableNotifications', () => {
    const onDisable = vi.fn()
    render(
      <FlowHeader
        flowName="demo"
        connected={true}
        notificationsPermission="granted"
        notificationsEnabled={true}
        onDisableNotifications={onDisable}
      />,
    )
    fireEvent.click(screen.getByLabelText('Disable desktop notifications'))
    expect(onDisable).toHaveBeenCalled()
  })

  test('hides the file browser button when capabilities.fileBrowser is false or omitted', () => {
    renderWithProvider(<FlowHeader flowName="demo" connected={true} capabilities={{ fileBrowser: false }} />)
    expect(screen.queryByLabelText('Open project files')).not.toBeInTheDocument()

    renderWithProvider(<FlowHeader flowName="demo" connected={true} />)
    expect(screen.queryByLabelText('Open project files')).not.toBeInTheDocument()
  })

  test('shows the file browser button when capabilities.fileBrowser is true, and opens the browser on click', async () => {
    new FilesApiMock().install()
    renderWithProvider(<FlowHeader flowName="demo" connected={true} capabilities={{ fileBrowser: true }} />)

    const button = screen.getByLabelText('Open project files')
    expect(button).toBeInTheDocument()

    fireEvent.click(button)
    expect(await screen.findByRole('dialog', { name: /browse project files/i })).toBeInTheDocument()
  })
})
