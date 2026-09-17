import { describe, expect, test } from 'vitest'
import {
  blockSpans,
  escapeHtml,
  formatLine,
  isHeading2,
  isSpecialSection,
  parseLineBlocks,
  renderInline,
  renderMarkdown,
  renderPlainMarkdown,
} from './markdown'

// Покрытие поведенческого порта markdown-рендера из app.js (renderMarkdownHTML + decorate* +
// formatLine/inlineFormat). Функции чистые — DOM/fetch не нужен.

// renderPlainMarkdown — нейтральный рендерер для ленты: тот же markdown-it, но БЕЗ
// plan-специфики (спец-секции, decorateCheckboxes). Именно его использует нарратив
// агента в FeedWorkspace.
describe('renderPlainMarkdown', () => {
  test('пустой ввод возвращает пустую строку', () => {
    expect(renderPlainMarkdown('')).toBe('')
    expect(renderPlainMarkdown('   \n  ')).toBe('')
  })

  test('рендерит блочный markdown: заголовок, жирный, inline code, таблицу', () => {
    const html = renderPlainMarkdown(
      '## Итог\n\n**жирно** и `code`\n\n| a | b |\n|---|---|\n| 1 | 2 |',
    )
    expect(html).toContain('<h2>')
    expect(html).toContain('<strong>жирно</strong>')
    expect(html).toContain('<code>code</code>')
    expect(html).toContain('<table>')
    expect(html).toContain('<th>a</th>')
    expect(html).toContain('<td>1</td>')
  })

  test('НЕ применяет plan-специфику: спецсекции и decorateCheckboxes', () => {
    const html = renderPlainMarkdown('## Assumptions\n- none\n\n- [x] done `[x]` inside')
    // спец-секция плана не оборачивается
    expect(html).not.toContain('plan-section-wrapper')
    expect(html).toContain('<h2>Assumptions</h2>')
    // чекбоксы не декорируются (в т.ч. внутри inline code)
    expect(html).not.toContain('cb-done')
    expect(html).not.toContain('cb-open')
  })

  test('безопасность: raw HTML экранируется, javascript:-ссылка не становится href', () => {
    const imgHtml = renderPlainMarkdown('<img src=x onerror=alert(1)>')
    expect(imgHtml).not.toContain('<img')
    expect(imgHtml).toContain('&lt;img')
    const linkHtml = renderPlainMarkdown('[x](javascript:alert(1))')
    expect(linkHtml).not.toContain('href="javascript:')
  })
})

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

// parseLineBlocks — тонкий алиас над blockSpans (используется DialogChannel),
// поэтому оба имени покрываются одним набором кейсов; часть тестов дублирует
// вызов через оба имени, чтобы зафиксировать, что сигнатуры остаются идентичны.
describe('blockSpans / parseLineBlocks — сегментация по markdown-it token.map', () => {
  test('обёрнутый многострочный параграф — ОДИН <p>, заякоренный на первой строке', () => {
    // До block-сегментации построчное сканирование резало это на 3 отдельных
    // <p> — по CommonMark это один параграф (lazy continuation внутри абзаца).
    const blocks = blockSpans('line one\nline two\nline three')
    expect(blocks).toHaveLength(1)
    expect(blocks[0]).toMatchObject({ line: 1 })
    expect(blocks[0]?.html).toContain('line one')
    expect(blocks[0]?.html).toContain('line two')
    expect(blocks[0]?.html).toContain('line three')
    expect((blocks[0]?.html.match(/<p>/g) ?? []).length).toBe(1)
  })

  test('нумерованный список (1./2.) — один <ol>, заякоренный на первой строке', () => {
    const blocks = blockSpans('1. one\n2. two')
    expect(blocks).toHaveLength(1)
    expect(blocks[0]).toMatchObject({ line: 1 })
    expect(blocks[0]?.html).toContain('<ol>')
    expect(blocks[0]?.html).toContain('<li>one</li>')
    expect(blocks[0]?.html).toContain('<li>two</li>')
  })

  test('маркированный список (-) — один <ul>, заякоренный на первой строке', () => {
    const blocks = parseLineBlocks('- a\n- b')
    expect(blocks).toHaveLength(1)
    expect(blocks[0]).toMatchObject({ line: 1 })
    expect(blocks[0]?.html).toContain('<ul>')
    expect(blocks[0]?.html).toContain('<li>a</li>')
    expect(blocks[0]?.html).toContain('<li>b</li>')
  })

  test('вложенный список — один блок верхнего уровня, вложенный <ul> не дублируется', () => {
    const blocks = blockSpans('- a\n  - nested\n- b')
    expect(blocks).toHaveLength(1)
    expect(blocks[0]).toMatchObject({ line: 1 })
    // Ровно два <ul> — внешний и один вложенный, без повторного рендера.
    expect((blocks[0]?.html.match(/<ul>/g) ?? []).length).toBe(2)
    expect(blocks[0]?.html).toContain('nested')
  })

  test('loose-список (пустая строка между пунктами) — один блок, оба пункта внутри', () => {
    const blocks = blockSpans('- a\n\n- b')
    expect(blocks).toHaveLength(1)
    expect(blocks[0]).toMatchObject({ line: 1 })
    expect(blocks[0]?.html).toContain('<li>')
    expect(blocks[0]?.html).toContain('a')
    expect(blocks[0]?.html).toContain('b')
  })

  test('lazy continuation ("- a"⏎"b") — один пункт списка, вторая строка внутри него', () => {
    const blocks = blockSpans('- a\nb')
    expect(blocks).toHaveLength(1)
    expect(blocks[0]).toMatchObject({ line: 1 })
    expect(blocks[0]?.html).toContain('<ul>')
    expect(blocks[0]?.html).toContain('a')
    expect(blocks[0]?.html).toContain('b')
    expect((blocks[0]?.html.match(/<li>/g) ?? []).length).toBe(1)
  })

  test('параграф + "- item" — ДВА блока: параграф и список', () => {
    const blocks = blockSpans('para\n- item')
    expect(blocks).toHaveLength(2)
    expect(blocks[0]).toMatchObject({ line: 1 })
    expect(blocks[0]?.html).toContain('<p>para</p>')
    expect(blocks[1]).toMatchObject({ line: 2 })
    expect(blocks[1]?.html).toContain('<ul>')
    expect(blocks[1]?.html).toContain('item')
  })

  test('параграф + "2. item" — ОДИН блок-параграф ("2." посреди параграфа — не начало списка)', () => {
    const blocks = blockSpans('para\n2. item')
    expect(blocks).toHaveLength(1)
    expect(blocks[0]).toMatchObject({ line: 1 })
    expect(blocks[0]?.html).not.toContain('<ol>')
    expect(blocks[0]?.html).toContain('para')
    expect(blocks[0]?.html).toContain('2. item')
  })

  test('blockquote с продолжением на второй строке — один блок <blockquote>', () => {
    const blocks = blockSpans('> quote\n> cont')
    expect(blocks).toHaveLength(1)
    expect(blocks[0]).toMatchObject({ line: 1 })
    expect(blocks[0]?.html).toContain('<blockquote>')
    expect(blocks[0]?.html).toContain('quote')
    expect(blocks[0]?.html).toContain('cont')
  })

  test('blockquote, затем параграф — ДВА блока', () => {
    const blocks = blockSpans('> quote\n\npara')
    expect(blocks).toHaveLength(2)
    expect(blocks[0]).toMatchObject({ line: 1 })
    expect(blocks[0]?.html).toContain('<blockquote>')
    expect(blocks[1]).toMatchObject({ line: 3 }) // после пустой строки-разделителя
    expect(blocks[1]?.html).toContain('<p>para</p>')
  })

  test('fenced-код схлопывается в один блок <pre>, заякоренный на строке ```', () => {
    // Регресс #1: раньше в диалоге код-блок разваливался построчно (```diff как
    // отдельный <p>), и yaml-контракт «резался». Теперь это один <pre>.
    const blocks = parseLineBlocks('Before\n```diff\n-old\n+new\n```\nAfter')
    expect(blocks).toHaveLength(3) // Before | код-блок | After
    expect(blocks[0]).toMatchObject({ line: 1 })
    expect(blocks[1]?.line).toBe(2) // блок заякорен на строке открывающего ```
    expect(blocks[1]?.html).toContain('<pre>')
    expect(blocks[1]?.html).toContain('-old')
    expect(blocks[1]?.html).toContain('+new')
    expect(blocks[2]).toMatchObject({ line: 6 }) // After — после закрывающего ```
    expect(blocks[2]?.html).toContain('After')
  })

  test('markdown-таблица схлопывается в один блок <table>', () => {
    // Регресс #4: раньше таблица в review-плане рендерилась «мешаниной» — каждая
    // |-строка отдельным <p>, разделитель |---| голым текстом.
    const blocks = parseLineBlocks('| A | B |\n|---|---|\n| 1 | 2 |')
    expect(blocks).toHaveLength(1)
    expect(blocks[0]).toMatchObject({ line: 1 })
    expect(blocks[0]?.html).toContain('<table>')
    expect(blocks[0]?.html).toContain('<td>1</td>')
  })

  test('одиночная |-строка без разделителя таблицей не считается', () => {
    const blocks = parseLineBlocks('value | with pipe')
    expect(blocks).toHaveLength(1)
    expect(blocks[0]?.html).not.toContain('<table>')
  })

  test('заголовок и пустая строка — один блок-заголовок', () => {
    const blocks = blockSpans('## Heading\n')
    expect(blocks).toHaveLength(1)
    expect(blocks[0]).toMatchObject({ line: 1 })
    expect(blocks[0]?.html).toContain('<h2>Heading</h2>')
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
