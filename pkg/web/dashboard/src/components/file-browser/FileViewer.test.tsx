import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import type { FileContent } from '../../api/files-client'
import { FlowApiError } from '../../api/run-client'
import { FileViewer } from './FileViewer'

function makeContent(overrides: Partial<FileContent> = {}): FileContent {
  return {
    path: 'a.go',
    displayPath: 'a.go',
    reference: '[AFM file: "/w/afm/a.go"]',
    language: 'go',
    size: 11,
    modifiedAt: '2026-09-03T00:00:00Z',
    content: 'package a',
    etag: 'W/"1"',
    ...overrides,
  }
}

describe('FileViewer', () => {
  test('shows a loading hint while loading', () => {
    render(<FileViewer content={null} loading error={null} />)
    expect(screen.getByText(/loading/i)).toBeInTheDocument()
  })

  test('shows an empty hint when nothing is selected', () => {
    render(<FileViewer content={null} loading={false} error={null} />)
    expect(screen.getByText(/select a file/i)).toBeInTheDocument()
  })

  test('shows the error message as plain text', () => {
    render(<FileViewer content={null} loading={false} error="binary_file" />)
    expect(screen.getByText(/binary_file/)).toBeInTheDocument()
  })

  test('renders highlighted, escaped source into a single code element (the only dangerouslySetInnerHTML in the tree)', () => {
    const { container } = render(<FileViewer content={makeContent({ language: 'go', content: 'package a' })} loading={false} error={null} />)
    const code = container.querySelector('code.hljs.language-go')
    expect(code).not.toBeNull()
    expect(code?.innerHTML).toContain('hljs-keyword')
    expect(code?.textContent).toBe('package a')
  })

  test('does not execute source as HTML — a script/img payload renders as inert text', () => {
    const pwned = '<img src=x onerror="window.__pwned=1">'
    const w = window as unknown as { __pwned?: number }
    delete w.__pwned

    const { container } = render(<FileViewer content={makeContent({ language: 'typescript', content: pwned })} loading={false} error={null} />)

    expect(container.querySelector('img')).toBeNull()
    expect(w.__pwned).toBeUndefined()
    expect(container.querySelector('code')?.textContent).toBe(pwned)
  })

  test('plain language renders escaped text without hljs token spans', () => {
    const { container } = render(<FileViewer content={makeContent({ language: 'plain', content: 'hello <world>' })} loading={false} error={null} />)
    const code = container.querySelector('code.language-plain')
    expect(code?.innerHTML).toBe('hello &lt;world&gt;')
  })

  test('renders one addressable row per source line', () => {
    render(<FileViewer content={makeContent({ content: 'line one\nline two\nline three' })} loading={false} error={null} />)
    expect(screen.getByTestId('file-line-1')).toHaveTextContent('line one')
    expect(screen.getByTestId('file-line-2')).toHaveTextContent('line two')
    expect(screen.getByTestId('file-line-3')).toHaveTextContent('line three')
  })

  test('a line is not clickable/annotatable when flowPauseState is not set and there is no pauseFlow', () => {
    render(<FileViewer content={makeContent({ content: 'a\nb' })} loading={false} error={null} root="project" addNote={vi.fn()} />)
    fireEvent.click(screen.getByTestId('file-line-2'))
    expect(screen.queryByRole('textbox')).toBeNull()
    expect(screen.queryByText(/поставить флоу на паузу/i)).toBeNull()
  })

  test('a line is not annotatable without an addNote/root (flowPauseState alone is not enough)', () => {
    render(<FileViewer content={makeContent({ content: 'a\nb' })} loading={false} error={null} flowPauseState="paused" />)
    fireEvent.click(screen.getByTestId('file-line-2'))
    expect(screen.queryByRole('textbox')).toBeNull()
  })

  test('opens a comment editor on line click and calls addNote on save', async () => {
    const addNote = vi.fn().mockResolvedValue({ note: {}, rev: 1 })
    render(
      <FileViewer
        content={makeContent({ content: 'line one\nline two\nline three' })}
        loading={false}
        error={null}
        root="project"
        flowPauseState="paused"
        addNote={addNote}
        contentSha="sha256:x"
      />,
    )

    fireEvent.click(screen.getByTestId('file-line-2'))
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'fix this' } })
    fireEvent.click(screen.getByText('Save'))

    await waitFor(() =>
      expect(addNote).toHaveBeenCalledWith(
        expect.objectContaining({ root: 'project', path: 'a.go', line: 2, text: 'fix this', content_sha: 'sha256:x' }),
      ),
    )
  })

  test('surfaces a rejected addNote inline and keeps the form open', async () => {
    const addNote = vi.fn().mockRejectedValue(new FlowApiError('/api/flow/notes', 409, 'stale_content'))
    render(
      <FileViewer
        content={makeContent({ content: 'line one\nline two' })}
        loading={false}
        error={null}
        root="project"
        flowPauseState="paused"
        addNote={addNote}
        contentSha="sha256:x"
      />,
    )

    fireEvent.click(screen.getByTestId('file-line-2'))
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'fix this' } })
    fireEvent.click(screen.getByText('Save'))

    await waitFor(() => expect(addNote).toHaveBeenCalledTimes(1))

    // The 409 is shown inline (mentioning the reload guidance), and the editor
    // stays open with the draft intact instead of silently closing.
    expect(screen.getByRole('alert')).toHaveTextContent(/reload/i)
    expect(screen.getByRole('textbox')).toHaveValue('fix this')
    expect(screen.getByTestId('file-line-2').className).not.toContain('has-comment')
  })

  test('marks a line with a saved comment and lets it be reopened for editing', async () => {
    const addNote = vi.fn().mockResolvedValue({ note: {}, rev: 1 })
    render(
      <FileViewer
        content={makeContent({ content: 'line one\nline two' })}
        loading={false}
        error={null}
        root="project"
        flowPauseState="paused"
        addNote={addNote}
        contentSha="sha256:x"
      />,
    )

    fireEvent.click(screen.getByTestId('file-line-1'))
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'note text' } })
    fireEvent.click(screen.getByText('Save'))
    await waitFor(() => expect(addNote).toHaveBeenCalledTimes(1))

    expect(screen.getByTestId('file-line-1').className).toContain('has-comment')

    // Reopening the same line prefills the previously saved text and offers
    // "Update" instead of "Save" — the backend addNote call replaces the note
    // for the same root/path/line rather than creating a duplicate.
    fireEvent.click(screen.getByTestId('file-line-1'))
    expect(screen.getByRole('textbox')).toHaveValue('note text')
    expect(screen.getByText('Update')).toBeInTheDocument()
  })

  // Review-finding P2 #5: a note already persisted for this file (passed via
  // savedNotes) must rehydrate its marker + textarea prefill on open — without
  // any local save this session — so switching files and back never loses the
  // annotation. Only line notes matching this file's root+path hydrate.
  test('hydrates saved line notes into markers and textarea prefill', () => {
    const savedNotes = [
      {
        id: 'n1', root: 'project', path: 'a.go', display_path: 'project/a.go',
        reference: '[AFM file: "/w/a.go"]', line: 2, orig_line_text: 'line two',
        content_sha: 'sha256:x', text: 'saved on line two', created_at: '2026-09-08T00:00:00Z',
      },
      // A note for a DIFFERENT file must NOT bleed into this viewer.
      {
        id: 'n2', root: 'project', path: 'other.go', display_path: 'project/other.go',
        reference: '[AFM file: "/w/other.go"]', line: 1, orig_line_text: 'x',
        content_sha: 'sha256:y', text: 'elsewhere', created_at: '2026-09-08T00:00:00Z',
      },
    ]
    render(
      <FileViewer
        content={makeContent({ content: 'line one\nline two' })}
        loading={false}
        error={null}
        root="project"
        flowPauseState="paused"
        addNote={vi.fn()}
        contentSha="sha256:x"
        savedNotes={savedNotes}
      />,
    )

    // Line 2 has the saved note's marker; line 1 does not.
    expect(screen.getByTestId('file-line-2').className).toContain('has-comment')
    expect(screen.getByTestId('file-line-1').className).not.toContain('has-comment')

    // Reopening line 2 prefills the saved text and offers "Update".
    fireEvent.click(screen.getByTestId('file-line-2'))
    expect(screen.getByRole('textbox')).toHaveValue('saved on line two')
    expect(screen.getByText('Update')).toBeInTheDocument()
  })

  // Re-review P2: a locally-saved note must not leak across workspace roots that
  // share a path. After saving on root A `a.go`, switching to root B `a.go`
  // (same path, same content) must reset the local marker/prefill — otherwise
  // Save on root B would create a note carrying root A's text.
  test('does not leak local annotations between roots with the same path', async () => {
    const addNote = vi.fn().mockResolvedValue({ note: {}, rev: 1 })
    const content = makeContent({ content: 'line one\nline two' })
    const { rerender } = render(
      <FileViewer content={content} loading={false} error={null} root="A" flowPauseState="paused" addNote={addNote} contentSha="sha256:x" />,
    )

    fireEvent.click(screen.getByTestId('file-line-1'))
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'note for root A' } })
    fireEvent.click(screen.getByText('Save'))
    await waitFor(() => expect(addNote).toHaveBeenCalledTimes(1))
    expect(screen.getByTestId('file-line-1').className).toContain('has-comment')

    // Switch to root B, same path/content.
    rerender(
      <FileViewer content={content} loading={false} error={null} root="B" flowPauseState="paused" addNote={addNote} contentSha="sha256:x" />,
    )
    expect(screen.getByTestId('file-line-1').className).not.toContain('has-comment')
    // Opening line 1 on root B offers a fresh "Save" with an empty draft, not
    // "Update" prefilled with root A's text.
    fireEvent.click(screen.getByTestId('file-line-1'))
    expect(screen.getByRole('textbox')).toHaveValue('')
    expect(screen.getByText('Save')).toBeInTheDocument()
  })

  test('clicking the same line again closes the editor without saving', () => {
    const addNote = vi.fn()
    render(
      <FileViewer
        content={makeContent({ content: 'line one' })}
        loading={false}
        error={null}
        root="project"
        flowPauseState="paused"
        addNote={addNote}
        contentSha="sha256:x"
      />,
    )

    fireEvent.click(screen.getByTestId('file-line-1'))
    expect(screen.getByRole('textbox')).toBeInTheDocument()

    fireEvent.click(screen.getByTestId('file-line-1'))
    expect(screen.queryByRole('textbox')).toBeNull()
    expect(addNote).not.toHaveBeenCalled()
  })

  test('Save is disabled while the draft is empty', () => {
    render(
      <FileViewer
        content={makeContent({ content: 'line one' })}
        loading={false}
        error={null}
        root="project"
        flowPauseState="paused"
        addNote={vi.fn()}
        contentSha="sha256:x"
      />,
    )

    fireEvent.click(screen.getByTestId('file-line-1'))
    expect(screen.getByText('Save')).toBeDisabled()
  })

  test('prompts to pause on the first note when not paused', async () => {
    const pauseFlow = vi.fn().mockResolvedValue({ paused_stages: ['s1'] })
    const sampleFile = makeContent({ content: 'line one\nline two\nline three' })
    render(
      <FileViewer content={sampleFile} loading={false} error={null} root="project" addNote={vi.fn()} flowPauseState="none" pauseFlow={pauseFlow} />,
    )
    fireEvent.click(screen.getByTestId('file-line-2'))
    expect(screen.getByText(/поставить флоу на паузу/i)).toBeInTheDocument()
    // The confirm blocks the editor from opening on this same click.
    expect(screen.queryByRole('textbox')).toBeNull()
    fireEvent.click(screen.getByText('Да'))
    await waitFor(() => expect(pauseFlow).toHaveBeenCalled())
  })

  test('declining the pause-gate confirm closes it without calling pauseFlow', () => {
    const pauseFlow = vi.fn()
    render(
      <FileViewer
        content={makeContent({ content: 'a\nb' })}
        loading={false}
        error={null}
        root="project"
        addNote={vi.fn()}
        flowPauseState="none"
        pauseFlow={pauseFlow}
      />,
    )

    fireEvent.click(screen.getByTestId('file-line-2'))
    expect(screen.getByText(/поставить флоу на паузу/i)).toBeInTheDocument()

    fireEvent.click(screen.getByText('Нет'))
    expect(screen.queryByText(/поставить флоу на паузу/i)).toBeNull()
    expect(pauseFlow).not.toHaveBeenCalled()
  })

  test('opens the editor for the pending line once the flow reports paused', () => {
    const pauseFlow = vi.fn().mockResolvedValue({ paused_stages: ['s1'] })
    const { rerender } = render(
      <FileViewer
        content={makeContent({ content: 'a\nb' })}
        loading={false}
        error={null}
        root="project"
        addNote={vi.fn()}
        flowPauseState="none"
        pauseFlow={pauseFlow}
      />,
    )

    fireEvent.click(screen.getByTestId('file-line-2'))
    fireEvent.click(screen.getByText('Да'))

    // The caller's poll picks up the new status and re-renders with 'paused'.
    rerender(
      <FileViewer
        content={makeContent({ content: 'a\nb' })}
        loading={false}
        error={null}
        root="project"
        addNote={vi.fn()}
        flowPauseState="paused"
        pauseFlow={pauseFlow}
      />,
    )

    expect(screen.queryByText(/поставить флоу на паузу/i)).toBeNull()
    expect(screen.getByRole('textbox')).toBeInTheDocument()
  })
})
