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
