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

  it('clicking a profile selects it once; mousedown alone only keeps focus in the textarea', () => {
    const onSelect = vi.fn()
    render(<AssistantMentionMenu profiles={PROFILES} highlightIndex={0} onHover={vi.fn()} onSelect={onSelect} />)
    const option = screen.getByText('@frontendDeveloper').closest('button') as HTMLButtonElement
    expect(fireEvent.mouseDown(option)).toBe(false) // default prevented → the textarea keeps focus
    expect(onSelect).not.toHaveBeenCalled()
    fireEvent.click(option)
    expect(onSelect).toHaveBeenCalledTimes(1)
    expect(onSelect).toHaveBeenCalledWith(PROFILES[0])
  })

  it('a keyboard-focused option is selected with Enter (the browser turns it into a click)', () => {
    const onSelect = vi.fn()
    render(<AssistantMentionMenu profiles={PROFILES} highlightIndex={0} onHover={vi.fn()} onSelect={onSelect} />)
    const option = screen.getByText('@frontendDeveloper').closest('button') as HTMLButtonElement
    option.focus()
    fireEvent.click(option) // what Enter/Space on a focused <button> dispatches
    expect(onSelect).toHaveBeenCalledWith(PROFILES[0])
  })
})
