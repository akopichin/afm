import { describe, expect, test } from 'vitest'
import { escapeHtml, highlight } from './highlight'

describe('highlight', () => {
  test('highlights a registered language and wraps recognized tokens', () => {
    const html = highlight('go', 'package main')
    expect(html).toContain('hljs-keyword')
    expect(html).toContain('package')
  })

  test('falls back to escaped plain text for language "plain"', () => {
    const html = highlight('plain', '<b>hi</b>')
    expect(html).toBe('&lt;b&gt;hi&lt;/b&gt;')
  })

  test('falls back to escaped plain text for an unregistered language', () => {
    const html = highlight('rust', 'fn main() {}')
    expect(html).toBe(escapeHtml('fn main() {}'))
  })

  test('escapes HTML-significant characters in the source, never executes them', () => {
    const html = highlight('plain', '<img src=x onerror="window.__pwned=1">')
    expect(html).not.toContain('<img')
    expect(html).toContain('&lt;img')
  })

  test('escapeHtml escapes &, <, >', () => {
    expect(escapeHtml('a & b < c > d')).toBe('a &amp; b &lt; c &gt; d')
  })
})
