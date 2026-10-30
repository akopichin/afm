import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { AssistantMentionMenu, MentionMenu } from './AssistantMentionMenu'
import { buildMeshMentionGroups } from '../feed-workspace/mesh-recipients'
import type { AssistantProfile } from '../../types/assistant-profile'

const PROFILES: AssistantProfile[] = [
  { id: 'frontendDeveloper', title: 'Frontend reviewer', command: 'claude', available: true, unavailableReason: '' },
  { id: 'ghost', title: 'Ghost', command: 'claude', available: false, unavailableReason: 'no verifiable read-only policy' },
]

describe('AssistantMentionMenu', () => {
  it('shows participant initials, role, scope and finished history beside its handle', () => {
    const groups = buildMeshMentionGroups([
      { id: 'run:qa', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA reviewer', reachable: true },
      { id: 'helper:old', kind: 'helper', scope: 'stage', stage_id: 'impl', alias: 'old', display_name: 'Old helper', reachable: false, activity: 'done' },
    ], [], 'impl')
    render(<MentionMenu groups={groups} highlightIndex={0} onHover={vi.fn()} onSelect={vi.fn()} ariaLabel="Mention a participant" />)
    expect(screen.getByText('Mention a participant · ↑ ↓ to choose · Enter to add')).toBeInTheDocument()
    expect(screen.getByText('Run agent · Whole run')).toBeInTheDocument()
    expect(screen.getByText('Helper · Finished · history only')).toBeInTheDocument()
    expect(screen.getByText('@qa').closest('button')?.querySelector('.mesh-avatar')).toHaveTextContent('QA')
    expect(screen.getByText('@old').closest('[role="option"]')).toHaveClass('is-unavailable')
    expect(screen.getByText('@old').closest('button')).toBeDisabled()
  })

  it('anchors above the composer and stays inside the viewport after resize', () => {
    const bounds = vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      return { top: 600, left: 300, width: 500, height: this.matches('[role="listbox"]') ? 200 : 100 } as DOMRect
    })
    render(<div className="chat-composer-row"><AssistantMentionMenu profiles={PROFILES} highlightIndex={0} onHover={vi.fn()} onSelect={vi.fn()} /></div>)
    const menu = screen.getByRole('listbox')
    expect(menu.style.top).toBe('391px')
    expect(menu.style.left).toBe('300px')
    expect(menu.style.width).toBe('360px')
    const oldWidth = window.innerWidth
    Object.defineProperty(window, 'innerWidth', { value: 320, configurable: true })
    fireEvent(window, new Event('resize'))
    expect(menu.style.width).toBe('296px')
    expect(menu.style.left).toBe('12px')
    Object.defineProperty(window, 'innerWidth', { value: oldWidth, configurable: true })
    bounds.mockRestore()
  })

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
