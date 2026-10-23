import { describe, expect, test } from 'vitest'
import { draftKey, newFileCommentKey, rawSourceLines, renderDraftComments, type DraftComment } from './draft-comments'

function makeComment(overrides: Partial<DraftComment> = {}): DraftComment {
  return {
    root: 'root',
    path: 'pkg/foo/bar.go',
    displayPath: 'project/pkg/foo/bar.go',
    reference: '[AFM file: "/work/pkg/foo/bar.go"]',
    line: 42,
    origLineText: '\tif err != nil {',
    text: 'тут нужен wrap',
    ...overrides,
  }
}

describe('renderDraftComments', () => {
  test('пустой вход → пустая строка', () => {
    expect(renderDraftComments([])).toBe('')
  })

  test('один коммент: заголовок File comments, ссылка на файл, строка, цитата и текст с отступом', () => {
    const out = renderDraftComments([makeComment()])
    expect(out.split('\n')).toEqual([
      '## File comments',
      '',
      '### [AFM file: "/work/pkg/foo/bar.go"]  "project/pkg/foo/bar.go"',
      '- Line 42; source: "\\tif err != nil {"',
      '  тут нужен wrap',
      '',
    ])
  })

  test('многострочный текст: каждая строка с отступом, пустая строка остаётся пустой', () => {
    const out = renderDraftComments([makeComment({ text: 'first\n\nsecond\r\nthird' })])
    expect(out).toContain('  first\n\n  second\n  third\n')
  })

  test('два коммента в одном файле, переданные в обратном порядке строк → по возрастанию line', () => {
    const out = renderDraftComments([
      makeComment({ line: 88, origLineText: '\tfoo()', text: 'это лишнее' }),
      makeComment({ line: 42, origLineText: '\tif err != nil {', text: 'тут нужен wrap' }),
    ])
    expect(out.indexOf('- Line 42;')).toBeGreaterThan(0)
    expect(out.indexOf('- Line 42;')).toBeLessThan(out.indexOf('- Line 88;'))
    expect(out.match(/### /g)).toHaveLength(1)
  })

  test('два файла, переданные в обратном алфавитном порядке displayPath → группы по возрастанию', () => {
    const out = renderDraftComments([
      makeComment(),
      makeComment({
        path: 'cmd/afm/run.go',
        displayPath: 'project/cmd/afm/run.go',
        reference: '[AFM file: "/work/cmd/afm/run.go"]',
        line: 7,
        origLineText: 'import (',
        text: 'и тут',
      }),
    ])
    expect(out.indexOf('"project/cmd/afm/run.go"')).toBeLessThan(out.indexOf('"project/pkg/foo/bar.go"'))
  })

  test('одинаковые displayPath и path в разных корнях → две секции со своими reference', () => {
    const out = renderDraftComments([
      makeComment({ root: 'a', reference: '[AFM file: "/one/bar.go"]', text: 'первый' }),
      makeComment({ root: 'b', reference: '[AFM file: "/two/bar.go"]', text: 'второй' }),
    ])
    expect(out.match(/### /g)).toHaveLength(2)
    expect(out).toMatch(/### \[AFM file: "\/one\/bar.go"\]  .*\n- Line 42;.*\n  первый\n/)
    expect(out).toMatch(/### \[AFM file: "\/two\/bar.go"\]  .*\n- Line 42;.*\n  второй\n/)
  })

  test('цитата с табом и кавычкой → JSON-escaped, в одной строке', () => {
    const out = renderDraftComments([makeComment({ origLineText: '\tfoo("x")', text: 'коммент' })])
    expect(out).toContain('source: "\\tfoo(\\"x\\")"')
  })

  test('длинная цитата ограничена и помечена многоточием, а не режет пару суррогатов', () => {
    const out = renderDraftComments([makeComment({ origLineText: '😀'.repeat(300), text: 'x' })])
    const quote = out.split('\n').find((l) => l.startsWith('- Line'))!
    expect(quote).toBe(`- Line 42; source: ${JSON.stringify('😀'.repeat(160) + '…')}`)
  })

  test('@ в коде и в тексте остаётся буквальным: цитата в кавычках, текст с отступом', () => {
    const out = renderDraftComments([makeComment({ origLineText: 'user@example.com @helper', text: 'ask @reviewer' })])
    expect(out).toContain('source: "user@example.com @helper"')
    expect(out).toContain('\n  ask @reviewer\n')
  })

  test('заголовок в тексте комментария не становится заголовком сообщения', () => {
    const out = renderDraftComments([makeComment({ text: '## File comments\n### fake' })])
    expect(out.split('\n').filter((l) => l.startsWith('#'))).toEqual(['## File comments', '### [AFM file: "/work/pkg/foo/bar.go"]  "project/pkg/foo/bar.go"'])
  })

  test('displayPath в кавычках, ровно два пробела между reference и ним', () => {
    const headerLine = renderDraftComments([makeComment()]).split('\n')[2]
    expect(headerLine).toBe('### [AFM file: "/work/pkg/foo/bar.go"]  "project/pkg/foo/bar.go"')
  })
})

describe('draftKey', () => {
  test('строится через JSON.stringify([root, path, line]), не допуская коллизий конкатенации', () => {
    const a = draftKey('r', 'a/b', 1)
    const b = draftKey('r', 'a', 1)
    expect(a).not.toBe(b)
    expect(a).toBe(JSON.stringify(['r', 'a/b', 1]))
    expect(b).toBe(JSON.stringify(['r', 'a', 1]))
  })
})

describe('newFileCommentKey', () => {
  test('returns a distinct id on every call, independent of file and line', () => {
    expect(newFileCommentKey()).not.toBe(newFileCommentKey())
  })
})

describe('rawSourceLines', () => {
  test('matches the displayed line count: empty and newline-only files have no lines', () => {
    expect(rawSourceLines('')).toEqual([])
    expect(rawSourceLines('\n')).toEqual([])
  })

  test('drops exactly one trailing newline', () => {
    expect(rawSourceLines('a\nb')).toEqual(['a', 'b'])
    expect(rawSourceLines('a\n')).toEqual(['a'])
    expect(rawSourceLines('a\n\n')).toEqual(['a', ''])
  })
})
