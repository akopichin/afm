import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { StrictMode, type ReactElement } from 'react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { FileBrowserProvider } from '../file-browser'
import { CommentProvider } from '../comments/CommentProvider'
import { LineCommentedDocument, quotedSourceLine, StaleComments, type LineCommentDocumentApi } from './LineCommentedDocument'
import { useComments } from '../comments/CommentProvider'

// Проверяем useLineComments через реальный DOM: ввод, навигацию, классы, collapse и reset.

type HarnessProps = {
  text: string
  specialSections?: boolean
  identity?: string
  version?: string
  show?: boolean
  beforeOpen?: () => void
  docRef?: { current: LineCommentDocumentApi | null }
}

function Harness({ text, specialSections = true, identity = 'doc-1', version = 'v1', show = true, beforeOpen, docRef }: HarnessProps): ReactElement {
  return (
    <CommentProvider runId="run-1">
    <FileBrowserProvider flowName="flow1" startedAt="t1" enabled={true}>
      {show && <LineCommentedDocument
        key={identity}
        text={text}
        specialSections={specialSections}
        stageId="s1"
        scope={{ kind: 'plan', version }}
        beforeOpen={beforeOpen}
        bodyId="plan-content"
        bodyClassName="line-commented"
      >
        {(doc) => {
          if (docRef !== undefined) docRef.current = doc
          return (
            <>
              {doc.body}
              <div data-testid="count">{doc.commentCount}</div>
              <div data-testid="open-draft">{String(doc.hasOpenDraft)}</div>
            </>
          )
        }}
      </LineCommentedDocument>}
    </FileBrowserProvider>
    </CommentProvider>
  )
}

function anchor(container: HTMLElement, line: number): HTMLElement {
  return container.querySelector(`[data-line="${line}"]`) as HTMLElement
}

describe('LineCommentedDocument / useLineComments', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('click on an anchor opens a form (sibling, data-comment-ui) and marks the anchor is-active', () => {
    const { container } = render(<Harness text={'# First line\n# Second line'} />)

    expect(container.querySelector('.line-comment-form')).toBeNull()
    const a1 = anchor(container, 1)
    fireEvent.click(a1)

    const form = container.querySelector('.line-comment-form[data-comment-line="1"]') as HTMLElement
    expect(form).not.toBeNull()
    expect(form.getAttribute('data-comment-ui')).not.toBeNull()
    // Форма — НЕ потомок якоря.
    expect(form.closest('[data-line]')).toBeNull()
    expect(a1.classList.contains('is-active')).toBe(true)
    // Заголовок формы называет строку.
    expect(form.querySelector('.comment-title')?.textContent).toBe('Comment on line 1')
  })

  test('beforeOpen is called synchronously before the form opens', () => {
    const beforeOpen = vi.fn()
    const { container } = render(<Harness text={'# Line'} beforeOpen={beforeOpen} />)

    fireEvent.click(anchor(container, 1))
    expect(beforeOpen).toHaveBeenCalledTimes(1)
    expect(container.querySelector('.line-comment-form')).not.toBeNull()
  })

  test('clicking a link inside an anchor does NOT open a form', () => {
    const { container } = render(<Harness text={'See [here](/x) now'} />)
    const link = container.querySelector('a') as HTMLElement
    expect(link).not.toBeNull()

    fireEvent.click(link)
    expect(container.querySelector('.line-comment-form')).toBeNull()
  })

  test('clicks inside a form (data-comment-ui) are ignored by the delegated handler', () => {
    const { container } = render(<Harness text={'# Line'} />)
    fireEvent.click(anchor(container, 1))
    const textarea = container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'typing' } })

    // Клик по самому textarea (внутри data-comment-ui) не должен закрыть/пере-
    // открыть форму — черновик остаётся.
    fireEvent.click(textarea)
    expect((container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement).value).toBe('typing')
  })

  test('a text selection intersecting the anchor suppresses opening the form', () => {
    const { container } = render(<Harness text={'# Line one\n# Line two'} />)
    const a1 = anchor(container, 1)

    vi.spyOn(window, 'getSelection').mockReturnValue({
      isCollapsed: false,
      rangeCount: 1,
      toString: () => 'selected',
      getRangeAt: () => ({ intersectsNode: () => true }) as unknown as Range,
      anchorNode: a1,
    } as unknown as Selection)

    fireEvent.click(a1)
    expect(container.querySelector('.line-comment-form')).toBeNull()
  })

  // Примечание: jsdom не реализует PointerEvent, поэтому координатный drag-порог здесь драйвить нельзя.
  // Реальный drag по тексту создаёт выделение — этот сценарий покрыт тестом selection выше.
  test('a plain pointerdown/pointerup (no drag) does not suppress the click', () => {
    const { container } = render(<Harness text={'# Line one'} />)
    const a1 = anchor(container, 1)

    fireEvent.pointerDown(a1)
    fireEvent.pointerUp(a1)
    fireEvent.click(a1)
    expect(container.querySelector('.line-comment-form')).not.toBeNull()
  })

  test('hover adds is-hover only to the nearest anchor and moves it between anchors', () => {
    const { container } = render(<Harness text={'- alpha\n- beta'} specialSections={false} />)
    const li1 = anchor(container, 1)
    const li2 = anchor(container, 2)

    fireEvent.pointerOver(li1)
    expect(li1.classList.contains('is-hover')).toBe(true)
    expect(li2.classList.contains('is-hover')).toBe(false)

    fireEvent.pointerOver(li2)
    expect(li1.classList.contains('is-hover')).toBe(false)
    expect(li2.classList.contains('is-hover')).toBe(true)

    fireEvent.pointerOut(li2, { relatedTarget: document.body })
    expect(li2.classList.contains('is-hover')).toBe(false)
  })

  test('saving a comment sets has-comment on the anchor and updates commentCount; deleting clears it', () => {
    const { container } = render(<Harness text={'# First line\n# Second line'} />)
    const a1 = anchor(container, 1)

    fireEvent.click(a1)
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, {
      target: { value: 'please fix' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))

    expect(a1.classList.contains('has-comment')).toBe(true)
    expect(screen.getByTestId('count').textContent).toBe('1')
    // Индикатор сохранённого комментария — sibling с data-comment-line.
    expect(container.querySelector('.line-comment-display[data-comment-line="1"]')).not.toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Remove comment on line 1' }))
    expect(a1.classList.contains('has-comment')).toBe(false)
    expect(screen.getByTestId('count').textContent).toBe('0')
  })

  test('imperative classes survive a re-render that does not change the document text', () => {
    const { container } = render(<Harness text={'# First line\n# Second line'} />)
    const a1 = anchor(container, 1)

    // Комментарий на строке 1.
    fireEvent.click(a1)
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, { target: { value: 'note' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))
    expect(a1.classList.contains('has-comment')).toBe(true)

    // Открываем форму на строке 2 (ре-рендер владельца без смены текста) —
    // has-comment на строке 1 восстанавливается императивным слоем.
    fireEvent.click(anchor(container, 2))
    expect(a1.classList.contains('has-comment')).toBe(true)
  })

  test('a new document version starts clean; the old version drafts come back with that version', () => {
    const { container, rerender } = render(<Harness text={'# First line'} version="a" />)
    fireEvent.click(anchor(container, 1))
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, { target: { value: 'x' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))
    expect(screen.getByTestId('count').textContent).toBe('1')

    rerender(<Harness text={'# First line'} version="b" />)
    expect(screen.getByTestId('count').textContent).toBe('0')
    expect(container.querySelector('.line-comment-form')).toBeNull()
    expect(anchor(container, 1).classList.contains('has-comment')).toBe(false)

    rerender(<Harness text={'# First line'} version="a" />)
    expect(screen.getByTestId('count').textContent).toBe('1')
    expect(anchor(container, 1).classList.contains('has-comment')).toBe(true)
  })

  test('unmounting and mounting the document restores the open editor text and the saved card', () => {
    const { container, rerender } = render(<Harness text={'# First\n# Second'} />)
    fireEvent.click(anchor(container, 1))
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, { target: { value: 'saved one' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))
    fireEvent.click(anchor(container, 2))
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, { target: { value: 'typing two' } })

    rerender(<Harness text={'# First\n# Second'} show={false} />)
    expect(container.querySelector('.line-comment-form')).toBeNull()
    rerender(<Harness text={'# First\n# Second'} show={true} />)

    expect(container.querySelector('.line-comment-display[data-comment-line="1"] .comment-text')?.textContent).toBe('saved one')
    const editor = container.querySelector('.line-comment-form:not(.line-comment-display)[data-comment-line="2"] textarea') as HTMLTextAreaElement
    expect(editor.value).toBe('typing two')
    expect(screen.getByTestId('count').textContent).toBe('2')
  })

  test('closing the editor with unsaved text folds it into a Draft card that keeps the text and counts', () => {
    const { container } = render(<Harness text={'# Line'} />)
    fireEvent.click(anchor(container, 1))
    const textarea = container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'half done' } })
    fireEvent.keyDown(textarea, { key: 'Escape' })

    const card = container.querySelector('.line-comment-display[data-comment-line="1"]') as HTMLElement
    expect(card.querySelector('.comment-text')?.textContent).toBe('half done')
    expect(card.querySelector('.comment-draft-badge')).not.toBeNull()
    expect(screen.getByTestId('count').textContent).toBe('1')

    fireEvent.click(within(card).getByRole('button', { name: 'Edit comment on line 1' }))
    expect((container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement).value).toBe('half done')
  })

  test('the api lists non-empty comments with key, revision and trimmed text, ordered by line', () => {
    const docRef: { current: LineCommentDocumentApi | null } = { current: null }
    const { container } = render(<Harness text={'# One\n# Two'} docRef={docRef} />)
    fireEvent.click(anchor(container, 2))
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, { target: { value: '  second  ' } })
    fireEvent.keyDown(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, { key: 'Escape' })
    fireEvent.click(anchor(container, 1))
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, { target: { value: 'first' } })

    expect(docRef.current?.comments.map((c) => [c.line, c.text])).toEqual([[1, 'first'], [2, 'second']])
    expect(docRef.current?.comments.every((c) => c.revision > 0 && c.key !== '')).toBe(true)
  })

  test('re-render with the SAME identity keeps an in-progress draft (no remount, caret preserved)', () => {
    const { container, rerender } = render(<Harness text={'# First line'} identity="same" />)
    fireEvent.click(anchor(container, 1))
    const textarea = container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'in progress' } })

    rerender(<Harness text={'# First line'} identity="same" />)
    expect((container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement).value).toBe('in progress')
  })

  test('StrictMode: opening and saving a comment does not error', () => {
    const { container } = render(
      <StrictMode>
        <Harness text={'# Line'} />
      </StrictMode>,
    )
    fireEvent.click(anchor(container, 1))
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, { target: { value: 'ok' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))
    expect(anchor(container, 1).classList.contains('has-comment')).toBe(true)
  })

  test('the form scrolls into view (block:nearest) when it opens', () => {
    const scrollSpy = vi.spyOn(window.HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})
    const { container } = render(<Harness text={'# Line'} />)

    fireEvent.click(anchor(container, 1))
    const nearestCalls = scrollSpy.mock.calls.filter(([opt]) => (opt as ScrollIntoViewOptions | undefined)?.block === 'nearest')
    expect(nearestCalls.length).toBeGreaterThan(0)
  })

  test('a list with comments on several lines renders a display+form under the block, each with the right Line N', () => {
    const { container } = render(<Harness text={'- one\n- two\n- three'} specialSections={false} />)

    // Комментарий на строке 1.
    fireEvent.click(anchor(container, 1))
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, { target: { value: 'c1' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))

    // Комментарий на строке 3.
    fireEvent.click(anchor(container, 3))
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, { target: { value: 'c3' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))

    expect(container.querySelector('.line-comment-display[data-comment-line="1"]')).not.toBeNull()
    expect(container.querySelector('.line-comment-display[data-comment-line="3"]')).not.toBeNull()
    expect(anchor(container, 1).classList.contains('has-comment')).toBe(true)
    expect(anchor(container, 3).classList.contains('has-comment')).toBe(true)
    expect(screen.getByTestId('count').textContent).toBe('2')
  })

  // ── keyboard / roving ───────────────────────────────────────────────────
  test('roving: ArrowDown/ArrowUp move focus across visible anchors', () => {
    const { container } = render(<Harness text={'- a\n- b\n- c'} specialSections={false} />)
    const a1 = anchor(container, 1)
    a1.focus()

    fireEvent.keyDown(a1, { key: 'ArrowDown' })
    expect(document.activeElement).toBe(anchor(container, 2))

    fireEvent.keyDown(anchor(container, 2), { key: 'ArrowDown' })
    expect(document.activeElement).toBe(anchor(container, 3))

    fireEvent.keyDown(anchor(container, 3), { key: 'ArrowUp' })
    expect(document.activeElement).toBe(anchor(container, 2))
  })

  test('Enter on the anchor opens the form; Enter on a nested link does not', () => {
    const { container } = render(<Harness text={'Go [here](/x) now'} />)
    const a1 = anchor(container, 1)

    fireEvent.keyDown(container.querySelector('a') as HTMLElement, { key: 'Enter' })
    expect(container.querySelector('.line-comment-form')).toBeNull()

    a1.focus()
    fireEvent.keyDown(a1, { key: 'Enter' })
    expect(container.querySelector('.line-comment-form')).not.toBeNull()
  })

  test('Esc in the form closes it and returns focus to the anchor', () => {
    const { container } = render(<Harness text={'# Line'} />)
    const a1 = anchor(container, 1)
    fireEvent.click(a1)
    const textarea = container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement
    fireEvent.keyDown(textarea, { key: 'Escape' })

    expect(container.querySelector('.line-comment-form')).toBeNull()
    expect(document.activeElement).toBe(a1)
  })

  test('saving returns focus to the anchor', () => {
    const { container } = render(<Harness text={'# Line'} />)
    const a1 = anchor(container, 1)
    fireEvent.click(a1)
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, { target: { value: 'v' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))
    expect(document.activeElement).toBe(a1)
  })

  // ── collapse ────────────────────────────────────────────────────────────
  test('special-section header is a button with aria-expanded that collapses the body (anchors go hidden, out of roving)', () => {
    const { container } = render(<Harness text={'## Assumptions\n- a\n## Other\ntext'} specialSections={true} />)

    const header = screen.getByRole('button', { name: /Assumptions/ })
    expect(header.getAttribute('aria-expanded')).toBe('true')

    fireEvent.click(header)
    expect(header.getAttribute('aria-expanded')).toBe('false')

    // Якорь списка внутри свёрнутой секции — скрыт (внутри [hidden]).
    const li = anchor(container, 2)
    expect(li.closest('[hidden]')).not.toBeNull()

    // Первый ВИДИМЫЙ якорь получает roving tabindex=0 (скрытые исключены).
    const visible = Array.from(container.querySelectorAll<HTMLElement>('[data-line]')).filter((a) => a.closest('[hidden]') === null)
    expect(visible[0]?.getAttribute('tabindex')).toBe('0')
    expect(li.getAttribute('tabindex')).toBe('-1')
  })

  test('collapse is refused while a non-empty draft is open in that section; focus goes to the header', () => {
    const { container } = render(<Harness text={'## Assumptions\n- a\n- b'} specialSections={true} />)

    fireEvent.click(anchor(container, 2))
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, { target: { value: 'unsaved' } })

    const header = screen.getByRole('button', { name: /Assumptions/ })
    fireEvent.click(header)

    // Секция осталась раскрытой, черновик виден, фокус на заголовке.
    expect(header.getAttribute('aria-expanded')).toBe('true')
    expect(anchor(container, 2).closest('[hidden]')).toBeNull()
    expect((container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement).value).toBe('unsaved')
    expect(document.activeElement).toBe(header)
  })
})

// Отдельная проверка: doc, отданный через render-prop, экспонирует hasOpenDraft.
describe('LineCommentedDocument render-prop api', () => {
  afterEach(() => vi.restoreAllMocks())

  test('hasOpenDraft flips true while a non-empty draft is open and back to false on save', () => {
    const docRef: { current: LineCommentDocumentApi | null } = { current: null }
    const { container } = render(<Harness text={'# Line'} docRef={docRef} />)

    fireEvent.click(anchor(container, 1))
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, { target: { value: 'd' } })
    expect(screen.getByTestId('open-draft').textContent).toBe('true')

    act(() => {
      fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))
    })
    expect(screen.getByTestId('open-draft').textContent).toBe('false')
    expect(docRef.current?.commentCount).toBe(1)
  })
})

// Уведомление о комментариях, оставшихся от прошлой версии документа.
describe('StaleComments', () => {
  afterEach(() => vi.restoreAllMocks())

  function StaleHarness(): ReactElement {
    const { comments, open, edit } = useComments()
    return (
      <>
        <button
          type="button"
          onClick={() => {
            open('k1', { anchor: { kind: 'plan', stageId: 's1', document: 'plan', version: 'old', line: 3 }, quote: 'old line' })
            edit('k1', 'stale text')
          }}
        >
          seed
        </button>
        <StaleComments entries={comments} source="previous version" />
      </>
    )
  }

  function renderStale() {
    return render(
      <CommentProvider runId="run-1">
        <StaleHarness />
      </CommentProvider>,
    )
  }

  test('renders nothing without entries', () => {
    const { container } = renderStale()
    expect(container.querySelector('.stale-comments')).toBeNull()
  })

  test('shows the count and source, Show reveals the old text, Dismiss discards it', () => {
    const { container } = renderStale()
    fireEvent.click(screen.getByRole('button', { name: 'seed' }))
    expect(screen.getByText('1 comment from previous version')).toBeInTheDocument()
    expect(screen.queryByText('stale text')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Show' }))
    expect(screen.getByText('stale text')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(container.querySelector('.stale-comments')).toBeNull()
  })

  test('Copy writes every old comment to the clipboard', () => {
    const writeText = vi.fn(() => Promise.resolve())
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    renderStale()
    fireEvent.click(screen.getByRole('button', { name: 'seed' }))
    fireEvent.click(screen.getByRole('button', { name: 'Copy' }))
    expect(writeText).toHaveBeenCalledWith('Line 3: stale text')
  })

  test('Copy confirms success on the button', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: () => Promise.resolve() }, configurable: true })
    renderStale()
    fireEvent.click(screen.getByRole('button', { name: 'seed' }))
    fireEvent.click(screen.getByRole('button', { name: 'Copy' }))
    expect(await screen.findByRole('button', { name: 'Copied' })).toBeInTheDocument()
  })

  test('Copy reports a failure instead of throwing when the Clipboard API is unavailable', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
    renderStale()
    fireEvent.click(screen.getByRole('button', { name: 'seed' }))
    fireEvent.click(screen.getByRole('button', { name: 'Copy' }))
    expect(await screen.findByRole('button', { name: 'Copy failed' })).toBeInTheDocument()
    expect(screen.getByText('1 comment from previous version')).toBeInTheDocument()
  })
})

// Quote preview above the comment textarea.
describe('LineCommentedDocument quote preview', () => {
  afterEach(() => vi.restoreAllMocks())

  // Distinct source lines identify paragraph, list, table, fence, and hr anchors; fence contents must not
  // become the quote.
  const quoteText = [
    'Paragraph one here',
    '',
    '- Item one',
    '- Task two: do the thing',
    '',
    '| Name | Value |',
    '|---|---|',
    '| Row two data | 2 |',
    '',
    '```js',
    'const anchorLine = 42',
    '```',
    '',
    '---',
  ].join('\n')

  function openFormAt(container: HTMLElement, line: number): HTMLElement {
    fireEvent.click(anchor(container, line))
    return container.querySelector(`.line-comment-form[data-comment-line="${line}"]`) as HTMLElement
  }

  test('(a) list item: quote shows that item source line', () => {
    const { container } = render(<Harness text={quoteText} specialSections={false} />)
    const form = openFormAt(container, 4)
    expect(form.querySelector('.line-comment-quote-src')?.textContent).toBe('- Task two: do the thing')
  })

  test('(b) table row: quote shows that row source line', () => {
    const { container } = render(<Harness text={quoteText} specialSections={false} />)
    const form = openFormAt(container, 8)
    expect(form.querySelector('.line-comment-quote-src')?.textContent).toBe('| Row two data | 2 |')
  })

  test('(c) paragraph: quote shows its own source line', () => {
    const { container } = render(<Harness text={quoteText} specialSections={false} />)
    const form = openFormAt(container, 1)
    expect(form.querySelector('.line-comment-quote-src')?.textContent).toBe('Paragraph one here')
  })

  test('(d) fenced code: quote shows the OPENING fence line, not an interior line (anchor semantics)', () => {
    const { container } = render(<Harness text={quoteText} specialSections={false} />)
    const form = openFormAt(container, 10)
    expect(form.querySelector('.line-comment-quote-src')?.textContent).toBe('```js')
  })

  test('(e) hr: no .line-comment-quote element at all', () => {
    const { container } = render(<Harness text={quoteText} specialSections={false} />)
    const form = openFormAt(container, 14)
    expect(form.querySelector('.line-comment-quote')).toBeNull()
  })

  test('quotedSourceLine: blank line and hr both resolve to empty string', () => {
    expect(quotedSourceLine(['a', '', 'b'], 2)).toBe('')
    expect(quotedSourceLine(['a', '   ', 'b'], 2)).toBe('')
    expect(quotedSourceLine(['a', '---', 'b'], 2)).toBe('')
    expect(quotedSourceLine(['a', '***', 'b'], 2)).toBe('')
    expect(quotedSourceLine(['a', '  text  ', 'b'], 2)).toBe('text')
    // out-of-range index → '' (defensive fallback, same as `?? ''`).
    expect(quotedSourceLine(['only one line'], 5)).toBe('')
  })
})

// Комментарий ставится на любую исходную строку: пустую, продолжение абзаца, строку кода, разделитель таблицы.
describe('LineCommentedDocument: comment on any source line', () => {
  afterEach(() => vi.restoreAllMocks())

  const anyLineText = [
    '# Title', //                   1
    '', //                          2
    'first paragraph line', //      3
    'continued paragraph line', //  4
    '', //                         5
    '| A | B |', //                6
    '|---|---|', //                7
    '| 1 | 2 |', //                8
    '', //                         9
    '```js', //                   10
    'const a = 1', //             11
    '', //                        12
    'const b = 2', //             13
    '```', //                     14
    '', //                        15
    '## Assumptions', //          16
    '', //                        17
    '- item', //                  18
  ].join('\n')
  const lineCount = 18

  function formAt(container: HTMLElement, line: number): HTMLElement {
    return container.querySelector(`.line-comment-form[data-comment-line="${line}"]`) as HTMLElement
  }

  test('every line 1..N has exactly one anchor', () => {
    const { container } = render(<Harness text={anyLineText} />)
    for (let line = 1; line <= lineCount; line++) {
      expect(container.querySelectorAll(`[data-line="${line}"]`), `line ${line}`).toHaveLength(1)
    }
    expect(container.querySelectorAll('[data-line]')).toHaveLength(lineCount)
  })

  // The gutter number is generated by CSS from the row's own line attribute; a table row has no attribute on its
  // cells, so the first cell repeats it. No row type may draw its number from a separate element.
  test('every line 1..N draws its number through the same attribute mechanism', () => {
    const { container } = render(<Harness text={anyLineText} />)
    expect(container.querySelector('.md-line-no')).toBeNull()
    for (let line = 1; line <= lineCount; line++) {
      const row = anchor(container, line)
      const numberSource = row.tagName === 'TR' ? row.querySelector('[data-row-line]') : row
      const attribute = row.tagName === 'TR' ? 'data-row-line' : 'data-line'
      expect(numberSource?.getAttribute(attribute), `line ${line}`).toBe(String(line))
    }
  })

  test.each([
    [2, ''], //                       blank line between blocks
    [4, 'continued paragraph line'], // continuation of a paragraph
    [7, '|---|---|'], //              table delimiter
    [12, ''], //                      blank line inside a fence
    [13, 'const b = 2'], //           fence content
    [14, '```'], //                   closing fence
    [16, '## Assumptions'], //         special section heading
    [17, ''], //                      blank line inside a special section
  ])('line %i opens a form titled with its number and quoting its source', (line, quote) => {
    const { container } = render(<Harness text={anyLineText} />)
    fireEvent.click(anchor(container, line))

    const form = formAt(container, line)
    expect(form.querySelector('.comment-title')?.textContent).toBe(`Comment on line ${line}`)
    expect(form.querySelector('.line-comment-quote-src')?.textContent ?? '').toBe(quote)
    expect(anchor(container, line).classList.contains('is-active')).toBe(true)
  })

  test('a saved comment on a blank line keeps its number, marks the row and counts', () => {
    const docRef = { current: null as LineCommentDocumentApi | null }
    const { container } = render(<Harness text={anyLineText} docRef={docRef} />)
    fireEvent.click(anchor(container, 2))
    fireEvent.change(formAt(container, 2).querySelector('textarea') as HTMLTextAreaElement, { target: { value: 'add a summary here' } })
    fireEvent.click(within(formAt(container, 2)).getByRole('button', { name: 'Save comment' }))

    expect(anchor(container, 2).classList.contains('has-comment')).toBe(true)
    expect(container.querySelector('.comment-card[data-comment-line="2"] .comment-title')?.textContent).toBe('Comment on line 2')
    expect(docRef.current?.comments).toEqual([expect.objectContaining({ line: 2, text: 'add a summary here' })])
  })

  test('comments on several kinds of lines are listed in source order', () => {
    const docRef = { current: null as LineCommentDocumentApi | null }
    const { container } = render(<Harness text={anyLineText} docRef={docRef} />)
    for (const line of [13, 2, 4]) {
      fireEvent.click(anchor(container, line))
      fireEvent.change(formAt(container, line).querySelector('textarea') as HTMLTextAreaElement, { target: { value: `note ${line}` } })
      fireEvent.click(within(formAt(container, line)).getByRole('button', { name: 'Save comment' }))
    }
    expect(docRef.current?.comments.map((comment) => [comment.line, comment.text])).toEqual([
      [2, 'note 2'],
      [4, 'note 4'],
      [13, 'note 13'],
    ])
  })

  test('arrow keys visit every line in source order', () => {
    const { container } = render(<Harness text={anyLineText} />)
    anchor(container, 1).focus()
    const visited = [1]
    for (let step = 1; step < lineCount; step++) {
      fireEvent.keyDown(document.activeElement as HTMLElement, { key: 'ArrowDown' })
      visited.push(Number((document.activeElement as HTMLElement).getAttribute('data-line')))
    }
    expect(visited).toEqual(Array.from({ length: lineCount }, (_, i) => i + 1))
  })

  test('a link inside a continuation line still opens the link, not the form', () => {
    const { container } = render(<Harness text={'one\ntwo [here](/x)'} specialSections={false} />)
    fireEvent.click(container.querySelector('a') as HTMLElement)
    expect(container.querySelector('.line-comment-form')).toBeNull()
    fireEvent.click(anchor(container, 2))
    expect(formAt(container, 2)).not.toBeNull()
  })
})

// Тот же quote-блок, что и у открытой формы, но у УЖЕ СОХРАНЁННОГО комментария — чтобы читатель видел, к
// какой строке привязан коммент, не открывая форму.
describe('LineCommentedDocument saved-comment quote', () => {
  afterEach(() => vi.restoreAllMocks())

  const listText = ['- Item one', '- Task two: do the thing', '- Item three'].join('\n')

  function saveCommentAt(container: HTMLElement, line: number, text: string): void {
    fireEvent.click(anchor(container, line))
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, {
      target: { value: text },
    })
    fireEvent.click(screen.getByRole('button', { name: /^(Save|Update) comment$/ }))
  }

  test('a saved comment quotes its source line above the comment text', () => {
    const { container } = render(<Harness text={listText} specialSections={false} />)
    saveCommentAt(container, 1, 'please fix this item')

    const display = container.querySelector('.line-comment-display[data-comment-line="1"]') as HTMLElement
    expect(display).not.toBeNull()
    const quote = display.querySelector('.line-comment-quote-src') as HTMLElement
    expect(quote?.textContent).toBe('- Item one')

    // Цитата — ВЫШЕ текста комментария в том же контейнере.
    const commentText = within(display).getByText('please fix this item')
    expect(quote.compareDocumentPosition(commentText) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  test('each saved card quotes ITS OWN line while the open form quotes a third line', () => {
    const { container } = render(<Harness text={listText} specialSections={false} />)
    saveCommentAt(container, 1, 'c1')
    saveCommentAt(container, 2, 'c2')

    // Открываем форму на третьей строке (ещё без коммента).
    fireEvent.click(anchor(container, 3))

    const card1 = container.querySelector('.line-comment-display[data-comment-line="1"]') as HTMLElement
    const card2 = container.querySelector('.line-comment-display[data-comment-line="2"]') as HTMLElement
    const form = container.querySelector('.line-comment-form[data-comment-line="3"]') as HTMLElement

    expect(card1.querySelector('.line-comment-quote-src')?.textContent).toBe('- Item one')
    expect(card2.querySelector('.line-comment-quote-src')?.textContent).toBe('- Task two: do the thing')
    expect(form.querySelector('.line-comment-quote-src')?.textContent).toBe('- Item three')
  })

  test('a saved comment on a horizontal-rule line renders NO quote block', () => {
    const { container } = render(<Harness text={['first paragraph', '', '---'].join('\n')} specialSections={false} />)
    // hr — строка 3.
    saveCommentAt(container, 3, 'why is there a rule here')

    const display = container.querySelector('.line-comment-display[data-comment-line="3"]') as HTMLElement
    expect(display).not.toBeNull()
    expect(display.querySelector('.line-comment-quote')).toBeNull()
  })

  test('editing an existing comment shows exactly one quote (the form), no stale display card', () => {
    const { container } = render(<Harness text={listText} specialSections={false} />)
    saveCommentAt(container, 2, 'first version')

    // Кликаем ту же строку — открываем форму на редактирование.
    fireEvent.click(anchor(container, 2))

    // Карточка активной строки НЕ рендерится — остаётся только форма.
    expect(container.querySelector('.line-comment-display[data-comment-line="2"]')).toBeNull()
    const form = container.querySelector('.line-comment-form[data-comment-line="2"]') as HTMLElement
    expect(form).not.toBeNull()
    // Ровно одна цитата на всю ленту (формы), не задвоенная.
    expect(container.querySelectorAll('.line-comment-quote').length).toBe(1)
    expect(form.querySelector('.line-comment-quote-src')?.textContent).toBe('- Task two: do the thing')
    // Форма открылась именно на РЕДАКТИРОВАНИЕ — draft содержит сохранённый текст.
    const textarea = form.querySelector('textarea') as HTMLTextAreaElement
    expect(textarea.value).toBe('first version')

    // Закрытие формы (Escape) возвращает карточку с цитатой на место.
    fireEvent.keyDown(textarea, { key: 'Escape' })
    const card = container.querySelector('.line-comment-display[data-comment-line="2"]') as HTMLElement
    expect(card).not.toBeNull()
    expect(card.querySelector('.line-comment-quote-src')?.textContent).toBe('- Task two: do the thing')
    // Форма редактирования закрыта. NB: карточка тоже имеет класс
    // .line-comment-form, поэтому форму отличаем через :not(.line-comment-display).
    expect(container.querySelector('.line-comment-form:not(.line-comment-display)[data-comment-line="2"]')).toBeNull()
  })
})

// Общий CommentCard/CommentEditor: тот же текст, клавиатура и Escape, что и в других контекстах.
describe('LineCommentedDocument shared card and editor', () => {
  afterEach(() => vi.restoreAllMocks())

  function saveCommentAt(container: HTMLElement, line: number, text: string): void {
    fireEvent.click(anchor(container, line))
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, { target: { value: text } })
    fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))
  }

  test('Escape in the editor stops before document-level handlers', () => {
    const documentKey = vi.fn()
    const { container } = render(<Harness text={'First line\nSecond line'} />)
    fireEvent.click(anchor(container, 1))
    document.addEventListener('keydown', documentKey)
    fireEvent.keyDown(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, { key: 'Escape' })
    document.removeEventListener('keydown', documentKey)
    expect(container.querySelector('.line-comment-form')).toBeNull()
    expect(documentKey).not.toHaveBeenCalled()
  })

  test('Cmd/Ctrl+Enter saves the comment', () => {
    const { container } = render(<Harness text={'First line'} />)
    fireEvent.click(anchor(container, 1))
    const textarea = container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'kbd save' } })
    fireEvent.keyDown(textarea, { key: 'Enter', metaKey: true })
    expect(container.querySelector('.line-comment-display[data-comment-line="1"]')).not.toBeNull()
  })

  test('card shows full multi-line text and Edit reopens the editor with it', () => {
    const { container } = render(<Harness text={'First line'} />)
    saveCommentAt(container, 1, 'alpha\nbeta')
    const card = container.querySelector('.line-comment-display[data-comment-line="1"]') as HTMLElement
    expect(card.querySelector('.comment-text')?.textContent).toBe('alpha\nbeta')
    fireEvent.click(within(card).getByRole('button', { name: 'Edit comment on line 1' }))
    expect((container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement).value).toBe('alpha\nbeta')
  })

  test('quote is the raw source line as plain text', () => {
    const { container } = render(<Harness text={'Use <b>bold</b> here'} specialSections={false} />)
    fireEvent.click(anchor(container, 1))
    const quote = container.querySelector('.line-comment-quote-src') as HTMLElement
    expect(quote.textContent).toBe('Use <b>bold</b> here')
    expect(quote.querySelector('b')).toBeNull()
  })

  test('plan editor keeps the Attach affordance', () => {
    const { container } = render(<Harness text={'First line'} />)
    fireEvent.click(anchor(container, 1))
    expect(screen.getByRole('button', { name: 'Attach' })).toBeInTheDocument()
  })
})
