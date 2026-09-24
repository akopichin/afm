import { describe, expect, test } from 'vitest'
import { formatTokensCompact } from './format-tokens'

describe('formatTokensCompact', () => {
  test('меньше 1000 — как есть, без суффикса', () => {
    expect(formatTokensCompact(0)).toBe('0')
    expect(formatTokensCompact(42)).toBe('42')
    expect(formatTokensCompact(999)).toBe('999')
  })

  test('тысячи → суффикс K с одной значащей цифрой', () => {
    expect(formatTokensCompact(1000)).toBe('1K')
    expect(formatTokensCompact(1500)).toBe('1.5K')
    expect(formatTokensCompact(345_000)).toBe('345K')
    expect(formatTokensCompact(999_400)).toBe('999.4K')
  })

  test('миллионы → суффикс M с одной значащей цифрой', () => {
    expect(formatTokensCompact(1_000_000)).toBe('1M')
    expect(formatTokensCompact(1_234_567)).toBe('1.2M')
    expect(formatTokensCompact(12_000_000)).toBe('12M')
  })

  test('без хвостового «.0» на целых значениях', () => {
    expect(formatTokensCompact(2_000)).toBe('2K')
    expect(formatTokensCompact(2_000_000)).toBe('2M')
  })
})
