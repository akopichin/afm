import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ReviewBanner } from './ReviewBanner'

describe('ReviewBanner', () => {
  it('renders in paused state with Send and Cancel', () => {
    render(<ReviewBanner flowPauseState="paused" noteCount={3} onSend={() => {}} onCancel={() => {}} />)
    expect(screen.getByText(/Review mode/i)).toBeInTheDocument()
    expect(screen.getByText(/3/)).toBeInTheDocument()
    expect(screen.getByText('Send notes')).toBeInTheDocument()
    expect(screen.getByText('Cancel')).toBeInTheDocument()
  })

  it('calls onSend/onCancel when the buttons are clicked', () => {
    const onSend = vi.fn()
    const onCancel = vi.fn()
    render(<ReviewBanner flowPauseState="paused" noteCount={1} onSend={onSend} onCancel={onCancel} />)

    screen.getByText('Send notes').click()
    screen.getByText('Cancel').click()

    expect(onSend).toHaveBeenCalledTimes(1)
    expect(onCancel).toHaveBeenCalledTimes(1)
  })

  it('renders "Resuming…" with no buttons when resuming', () => {
    render(<ReviewBanner flowPauseState="resuming" noteCount={0} onSend={() => {}} onCancel={() => {}} />)
    expect(screen.getByText(/Resuming/i)).toBeInTheDocument()
    expect(screen.queryByText('Send notes')).not.toBeInTheDocument()
    expect(screen.queryByText('Cancel')).not.toBeInTheDocument()
  })

  it('renders nothing when none', () => {
    const { container } = render(<ReviewBanner flowPauseState="none" noteCount={0} onSend={() => {}} onCancel={() => {}} />)
    expect(container).toBeEmptyDOMElement()
  })
})
