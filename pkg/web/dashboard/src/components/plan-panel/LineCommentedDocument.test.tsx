import { act, fireEvent, render, screen } from '@testing-library/react'
import { StrictMode, type ReactElement } from 'react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { FileBrowserProvider } from '../file-browser'
import { LineCommentedDocument, quotedSourceLine, type LineCommentDocumentApi } from './LineCommentedDocument'

// Тесты слоя ВЗАИМОДЕЙСТВИЯ (useLineComments) через реальный DOM владельца
// LineCommentedDocument: клик/hover/keyboard/выделение, императивные классы
// has-comment/is-active/roving tabindex, collapse спец-секций, атомарный reset.

type HarnessProps = {
  text: string
  specialSections?: boolean
  identity?: string
  beforeOpen?: () => void
  docRef?: { current: LineCommentDocumentApi | null }
}

function Harness({ text, specialSections = true, identity = 'doc-1', beforeOpen, docRef }: HarnessProps): ReactElement {
  return (
    <FileBrowserProvider flowName="flow1" startedAt="t1" enabled={true}>
      <LineCommentedDocument
        key={identity}
        text={text}
        specialSections={specialSections}
        stageId="s1"
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
      </LineCommentedDocument>
    </FileBrowserProvider>
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

  // Примечание: jsdom не реализует PointerEvent (нет clientX), поэтому
  // координатный drag-порог здесь драйвить нельзя. Реальный drag по тексту
  // создаёт выделение — этот сценарий покрыт тестом selection выше. Здесь
  // проверяем, что pointer-трекинг НЕ пере-подавляет обычный клик.
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
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))

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
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))
    expect(a1.classList.contains('has-comment')).toBe(true)

    // Открываем форму на строке 2 (ре-рендер владельца без смены текста) —
    // has-comment на строке 1 восстанавливается императивным слоем.
    fireEvent.click(anchor(container, 2))
    expect(a1.classList.contains('has-comment')).toBe(true)
  })

  test('changing documentIdentity atomically resets all comment state', () => {
    const { container, rerender } = render(<Harness text={'# First line'} identity="a" />)
    fireEvent.click(anchor(container, 1))
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, { target: { value: 'x' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))
    expect(screen.getByTestId('count').textContent).toBe('1')

    // Смена identity (key) → remount → всё обнулено.
    rerender(<Harness text={'# First line'} identity="b" />)
    expect(screen.getByTestId('count').textContent).toBe('0')
    expect(container.querySelector('.line-comment-form')).toBeNull()
    expect(anchor(container, 1).classList.contains('has-comment')).toBe(false)
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
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))
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
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))

    // Комментарий на строке 3.
    fireEvent.click(anchor(container, 3))
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, { target: { value: 'c3' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))

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
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))
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
      fireEvent.click(screen.getByRole('button', { name: 'Add' }))
    })
    expect(screen.getByTestId('open-draft').textContent).toBe('false')
    expect(docRef.current?.commentCount).toBe(1)
  })
})

// Quote preview above the comment textarea (messenger-style, big faint accent
// quotation mark). Anchor semantics (see task brief): data-line — 1-based,
// каждый li/tr/paragraph/heading/hr/fence якорится на СВОЕЙ стартовой строке —
// поэтому quotedLine всегда равен именно этой строке, а не «содержательной»
// внутренней строке блока (см. кейс fence ниже).
describe('LineCommentedDocument quote preview', () => {
  afterEach(() => vi.restoreAllMocks())

  // Один текст с явно узнаваемыми, различимыми строками для каждого типа блока:
  //  1: paragraph
  //  2: (blank — separator)
  //  3: list item one
  //  4: list item two   ← (a)
  //  5: (blank)
  //  6: table header row
  //  7: table delimiter (no anchor)
  //  8: table data row  ← (b)
  //  9: (blank)
  // 10: fence opening ``` ← (d)
  // 11: fence interior line (must NOT be the quote)
  // 12: fence closing ```
  // 13: (blank)
  // 14: hr             ← (e)
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

  // blank line — не достижимо через клик (пустая строка никогда не получает
  // якорь в renderAnchoredSections), поэтому проверяем чистую функцию напрямую.
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
