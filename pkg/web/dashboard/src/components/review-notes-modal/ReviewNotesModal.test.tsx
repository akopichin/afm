import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { cancelNotes, deleteNote, FlowApiError, injectNotes, listNotes, updateNote } from '../../api/run-client'
import { ReviewNotesModal } from './ReviewNotesModal'

vi.mock('../../api/run-client', async () => {
  const actual = await vi.importActual<typeof import('../../api/run-client')>('../../api/run-client')
  return {
    ...actual,
    listNotes: vi.fn(),
    updateNote: vi.fn(),
    deleteNote: vi.fn(),
    injectNotes: vi.fn(),
    cancelNotes: vi.fn(),
  }
})

function note(overrides: Partial<import('../../types').ReviewNote> = {}): import('../../types').ReviewNote {
  return {
    id: 'n1',
    root: 'project',
    path: 'a.go',
    display_path: 'project/a.go',
    reference: '[AFM file: "/w/a.go"]',
    line: 2,
    orig_line_text: null,
    content_sha: 'sha256:x',
    text: 'x',
    created_at: '2026-09-08T00:00:00Z',
    ...overrides,
  }
}

describe('ReviewNotesModal', () => {
  beforeEach(() => {
    vi.mocked(cancelNotes).mockReset()
    vi.mocked(injectNotes).mockReset()
    vi.mocked(updateNote).mockReset()
    vi.mocked(deleteNote).mockReset()
    vi.mocked(listNotes).mockReset()
  })

  it('disables inject and discloses when no target stage', async () => {
    vi.mocked(listNotes).mockResolvedValue({ rev: 1, notes: [note({ display_path: 'project/a.go' })] })
    render(<ReviewNotesModal pausedStages={[]} onClose={() => {}} />)
    await screen.findByText(/project\/a.go/)
    expect(screen.getByText(/no active stage to receive notes/i)).toBeInTheDocument()
    expect(screen.getByText('Inject')).toBeDisabled()
  })

  it('injects into the chosen stage', async () => {
    vi.mocked(listNotes).mockResolvedValue({ rev: 1, notes: [note({ display_path: 'project/a.go' })] })
    const inject = vi.mocked(injectNotes).mockResolvedValue()
    render(<ReviewNotesModal pausedStages={['s1']} onClose={() => {}} />)
    fireEvent.change(await screen.findByRole('combobox'), { target: { value: 's1' } })
    fireEvent.click(screen.getByText('Inject'))
    await waitFor(() => expect(inject).toHaveBeenCalledWith('s1'))
  })

  it('groups notes by display_path', async () => {
    vi.mocked(listNotes).mockResolvedValue({
      rev: 1,
      notes: [
        note({ id: 'n1', display_path: 'project/a.go', line: 1, text: 'first' }),
        note({ id: 'n2', display_path: 'project/b.go', line: 3, text: 'second' }),
        note({ id: 'n3', display_path: 'project/a.go', line: 5, text: 'third' }),
      ],
    })
    render(<ReviewNotesModal pausedStages={['s1']} onClose={() => {}} />)
    await screen.findByText('project/a.go')
    expect(screen.getByText('project/b.go')).toBeInTheDocument()
    expect(screen.getByText('first')).toBeInTheDocument()
    expect(screen.getByText('second')).toBeInTheDocument()
    expect(screen.getByText('third')).toBeInTheDocument()
  })

  it('edits a note and updates the tracked rev', async () => {
    vi.mocked(listNotes).mockResolvedValue({ rev: 1, notes: [note({ text: 'old text' })] })
    const update = vi.mocked(updateNote).mockResolvedValue({ rev: 2 })
    render(<ReviewNotesModal pausedStages={['s1']} onClose={() => {}} />)

    await screen.findByText('old text')
    fireEvent.click(screen.getByText('Edit'))
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'new text' } })
    fireEvent.click(screen.getByText('Save'))

    await waitFor(() => expect(update).toHaveBeenCalledWith('n1', 'new text', 1))
    expect(await screen.findByText('new text')).toBeInTheDocument()
  })

  it('saves an edited note on Enter and leaves Shift+Enter for a newline', async () => {
    vi.mocked(listNotes).mockResolvedValue({ rev: 1, notes: [note({ text: 'old text' })] })
    const update = vi.mocked(updateNote).mockResolvedValue({ rev: 2 })
    render(<ReviewNotesModal pausedStages={['s1']} onClose={() => {}} />)
    await screen.findByText('old text')
    fireEvent.click(screen.getByText('Edit'))
    const textarea = screen.getByRole('textbox')
    fireEvent.change(textarea, { target: { value: 'new text' } })
    expect(fireEvent.keyDown(textarea, { key: 'Enter', shiftKey: true })).toBe(true)
    expect(update).not.toHaveBeenCalled()
    fireEvent.keyDown(textarea, { key: 'Enter', isComposing: true })
    expect(update).not.toHaveBeenCalled()
    fireEvent.keyDown(textarea, { key: 'Enter' })
    await waitFor(() => expect(update).toHaveBeenCalledWith('n1', 'new text', 1))
    expect(await screen.findByText('new text')).toBeInTheDocument()
  })

  it('deletes a note and refreshes via listNotes to pick up the new rev', async () => {
    vi.mocked(listNotes)
      .mockResolvedValueOnce({ rev: 1, notes: [note({ id: 'n1', text: 'goner' })] })
      .mockResolvedValueOnce({ rev: 2, notes: [] })
    const del = vi.mocked(deleteNote).mockResolvedValue()
    render(<ReviewNotesModal pausedStages={['s1']} onClose={() => {}} />)

    await screen.findByText('goner')
    fireEvent.click(screen.getByRole('button', { name: 'Delete review note on line 2' }))

    await waitFor(() => expect(del).toHaveBeenCalledWith('n1', 1))
    await waitFor(() => expect(screen.queryByText('goner')).toBeNull())
    expect(listNotes).toHaveBeenCalledTimes(2)
  })

  it('cancels the review round without injecting', async () => {
    vi.mocked(listNotes).mockResolvedValue({ rev: 1, notes: [] })
    const onClose = vi.fn()
    const cancel = vi.mocked(cancelNotes).mockResolvedValue()
    render(<ReviewNotesModal pausedStages={['s1']} onClose={onClose} />)

    await waitFor(() => expect(listNotes).toHaveBeenCalled())
    fireEvent.click(screen.getByText('Cancel'))

    await waitFor(() => expect(cancel).toHaveBeenCalled())
    expect(onClose).toHaveBeenCalled()
  })

  it('surfaces a rev_conflict error inline instead of throwing', async () => {
    vi.mocked(listNotes).mockResolvedValue({ rev: 1, notes: [note({ text: 'old text' })] })
    vi.mocked(updateNote).mockRejectedValue(new FlowApiError('/api/flow/notes/n1', 409, 'rev_conflict'))
    render(<ReviewNotesModal pausedStages={['s1']} onClose={() => {}} />)

    await screen.findByText('old text')
    fireEvent.click(screen.getByText('Edit'))
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'new text' } })
    fireEvent.click(screen.getByText('Save'))

    expect(await screen.findByText(/rev conflict/i)).toBeInTheDocument()
  })

  it('labels a note as a review note sent with Continue and shows its full text', async () => {
    const long = `${'x'.repeat(300)}\nsecond line`
    vi.mocked(listNotes).mockResolvedValue({ rev: 1, notes: [note({ text: long })] })
    const { container } = render(<ReviewNotesModal pausedStages={['s1']} onClose={() => {}} />)

    await screen.findByText(/Review note · sent with Continue/)
    expect(container.querySelector('.comment-text')?.textContent).toBe(long)
  })

  it('edits in the shared editor; Escape folds it back without saving, closing the modal or losing the note', async () => {
    vi.mocked(listNotes).mockResolvedValue({ rev: 1, notes: [note({ text: 'old text' })] })
    const onClose = vi.fn()
    const { container } = render(<ReviewNotesModal pausedStages={['s1']} onClose={onClose} />)

    await screen.findByText('old text')
    fireEvent.click(screen.getByRole('button', { name: 'Edit review note on line 2' }))
    expect(container.querySelector('.comment-editor[data-comment-context="review"]')).not.toBeNull()
    expect(screen.queryByRole('button', { name: 'Attach' })).toBeNull()
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'half typed' } })
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Escape' })

    expect(container.querySelector('.comment-editor')).toBeNull()
    expect(screen.getByText('old text')).toBeInTheDocument()
    expect(updateNote).not.toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Edit review note on line 2' })).toHaveFocus()
  })

  it('keeps a half-typed edit after folding and restores it in the editor', async () => {
    vi.mocked(listNotes).mockResolvedValue({ rev: 1, notes: [note({ text: 'old text' })] })
    const { container } = render(<ReviewNotesModal pausedStages={['s1']} onClose={() => {}} />)

    await screen.findByText('old text')
    fireEvent.click(screen.getByRole('button', { name: 'Edit review note on line 2' }))
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'half typed' } })
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Escape' })

    expect(container.querySelector('.comment-draft-badge')).not.toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Edit review note on line 2' }))
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).value).toBe('half typed')
  })

  it('drops the kept edit once it is saved', async () => {
    vi.mocked(listNotes).mockResolvedValue({ rev: 1, notes: [note({ text: 'old text' })] })
    vi.mocked(updateNote).mockResolvedValue({ rev: 2 })
    const { container } = render(<ReviewNotesModal pausedStages={['s1']} onClose={() => {}} />)

    await screen.findByText('old text')
    fireEvent.click(screen.getByText('Edit'))
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'new text' } })
    fireEvent.click(screen.getByText('Save'))
    await screen.findByText('new text')

    expect(container.querySelector('.comment-draft-badge')).toBeNull()
    fireEvent.click(screen.getByText('Edit'))
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).value).toBe('new text')
  })

  it('has no one-click cross on a saved note; deleting needs the explicit Delete button', async () => {
    vi.mocked(listNotes).mockResolvedValue({ rev: 1, notes: [note({ text: 'keep me' })] })
    render(<ReviewNotesModal pausedStages={['s1']} onClose={() => {}} />)

    await screen.findByText('keep me')

    expect(screen.queryByRole('button', { name: /^Remove/ })).toBeNull()
    expect(screen.getByRole('button', { name: 'Delete review note on line 2' })).toBeInTheDocument()
  })

  it('saves with Cmd/Ctrl+Enter from the editor', async () => {
    vi.mocked(listNotes).mockResolvedValue({ rev: 1, notes: [note({ text: 'old text' })] })
    const update = vi.mocked(updateNote).mockResolvedValue({ rev: 2 })
    render(<ReviewNotesModal pausedStages={['s1']} onClose={() => {}} />)

    await screen.findByText('old text')
    fireEvent.click(screen.getByText('Edit'))
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'new text' } })
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Enter', ctrlKey: true })

    await waitFor(() => expect(update).toHaveBeenCalledWith('n1', 'new text', 1))
  })

  it('deletes explicitly from the editor', async () => {
    vi.mocked(listNotes)
      .mockResolvedValueOnce({ rev: 1, notes: [note({ text: 'goner' })] })
      .mockResolvedValueOnce({ rev: 2, notes: [] })
    const del = vi.mocked(deleteNote).mockResolvedValue()
    render(<ReviewNotesModal pausedStages={['s1']} onClose={() => {}} />)

    await screen.findByText('goner')
    fireEvent.click(screen.getByText('Edit'))
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))

    await waitFor(() => expect(del).toHaveBeenCalledWith('n1', 1))
  })
})
