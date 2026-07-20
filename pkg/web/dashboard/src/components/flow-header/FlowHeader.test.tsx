import { render, screen, fireEvent } from '@testing-library/react'
import { beforeEach, describe, expect, test } from 'vitest'
import { FlowHeader } from './FlowHeader'

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

    expect(screen.getByLabelText('Нужно действие')).toBeInTheDocument()
  })

  test('hides attention indicator when attention is false or omitted', () => {
    const { rerender } = render(<FlowHeader flowName="demo" connected={true} attention={false} />)

    expect(screen.queryByLabelText('Нужно действие')).not.toBeInTheDocument()

    rerender(<FlowHeader flowName="demo" connected={true} />)

    expect(screen.queryByLabelText('Нужно действие')).not.toBeInTheDocument()
  })

  test('toggles theme mode on click', () => {
    document.documentElement.dataset.theme = 'dark'
    render(<FlowHeader flowName="demo" connected={true} />)

    const button = screen.getByRole('button', { name: /light mode/i })
    fireEvent.click(button)

    expect(document.documentElement.dataset.theme).toBe('light')
  })
})
