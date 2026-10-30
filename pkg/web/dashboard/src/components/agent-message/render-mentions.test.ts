import { describe, expect, it } from 'vitest'
import { renderPlainMarkdown } from '../plan-panel/markdown'
import { highlightMentions } from './render-mentions'

describe('message mentions', () => {
  it('highlights known handles in prose while preserving code, links, emails and longer aliases', () => {
    const html = highlightMentions(renderPlainMarkdown('@QA asks **@impl**. `@qa` [@qa](https://example.com) user@qa.test @qa-other @unknown'), ['qa', 'impl'])
    const doc = document.createElement('div')
    doc.innerHTML = html
    expect([...doc.querySelectorAll('.message-mention')].map((n) => n.textContent)).toEqual(['@QA', '@impl'])
    expect(doc.querySelector('code')?.textContent).toBe('@qa')
    expect(doc.querySelector('a')?.textContent).toBe('@qa')
    expect(doc.querySelector('a')?.getAttribute('href')).toBe('https://example.com')
    expect(doc.querySelector('strong .message-mention')).not.toBeNull()
  })

  it('escapes regex characters in real handles without enabling HTML from the message or handle', () => {
    const html = highlightMentions(renderPlainMarkdown('@run:qa+2 <img src=x onerror=alert(1)>'), ['run:qa+2', '<script>'])
    const doc = document.createElement('div')
    doc.innerHTML = html
    expect(doc.querySelector('.message-mention')?.textContent).toBe('@run:qa+2')
    expect(doc.querySelector('img,script')).toBeNull()
    expect(doc.textContent).toContain('<img src=x onerror=alert(1)>')
  })
})
