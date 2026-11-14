import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { AttentionBanner } from './AttentionBanner'

describe('AttentionBanner', () => {
  test('a blocked required helper says helper, not assistant', () => {
    render(<AttentionBanner kind="assistants" />)
    expect(screen.getByText('A helper task needs your decision')).toBeInTheDocument()
    expect(screen.getByText(/^A required helper did not pass cleanly/)).toBeInTheDocument()
    expect(screen.queryByText(/assistant/i)).toBeNull()
  })

  test('draws a quiet line icon tinted by tone instead of a glowing glyph', () => {
    const { container, rerender } = render(<AttentionBanner kind="failed" />)
    expect(container.querySelector('.attention-banner')).toHaveAttribute('data-tone', 'danger')
    expect(container.querySelector('svg.attention-banner-icon')).toHaveAttribute('aria-hidden', 'true')
    rerender(<AttentionBanner kind="approval" />)
    expect(container.querySelector('.attention-banner')).toHaveAttribute('data-tone', 'attention')
  })

  test('a budget hold replaces the paused text, so Continue is not offered as a plain resume', () => {
    const { container } = render(<AttentionBanner kind="paused" budget={{ title: 'Stopped by budget', subtitle: 'The run budget is used up. Change the limit to continue.' }} />)
    expect(screen.getByText('Stopped by budget')).toBeInTheDocument()
    expect(screen.getByText(/The run budget is used up/)).toBeInTheDocument()
    expect(screen.queryByText('Stage is paused')).toBeNull()
    expect(container.querySelector('.attention-banner')).toHaveAttribute('data-kind', 'budget')
  })
})
