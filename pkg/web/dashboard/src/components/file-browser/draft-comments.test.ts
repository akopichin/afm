import { describe, expect, test } from 'vitest'
import { draftKey, renderDraftComments, type DraftComment } from './draft-comments'

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

  test('один коммент → ровно три строки', () => {
    const out = renderDraftComments([makeComment()])
    expect(out.split('\n')).toEqual([
      '## Review notes',
      '### [AFM file: "/work/pkg/foo/bar.go"]  "project/pkg/foo/bar.go"',
      '- Line 42; original: "\\tif err != nil {": тут нужен wrap',
      '',
    ])
  })

  test('два коммента в одном файле, переданные в обратном порядке строк → по возрастанию line', () => {
    const out = renderDraftComments([
      makeComment({ line: 88, origLineText: '\tfoo()', text: 'это лишнее' }),
      makeComment({ line: 42, origLineText: '\tif err != nil {', text: 'тут нужен wrap' }),
    ])
    expect(out).toBe(
      [
        '## Review notes',
        '### [AFM file: "/work/pkg/foo/bar.go"]  "project/pkg/foo/bar.go"',
        '- Line 42; original: "\\tif err != nil {": тут нужен wrap',
        '- Line 88; original: "\\tfoo()": это лишнее',
        '',
      ].join('\n'),
    )
  })

  test('два файла, переданные в обратном алфавитном порядке displayPath → группы по возрастанию', () => {
    const out = renderDraftComments([
      makeComment({
        root: 'root',
        path: 'cmd/afm/run.go',
        displayPath: 'project/cmd/afm/run.go',
        reference: '[AFM file: "/work/cmd/afm/run.go"]',
        line: 7,
        origLineText: 'import (',
        text: 'и тут',
      }),
      makeComment(),
    ])
    expect(out).toBe(
      [
        '## Review notes',
        '### [AFM file: "/work/cmd/afm/run.go"]  "project/cmd/afm/run.go"',
        '- Line 7; original: "import (": и тут',
        '### [AFM file: "/work/pkg/foo/bar.go"]  "project/pkg/foo/bar.go"',
        '- Line 42; original: "\\tif err != nil {": тут нужен wrap',
        '',
      ].join('\n'),
    )
  })

  test('origLineText с табом и кавычкой → JSON-escaped в выводе', () => {
    const out = renderDraftComments([
      makeComment({ origLineText: '\tfoo("x")', text: 'коммент' }),
    ])
    expect(out).toContain('original: "\\tfoo(\\"x\\")":')
  })

  test('displayPath в кавычках, ровно два пробела между reference и ним', () => {
    const out = renderDraftComments([makeComment()])
    const headerLine = out.split('\n')[1]
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
