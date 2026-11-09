import { describe, expect, test } from 'vitest'
import { createMarkdownCache, renderPlainMarkdown, renderPlainMarkdownCached } from './markdown'

describe('renderPlainMarkdownCached', () => {
  test('returns the same HTML as renderPlainMarkdown', () => {
    const text = '## Title\n\n**bold** and `code`'
    expect(renderPlainMarkdownCached(text)).toBe(renderPlainMarkdown(text))
    expect(renderPlainMarkdownCached('   ')).toBe('')
  })

  test('renders a repeated text once', () => {
    let calls = 0
    const cache = createMarkdownCache((text) => { calls++; return `<p>${text}</p>` }, { maxEntries: 10, maxChars: 1000 })
    expect(cache('a')).toBe('<p>a</p>')
    expect(cache('a')).toBe('<p>a</p>')
    expect(calls).toBe(1)
  })

  test('evicts the least recently used entry past the entry limit', () => {
    let calls = 0
    const cache = createMarkdownCache((text) => { calls++; return text }, { maxEntries: 2, maxChars: 1000 })
    cache('a')
    cache('b')
    cache('a') // a is now the most recent
    cache('c') // evicts b
    calls = 0
    cache('a')
    expect(calls).toBe(0)
    cache('b')
    expect(calls).toBe(1)
  })

  test('evicts old entries past the total input size limit', () => {
    let calls = 0
    const cache = createMarkdownCache((text) => { calls++; return text }, { maxEntries: 100, maxChars: 10 })
    cache('aaaaaa')
    cache('bbbbbb') // 12 > 10: evicts the first
    calls = 0
    cache('bbbbbb')
    expect(calls).toBe(0)
    cache('aaaaaa')
    expect(calls).toBe(1)
  })

  test('does not keep a text larger than the whole size limit', () => {
    let calls = 0
    const cache = createMarkdownCache((text) => { calls++; return text }, { maxEntries: 100, maxChars: 5 })
    expect(cache('abcdefgh')).toBe('abcdefgh')
    cache('abcdefgh')
    expect(calls).toBe(2)
  })
})
