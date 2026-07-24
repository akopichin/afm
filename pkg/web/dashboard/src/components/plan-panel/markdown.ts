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

// Единица построчного (комментируемого) рендера: либо обычная строка, либо
// многострочный блок (fenced-код / markdown-таблица), отрендеренный целиком и
// заякоренный на своей ПЕРВОЙ исходной строке. Номер строки сохраняется, поэтому
// клик-по-строке и цитирование в feedback продолжают работать. Блоки нужны, чтобы
// код и таблицы не разваливались построчно (renderInline не понимает блочную
// разметку) — из-за этого в диалоге «резался» yaml-контракт, а в review-плане
// таблицы превращались в мешанину.
export type LineBlock = { line: number; html: string }

// Строка таблицы — начинается с `|` (лидирующий пайп; консервативно, чтобы обычная
// проза с одиночным `|` не опозналась как таблица).
function isTableRow(line: string | undefined): boolean {
  return line !== undefined && line.trim().startsWith('|')
}

// Разделитель заголовка таблицы: строка из `|`, `-`, `:` и пробелов, с хотя бы
// одним `-` (напр. `|---|:--:|`). Именно он отличает таблицу от простых `|`-строк.
function isTableDelimiter(line: string | undefined): boolean {
  if (line === undefined) return false
  const trimmed = line.trim()
  return trimmed.includes('|') && trimmed.includes('-') && /^[|\-:\s]+$/.test(trimmed)
}

function isTableStart(lines: string[], i: number): boolean {
  return isTableRow(lines[i]) && isTableDelimiter(lines[i + 1])
}

// Полный блочный рендер многострочного фрагмента (код/таблица) через markdown-it —
// в отличие от formatLine, который форматирует одну строку инлайн.
function renderBlock(text: string): string {
  return decorateCheckboxes(md.render(text))
}

// nextLineBlock возвращает блок, начинающийся на lines[i], и индекс строки за ним.
// Fenced-код (```) и таблицы схлопываются в один блок; всё прочее — одна строка.
export function nextLineBlock(lines: string[], i: number): { block: LineBlock; next: number } {
  const first = lines[i] ?? ''

  // Fenced-код: собираем до закрывающего ``` (или до конца при незакрытом блоке).
  if (first.trim().startsWith('```')) {
    const buffer = [first]
    let j = i + 1
    for (; j < lines.length; j++) {
      const line = lines[j] ?? ''
      buffer.push(line)
      if (line.trim().startsWith('```')) {
        j++
        break
      }
    }
    return { block: { line: i + 1, html: renderBlock(buffer.join('\n')) }, next: j }
  }

  // Markdown-таблица: строка-заголовок + строка-разделитель, затем строки тела.
  if (isTableStart(lines, i)) {
    const buffer = [first, lines[i + 1] ?? '']
    let j = i + 2
    for (; j < lines.length && isTableRow(lines[j]); j++) {
      buffer.push(lines[j] ?? '')
    }
    return { block: { line: i + 1, html: renderBlock(buffer.join('\n')) }, next: j }
  }

  return { block: { line: i + 1, html: formatLine(first) }, next: i + 1 }
}

// Разбивает текст на построчные блоки (обычные строки + схлопнутые код/таблицы).
// Общая основа для pending-вопроса диалога и review-плана.
export function parseLineBlocks(text: string): LineBlock[] {
  const lines = text.split('\n')
  const blocks: LineBlock[] = []
  for (let i = 0; i < lines.length; ) {
    const { block, next } = nextLineBlock(lines, i)
    blocks.push(block)
    i = next
  }
  return blocks
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
