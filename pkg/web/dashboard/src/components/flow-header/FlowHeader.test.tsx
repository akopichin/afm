import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { FlowHeader } from './FlowHeader'

describe('FlowHeader', () => {
  test('shows LINK when connected and OFFLINE when disconnected', () => {
    const { rerender } = render(<FlowHeader flowName="demo" connected={true} />)

    expect(screen.getByText('LINK')).toBeInTheDocument()
    expect(screen.getByText('LINK')).toHaveClass('connected')

    rerender(<FlowHeader flowName="demo" connected={false} />)

    expect(screen.getByText('OFFLINE')).toBeInTheDocument()
    expect(screen.getByText('OFFLINE')).toHaveClass('disconnected')
  })
})
