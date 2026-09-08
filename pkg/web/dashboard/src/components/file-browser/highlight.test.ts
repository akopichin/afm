import { describe, expect, test } from 'vitest'
import { escapeHtml, highlight, splitHighlightedLines } from './highlight'

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

describe('splitHighlightedLines', () => {
  test('splits plain (no-tag) HTML one entry per line', () => {
    expect(splitHighlightedLines('a\nb\nc')).toEqual(['a', 'b', 'c'])
  })

  test('a span fully contained in one line stays on that line only', () => {
    const html = 'x<span class="hljs-keyword">go</span>y\nplain'
    expect(splitHighlightedLines(html)).toEqual(['x<span class="hljs-keyword">go</span>y', 'plain'])
  })

  test('re-balances a span that spans multiple lines so every line is self-contained', () => {
    const html = 'a<span class="hljs-comment">/*\nmulti\nline*/</span>b'
    expect(splitHighlightedLines(html)).toEqual([
      'a<span class="hljs-comment">/*</span>',
      '<span class="hljs-comment">multi</span>',
      '<span class="hljs-comment">line*/</span>b',
    ])
  })
})
