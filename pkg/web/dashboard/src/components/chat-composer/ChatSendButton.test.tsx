import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { ChatSendButton } from './ChatSendButton'

describe('ChatSendButton', () => {
  test('is an icon-only button named by its label', () => {
    render(<ChatSendButton label="Apply Revise" disabled={false} onClick={() => {}} />)
    const btn = screen.getByRole('button', { name: 'Apply Revise' })
    expect(btn).toHaveClass('chat-send')
    expect(btn).toHaveAttribute('title', 'Apply Revise')
    expect(btn.textContent).toBe('')
  })

  test('calls onClick unless disabled', () => {
    const onClick = vi.fn()
    const { rerender } = render(<ChatSendButton label="Send" disabled={false} onClick={onClick} />)
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onClick).toHaveBeenCalledTimes(1)
    rerender(<ChatSendButton label="Send" disabled onClick={onClick} />)
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  test('paused tints the button', () => {
    render(<ChatSendButton label="Send" disabled={false} onClick={() => {}} paused />)
    expect(screen.getByRole('button', { name: 'Send' })).toHaveClass('paused')
  })

  test('while sending it is disabled and named "Sending…"', () => {
    const onClick = vi.fn()
    render(<ChatSendButton label="Send" disabled={false} onClick={onClick} sending />)
    const btn = screen.getByRole('button', { name: 'Sending…' })
    expect(btn).toBeDisabled()
    expect(btn).toHaveAttribute('aria-busy', 'true')
  })
})
