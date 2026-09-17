import { describe, expect, test } from 'vitest'
import { escapeHtml, highlight, splitHighlightedLines } from './highlight'

describe('highlight', () => {
  test('highlights a registered language and wraps recognized tokens', () => {
    const html = highlight('go', 'package main')
    expect(html).toContain('hljs-keyword')
    expect(html).toContain('package')
  })

  test('highlights yaml', () => {
    const html = highlight('yaml', 'name: build')
    expect(html).toContain('hljs-attr')
  })

  test('highlights markdown', () => {
    const html = highlight('markdown', '# Title')
    expect(html).toContain('hljs-section')
  })

  test('highlights bash', () => {
    const html = highlight('bash', 'echo "$HOME"')
    expect(html).toContain('hljs-built_in')
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
    expect(splitHighlightedLines('a\nb\nc', 'a\nb\nc')).toEqual(['a', 'b', 'c'])
  })

  test('a span fully contained in one line stays on that line only', () => {
    const html = 'x<span class="hljs-keyword">go</span>y\nplain'
    expect(splitHighlightedLines(html, 'xgoy\nplain')).toEqual(['x<span class="hljs-keyword">go</span>y', 'plain'])
  })

  test('re-balances a span that spans multiple lines so every line is self-contained', () => {
    const html = 'a<span class="hljs-comment">/*\nmulti\nline*/</span>b'
    expect(splitHighlightedLines(html, 'a/*\nmulti\nline*/b')).toEqual([
      'a<span class="hljs-comment">/*</span>',
      '<span class="hljs-comment">multi</span>',
      '<span class="hljs-comment">line*/</span>b',
    ])
  })

  // Review-finding P2 #4: a file that ends in a trailing newline must NOT
  // produce a phantom empty final line — the backend counts only real lines
  // (TrimSuffix(content,"\n")), so a note on the phantom line always fails
  // stale_line. Line count here must match the backend's exactly.
  test('drops the phantom trailing line for a file ending in a newline', () => {
    expect(splitHighlightedLines('a\nb\n', 'a\nb\n')).toEqual(['a', 'b'])
  })

  test('a file without a trailing newline keeps every line', () => {
    expect(splitHighlightedLines('a\nb', 'a\nb')).toEqual(['a', 'b'])
  })

  test('an empty file has zero lines (matches the backend)', () => {
    expect(splitHighlightedLines('', '')).toEqual([])
  })

  // Re-review P2: the trailing-line decision must come from the SOURCE, not the
  // HTML. A source ending in a newline whose final newline sits INSIDE a
  // multi-line token highlights to HTML that does NOT end in '\n'
  // ('<span ...>/* hi\n</span>'), so an html.endsWith('\n') check would wrongly
  // keep a phantom second line. With the source known, it collapses to one line.
  test('multi-line token with a trailing source newline yields one line, not two', () => {
    const html = '<span class="hljs-comment">/* hi\n</span>'
    expect(splitHighlightedLines(html, '/* hi\n')).toEqual(['<span class="hljs-comment">/* hi</span>'])
  })

  // Source that is exactly a single newline: the backend counts zero real lines.
  test('a source of only a newline has zero lines', () => {
    expect(splitHighlightedLines('\n', '\n')).toEqual([])
  })
})
