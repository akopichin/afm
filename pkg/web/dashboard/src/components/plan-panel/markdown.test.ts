import { describe, expect, test } from 'vitest'
import {
  escapeHtml,
  formatLine,
  isHeading2,
  isSpecialSection,
  renderInline,
  renderMarkdown,
} from './markdown'

// Покрытие поведенческого порта markdown-рендера из app.js (renderMarkdownHTML + decorate* +
// formatLine/inlineFormat). Функции чистые — DOM/fetch не нужен.

describe('renderMarkdown', () => {
  test('пустой ввод возвращает пустую строку', () => {
    expect(renderMarkdown('')).toBe('')
    expect(renderMarkdown('   \n  ')).toBe('')
  })

  test('рендерит параграф', () => {
    expect(renderMarkdown('Hello world')).toContain('<p>Hello world</p>')
  })

  test('декорирует чекбоксы [x]/[ ]', () => {
    const html = renderMarkdown('- [x] done\n- [ ] todo')
    expect(html).toContain('cb cb-done')
    expect(html).toContain('cb cb-open')
  })

  test('оборачивает спецсекции в сворачиваемые обёртки и закрывает их', () => {
    const html = renderMarkdown('## Assumptions\n- none\n## Acceptance Criteria\n- one')
    expect(html).toContain('plan-section-wrapper plan-section-assumptions')
    expect(html).toContain('plan-section-wrapper plan-section-criteria')
    expect(html).toContain('⚠ Assumptions')
    expect(html).toContain('✓ Acceptance Criteria')
    // обе обёртки должны быть закрыты (</div></div> на каждую)
    expect((html.match(/<\/div><\/div>/g) ?? []).length).toBe(2)
  })

  test('сохраняет fenced code-блоки', () => {
    const html = renderMarkdown('```\ncode line\n```')
    expect(html).toContain('code line')
    expect(html).toContain('<code>')
  })
})

describe('renderInline', () => {
  test('рендерит инлайн-разметку без блочных тегов', () => {
    expect(renderInline('**bold**')).toContain('<strong>bold</strong>')
  })

  test('декорирует чекбоксы инлайн', () => {
    expect(renderInline('[x]')).toContain('cb cb-done')
  })
})

describe('formatLine', () => {
  test('форматирует заголовки по уровням', () => {
    expect(formatLine('# Title')).toBe('<h1>Title</h1>')
    expect(formatLine('## Section')).toBe('<h2>Section</h2>')
    expect(formatLine('### Sub')).toBe('<h3>Sub</h3>')
  })

  test('форматирует элементы списка, срезая - и *', () => {
    expect(formatLine('- item')).toBe('<li>item</li>')
    expect(formatLine('* item')).toBe('<li>item</li>')
  })

  test('пустая строка становится неразрывным пробелом', () => {
    expect(formatLine('')).toBe('&nbsp;')
  })

  test('обычные строки становятся параграфами', () => {
    expect(formatLine('plain text')).toBe('<p>plain text</p>')
  })
})

describe('escapeHtml', () => {
  test('экранирует &, <, >', () => {
    expect(escapeHtml('a & b < c > d')).toBe('a &amp; b &lt; c &gt; d')
  })
})

describe('хелперы секций', () => {
  test('isSpecialSection опознаёт известные секции и отбрасывает прочие', () => {
    expect(isSpecialSection('## Assumptions')?.label).toBe('Assumptions')
    expect(isSpecialSection('## Acceptance Criteria')?.label).toBe('Acceptance Criteria')
    expect(isSpecialSection('## Other')).toBeNull()
  })

  test('isHeading2 срабатывает только на строки вида "## …"', () => {
    expect(isHeading2('## Heading')).toBe(true)
    expect(isHeading2('### Not h2')).toBe(false)
    expect(isHeading2('text')).toBe(false)
  })
})
