import { useState } from 'react'
import { afterEach, describe, expect, test, vi, type MockInstance } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import type { FileContent } from '../../api/files-client'
import { CommentProvider } from '../comments/CommentProvider'
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

type ViewerProps = Partial<React.ComponentProps<typeof FileViewer>> & { show?: boolean }

function ui({ show = true, ...props }: ViewerProps): React.ReactElement {
  return (
    <CommentProvider runId="r1">
      {show && <FileViewer content={null} loading={false} error={null} root="project" {...props} />}
    </CommentProvider>
  )
}

// renderViewer держит CommentProvider смонтированным между rerender, как App держит его над Files.
function renderViewer(props: ViewerProps) {
  const view = render(ui(props))
  return { ...view, update: (next: ViewerProps) => view.rerender(ui(next)) }
}

const SRC = 'line one\nline two\nline three'
const lineButton = (n: number) => screen.getByRole('button', { name: `Comment on line ${n}` })
const editorOf = (n: number) => screen.getByPlaceholderText(`Comment on line ${n}...`)

function typeIn(line: number, text: string): void {
  fireEvent.change(editorOf(line), { target: { value: text } })
}

function saveNote(line: number, text: string): void {
  fireEvent.click(lineButton(line))
  typeIn(line, text)
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))
}

describe('FileViewer', () => {
  test('shows a loading hint while loading', () => {
    renderViewer({ content: null, loading: true })
    expect(screen.getByText(/loading/i)).toBeInTheDocument()
  })

  test('shows an empty hint when nothing is selected', () => {
    renderViewer({ content: null })
    expect(screen.getByText(/select a file/i)).toBeInTheDocument()
  })

  test('shows the error message as plain text', () => {
    renderViewer({ content: null, error: 'binary_file' })
    expect(screen.getByText(/binary_file/)).toBeInTheDocument()
  })

  test('renders highlighted, escaped source into a single code element (the only dangerouslySetInnerHTML in the tree)', () => {
    const { container } = renderViewer({ content: makeContent({ language: 'go', content: 'package a' }) })
    const code = container.querySelector('code.hljs.language-go')
    expect(code).not.toBeNull()
    expect(code?.innerHTML).toContain('hljs-keyword')
    expect(code?.textContent).toBe('package a')
  })

  test('does not execute source as HTML — a script/img payload renders as inert text', () => {
    const pwned = '<img src=x onerror="window.__pwned=1">'
    const w = window as unknown as { __pwned?: number }
    delete w.__pwned

    const { container } = renderViewer({ content: makeContent({ language: 'typescript', content: pwned }) })

    expect(container.querySelector('img')).toBeNull()
    expect(w.__pwned).toBeUndefined()
    expect(container.querySelector('code')?.textContent).toBe(pwned)
  })

  test('plain language renders escaped text without hljs token spans', () => {
    const { container } = renderViewer({ content: makeContent({ language: 'plain', content: 'hello <world>' }) })
    const code = container.querySelector('code.language-plain')
    expect(code?.innerHTML).toBe('hello &lt;world&gt;')
  })

  test('renders one addressable row per source line', () => {
    renderViewer({ content: makeContent({ content: 'line one\nline two\nline three' }) })
    expect(screen.getByTestId('file-line-1')).toHaveTextContent('line one')
    expect(screen.getByTestId('file-line-2')).toHaveTextContent('line two')
    expect(screen.getByTestId('file-line-3')).toHaveTextContent('line three')
  })

  describe('line comments', () => {
    test('the line number is a button; the editor is a sibling below the row, not nested in it', () => {
      renderViewer({ content: makeContent({ content: SRC }) })

      fireEvent.click(lineButton(2))

      const row = screen.getByTestId('file-line-2')
      const editor = editorOf(2)
      expect(row.contains(editor)).toBe(false)
      expect(row.nextElementSibling?.contains(editor)).toBe(true)
      expect(within(row).getAllByRole('button')).toHaveLength(1)
    })

    test('clicking the source text of a line opens the editor too', () => {
      renderViewer({ content: makeContent({ content: SRC }) })

      fireEvent.click(screen.getByTestId('file-line-3'))

      expect(editorOf(3)).toBeInTheDocument()
    })

    test('without a root the preview is read-only', () => {
      renderViewer({ content: makeContent({ content: SRC }), root: undefined })

      expect(screen.queryByRole('button', { name: /comment on line/i })).toBeNull()
    })

    test('Save turns the editor into a card with the full text and the source quote', () => {
      const { container } = renderViewer({ content: makeContent({ content: SRC }) })

      saveNote(2, 'first line\nsecond line')

      expect(screen.queryByPlaceholderText('Comment on line 2...')).toBeNull()
      expect(container.querySelector('.comment-text')?.textContent).toBe('first line\nsecond line')
      expect(container.querySelector('.line-comment-quote-src')?.textContent).toBe('line two')
      expect(screen.queryByText('Draft')).toBeNull()
      expect(screen.getByTestId('file-line-2').className).toContain('has-comment')
      // Кнопки карточки лежат вне строки файла.
      expect(within(screen.getByTestId('file-line-2')).queryByRole('button', { name: /edit/i })).toBeNull()
    })

    test('Cmd+Enter saves the comment', () => {
      renderViewer({ content: makeContent({ content: SRC }) })
      fireEvent.click(lineButton(1))
      typeIn(1, 'quick')

      fireEvent.keyDown(editorOf(1), { key: 'Enter', metaKey: true })

      expect(screen.queryByPlaceholderText('Comment on line 1...')).toBeNull()
      expect(screen.getByText('quick')).toBeInTheDocument()
    })

    test('Edit reopens the saved text and Delete removes the comment', () => {
      renderViewer({ content: makeContent({ content: SRC }) })
      saveNote(2, 'keep me')

      fireEvent.click(screen.getByRole('button', { name: 'Edit comment on line 2' }))
      expect(editorOf(2)).toHaveValue('keep me')
      fireEvent.click(screen.getByRole('button', { name: 'Delete' }))

      expect(screen.queryByText('keep me')).toBeNull()
      expect(screen.getByTestId('file-line-2').className).not.toContain('has-comment')
    })

    test('the card cross removes the saved comment', () => {
      renderViewer({ content: makeContent({ content: SRC }) })
      saveNote(2, 'remove me')

      fireEvent.click(screen.getByRole('button', { name: 'Remove comment on line 2' }))

      expect(screen.queryByText('remove me')).toBeNull()
    })

    test('Escape folds the editor into a Draft card, keeps the text and does not reach document listeners', () => {
      renderViewer({ content: makeContent({ content: SRC }) })
      const onDocumentKey = vi.fn()
      document.addEventListener('keydown', onDocumentKey)
      fireEvent.click(lineButton(1))
      typeIn(1, 'half typed')

      fireEvent.keyDown(editorOf(1), { key: 'Escape' })
      document.removeEventListener('keydown', onDocumentKey)

      expect(onDocumentKey).not.toHaveBeenCalled()
      expect(screen.queryByPlaceholderText('Comment on line 1...')).toBeNull()
      expect(screen.getByText('half typed')).toBeInTheDocument()
      expect(screen.getByText('Draft')).toBeInTheDocument()
    })

    test('an empty editor disappears on fold and on a second click of the same line', () => {
      renderViewer({ content: makeContent({ content: SRC }) })

      fireEvent.click(lineButton(1))
      fireEvent.click(lineButton(1))

      expect(screen.queryByPlaceholderText('Comment on line 1...')).toBeNull()
      expect(screen.getByTestId('file-line-1').className).not.toContain('has-comment')
    })

    test('several lines keep independent comments at the same time', () => {
      renderViewer({ content: makeContent({ content: SRC }) })
      saveNote(1, 'one')
      saveNote(3, 'three')
      fireEvent.click(lineButton(2))
      typeIn(2, 'two unsaved')

      expect(screen.getByText('one')).toBeInTheDocument()
      expect(screen.getByText('three')).toBeInTheDocument()
      expect(editorOf(2)).toHaveValue('two unsaved')
    })

    test('unmounting the viewer (DIFF, closing Files) folds unsaved text into a Draft card', () => {
      const content = makeContent({ content: SRC })
      const view = renderViewer({ content })
      fireEvent.click(lineButton(2))
      typeIn(2, 'still here')

      view.update({ content, show: false })
      expect(screen.queryByTestId('file-line-2')).toBeNull()
      view.update({ content })

      expect(screen.queryByPlaceholderText('Comment on line 2...')).toBeNull()
      expect(screen.getByText('still here')).toBeInTheDocument()
      expect(screen.getByText('Draft')).toBeInTheDocument()
    })

    test('opening another line folds the open editor: only one form at a time', () => {
      renderViewer({ content: makeContent({ content: SRC }) })
      fireEvent.click(lineButton(1))
      typeIn(1, 'first')
      fireEvent.click(lineButton(2))
      fireEvent.click(lineButton(3))

      expect(screen.getAllByRole('textbox')).toHaveLength(1)
      expect(editorOf(3)).toBeInTheDocument()
      expect(screen.getByText('first')).toBeInTheDocument()
      expect(screen.getByTestId('file-line-2').className).not.toContain('has-comment')

      fireEvent.click(screen.getByRole('button', { name: 'Edit comment on line 1' }))
      expect(screen.getAllByRole('textbox')).toHaveLength(1)
      expect(editorOf(1)).toHaveValue('first')
    })

    test('switching to another file folds the open editor; an empty one disappears', () => {
      const a = makeContent({ path: 'a.go', content: SRC })
      const view = renderViewer({ content: a })
      fireEvent.click(lineButton(1))
      typeIn(1, 'kept')
      fireEvent.click(lineButton(3))

      view.update({ content: makeContent({ path: 'b.go', content: SRC }) })
      view.update({ content: a })

      expect(screen.queryByRole('textbox')).toBeNull()
      expect(screen.getByText('kept')).toBeInTheDocument()
      expect(screen.getByTestId('file-line-3').className).not.toContain('has-comment')
    })

    test('A → B → A keeps A\'s comment and B shows none of it', () => {
      const a = makeContent({ path: 'a.go', content: SRC })
      const b = makeContent({ path: 'b.go', content: SRC })
      const view = renderViewer({ content: a })
      saveNote(2, 'note on a')

      view.update({ content: b })
      expect(screen.queryByText('note on a')).toBeNull()
      expect(screen.getByTestId('file-line-2').className).not.toContain('has-comment')

      view.update({ content: a })
      expect(screen.getByText('note on a')).toBeInTheDocument()
    })

    test('a comment on one root does not show on the same path in another root', () => {
      const content = makeContent({ content: SRC })
      const view = renderViewer({ content, root: 'project' })
      saveNote(1, 'project note')

      view.update({ content, root: 'other' })

      expect(screen.queryByText('project note')).toBeNull()
    })
  })

  describe('source drift', () => {
    const v1 = (content: string) => makeContent({ content, etag: 'W/"1"' })
    const v2 = (content: string) => makeContent({ content, etag: 'W/"2"' })

    test('a new ETag with the same line text is confirmed silently', () => {
      const view = renderViewer({ content: v1('alpha\nbeta') })
      saveNote(2, 'note')

      view.update({ content: v2('alpha\nbeta') })

      expect(screen.queryByText(/source changed/i)).toBeNull()
    })

    test('a changed line shows the old quote and the current text; Use current line re-anchors', () => {
      const view = renderViewer({ content: v1('alpha\nbeta') })
      saveNote(2, 'note')

      view.update({ content: v2('alpha\nbeta changed') })

      expect(screen.getByText(/source changed/i)).toHaveTextContent('line 2 now reads: "beta changed"')
      expect(document.querySelector('.line-comment-quote-src')?.textContent).toBe('beta')
      fireEvent.click(screen.getByRole('button', { name: 'Use current line 2' }))

      expect(screen.queryByText(/source changed/i)).toBeNull()
      expect(document.querySelector('.line-comment-quote-src')?.textContent).toBe('beta changed')
      expect(screen.getByText('note')).toBeInTheDocument()
    })

    test('a moved line offers Move to line N and keeps the comment text', () => {
      const view = renderViewer({ content: v1('alpha\nbeta') })
      saveNote(2, 'note')

      view.update({ content: v2('new\nalpha\nbeta') })
      fireEvent.click(screen.getByRole('button', { name: 'Move to line 3' }))

      expect(screen.queryByText(/source changed/i)).toBeNull()
      expect(screen.getByTestId('file-line-3').className).toContain('has-comment')
      expect(screen.getByTestId('file-line-2').className).not.toContain('has-comment')
      expect(screen.getByText('note')).toBeInTheDocument()
    })

    test('Move is disabled while the target line already has a comment', () => {
      const view = renderViewer({ content: v1('alpha\nbeta') })
      saveNote(1, 'on alpha')
      saveNote(2, 'on beta')

      view.update({ content: v2('beta\nalpha') })

      expect(screen.getByRole('button', { name: 'Move to line 2' })).toBeDisabled()
    })

    test('a vanished line offers no re-anchor, only removal', () => {
      const view = renderViewer({ content: v1('a\nb\nc') })
      saveNote(3, 'note')

      view.update({ content: v2('a\nb') })

      expect(screen.getByText(/source changed/i)).toHaveTextContent('line 3 no longer exists')
      expect(screen.queryByRole('button', { name: /use current line/i })).toBeNull()
      expect(screen.getByRole('button', { name: 'Remove comment on line 3' })).toBeInTheDocument()
    })
  })

  describe('navigation, focus and cleanup', () => {
    afterEach(() => vi.restoreAllMocks())

    type ScrollSpy = MockInstance<(arg?: boolean | ScrollIntoViewOptions) => void>
    const centeredCalls = (spy: ScrollSpy): number[] =>
      spy.mock.calls.flatMap(([opt], index) => ((opt as ScrollIntoViewOptions | undefined)?.block === 'center' ? [index] : []))

    test('focus on a vanished line scrolls to its orphan card', () => {
      const view = renderViewer({ content: makeContent({ content: 'a\nb\nc' }) })
      saveNote(3, 'note')
      const spy = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})

      view.update({
        content: makeContent({ content: 'a\nb', etag: 'W/"2"' }),
        focus: { root: 'project', path: 'a.go', line: 3 },
      })

      const [index] = centeredCalls(spy)
      expect(index).toBeDefined()
      expect((spy.mock.contexts[index!] as HTMLElement).dataset.commentLine).toBe('3')
    })

    test('focus is one-shot: Reload after a manual scroll does not jump back', () => {
      const spy = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})
      function Host({ content }: { content: FileContent }): React.ReactElement {
        const [focus, setFocus] = useState<{ root: string; path: string; line: number } | null>({
          root: 'project',
          path: 'a.go',
          line: 2,
        })
        return (
          <CommentProvider runId="r1">
            <FileViewer
              content={content}
              loading={false}
              error={null}
              root="project"
              focus={focus}
              onFocusHandled={() => setFocus(null)}
            />
          </CommentProvider>
        )
      }
      const view = render(<Host content={makeContent({ content: SRC })} />)
      expect(centeredCalls(spy)).toHaveLength(1)

      view.rerender(<Host content={makeContent({ content: SRC, etag: 'W/"2"' })} />)

      expect(centeredCalls(spy)).toHaveLength(1)
    })

    test('a comment on an emptied file counts as vanished, not as an existing empty line', () => {
      const view = renderViewer({ content: makeContent({ content: '\nfoo' }) })
      saveNote(1, 'note on the blank line')

      view.update({ content: makeContent({ content: '', etag: 'W/"2"' }) })

      expect(screen.getByText(/source changed/i)).toHaveTextContent('line 1 no longer exists')
    })

    test('Escape in the editor returns focus to the line number button', () => {
      renderViewer({ content: makeContent({ content: SRC }) })
      fireEvent.click(lineButton(2))
      typeIn(2, 'half')

      fireEvent.keyDown(editorOf(2), { key: 'Escape' })

      expect(lineButton(2)).toHaveFocus()
    })

    test('Save, Delete and the card cross return focus to the line number button', () => {
      renderViewer({ content: makeContent({ content: SRC }) })
      saveNote(1, 'one')
      expect(lineButton(1)).toHaveFocus()

      fireEvent.click(screen.getByRole('button', { name: 'Edit comment on line 1' }))
      fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
      expect(lineButton(1)).toHaveFocus()

      saveNote(2, 'two')
      lineButton(3).focus()
      fireEvent.click(screen.getByRole('button', { name: 'Remove comment on line 2' }))
      expect(lineButton(2)).toHaveFocus()
    })

    test('removing an orphan card returns focus to the source container', () => {
      const view = renderViewer({ content: makeContent({ content: 'a\nb\nc' }) })
      saveNote(3, 'note')
      view.update({ content: makeContent({ content: 'a\nb', etag: 'W/"2"' }) })

      fireEvent.click(screen.getByRole('button', { name: 'Remove comment on line 3' }))

      expect(document.querySelector('.file-viewer-source')).toHaveFocus()
    })

    test('erasing a saved comment and pressing Escape keeps the saved text without a Draft', () => {
      renderViewer({ content: makeContent({ content: SRC }) })
      saveNote(1, 'precious')
      fireEvent.click(lineButton(1))
      typeIn(1, '')

      fireEvent.keyDown(editorOf(1), { key: 'Escape' })

      expect(screen.getByText('precious')).toBeInTheDocument()
      expect(screen.queryByText('Draft')).toBeNull()
    })

    test('an empty Save on a saved comment keeps it; only Delete removes it', () => {
      renderViewer({ content: makeContent({ content: SRC }) })
      saveNote(1, 'precious')
      fireEvent.click(lineButton(1))
      typeIn(1, '  ')

      fireEvent.click(screen.getByRole('button', { name: 'Update' }))

      expect(screen.getByText('precious')).toBeInTheDocument()
      expect(screen.queryByPlaceholderText('Comment on line 1...')).toBeNull()
    })

    test('a whitespace-only draft is discarded when the same line number is clicked again', () => {
      renderViewer({ content: makeContent({ content: SRC }) })
      fireEvent.click(lineButton(1))
      typeIn(1, '   ')

      fireEvent.click(lineButton(1))
      fireEvent.click(lineButton(1))

      expect(editorOf(1)).toHaveValue('')
    })
  })

  describe('sending', () => {
    test('a saved comment card offers Send and reports its comment', () => {
      const onSend = vi.fn()
      renderViewer({ content: makeContent({ content: SRC }), onSend })
      saveNote(2, 'ship this')

      fireEvent.click(screen.getByRole('button', { name: 'Send comment on line 2' }))

      expect(onSend).toHaveBeenCalledTimes(1)
      expect(onSend).toHaveBeenCalledWith(expect.stringMatching(/^file:/), expect.any(HTMLElement))
    })

    test('without a send handler the card has no Send button', () => {
      renderViewer({ content: makeContent({ content: SRC }) })
      saveNote(2, 'ship this')

      expect(screen.queryByRole('button', { name: /^Send comment/ })).toBeNull()
    })

    test('the card carries its own recipient choice', () => {
      renderViewer({ content: makeContent({ content: SRC }), onSend: vi.fn() })
      saveNote(2, 'ship this')

      expect(screen.getByRole('combobox', { name: 'Recipient' })).toBeInTheDocument()
    })
  })
})
