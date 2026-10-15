import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { AssistantMentionMenu } from './AssistantMentionMenu'
import type { AssistantProfile } from '../../types/assistant-profile'

const PROFILES: AssistantProfile[] = [
  { id: 'frontendDeveloper', title: 'Frontend reviewer', command: 'claude', available: true, unavailableReason: '' },
  { id: 'ghost', title: 'Ghost', command: 'claude', available: false, unavailableReason: 'no verifiable read-only policy' },
]

describe('AssistantMentionMenu', () => {
  it('renders nothing for an empty profile list', () => {
    const { container } = render(
      <AssistantMentionMenu profiles={[]} highlightIndex={0} onHover={vi.fn()} onSelect={vi.fn()} />,
    )
    expect(container.firstChild).toBeNull()
  })

  it('renders every profile with id and title, marking the highlighted one', () => {
    render(<AssistantMentionMenu profiles={PROFILES} highlightIndex={1} onHover={vi.fn()} onSelect={vi.fn()} />)
    const options = screen.getAllByRole('option')
    expect(options).toHaveLength(2)
    expect(options[0]).toHaveAttribute('aria-selected', 'false')
    expect(options[1]).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByText('@frontendDeveloper')).toBeInTheDocument()
    expect(screen.getByText('Frontend reviewer')).toBeInTheDocument()
  })

  it('shows an Unavailable marker for a role without a working provider, but does not hide it', () => {
    render(<AssistantMentionMenu profiles={PROFILES} highlightIndex={0} onHover={vi.fn()} onSelect={vi.fn()} />)
    expect(screen.getByText('Unavailable')).toBeInTheDocument()
    expect(screen.getByText('@ghost')).toBeInTheDocument()
  })

  it('selecting a profile calls onSelect with it', () => {
    const onSelect = vi.fn()
    render(<AssistantMentionMenu profiles={PROFILES} highlightIndex={0} onHover={vi.fn()} onSelect={onSelect} />)
    fireEvent.mouseDown(screen.getByText('@frontendDeveloper'))
    expect(onSelect).toHaveBeenCalledWith(PROFILES[0])
  })
})
