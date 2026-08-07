import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { AgentNoteModal } from './AgentNoteModal'

describe('AgentNoteModal', () => {
  it('renders warning text and textarea, calls onSubmit with the typed note', () => {
    const onSubmit = vi.fn()
    const onCancel = vi.fn()
    render(<AgentNoteModal stageId="s1" onSubmit={onSubmit} onCancel={onCancel} />)

    expect(screen.getByText(/agent will finish its current action/i)).toBeInTheDocument()

    const textarea = screen.getByRole('textbox')
    fireEvent.change(textarea, { target: { value: 'please add tests' } })
    fireEvent.click(screen.getByRole('button', { name: /send/i }))

    expect(onSubmit).toHaveBeenCalledWith('please add tests')
  })

  it('calls onCancel and does not submit when Cancel is clicked', () => {
    const onSubmit = vi.fn()
    const onCancel = vi.fn()
    render(<AgentNoteModal stageId="s1" onSubmit={onSubmit} onCancel={onCancel} />)

    fireEvent.click(screen.getByRole('button', { name: /cancel/i }))

    expect(onCancel).toHaveBeenCalled()
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('disables Send while the note is empty', () => {
    render(<AgentNoteModal stageId="s1" onSubmit={vi.fn()} onCancel={vi.fn()} />)
    expect(screen.getByRole('button', { name: /send/i })).toBeDisabled()
  })

  it('grows the textarea to fit its content', () => {
    const scrollHeightSpy = vi
      .spyOn(window.HTMLTextAreaElement.prototype, 'scrollHeight', 'get')
      .mockReturnValue(180)

    render(<AgentNoteModal stageId="s1" onSubmit={vi.fn()} onCancel={vi.fn()} />)
    const textarea = screen.getByRole('textbox') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'a longer note for the agent' } })

    expect(textarea.style.height).toBe('180px')

    scrollHeightSpy.mockRestore()
  })
})
