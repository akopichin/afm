import { describe, expect, it } from 'vitest'
import { buildReplyBody } from './quote'

describe('buildReplyBody', () => {
  it('без цитаты (null) — просто триммированный комментарий', () => {
    expect(buildReplyBody(null, '  привет  ')).toBe('привет')
  })

  it('однострочная цитата — строка с `> ` + пустая строка + комментарий', () => {
    expect(buildReplyBody('one line', 'comment')).toBe('> one line\n\ncomment')
  })

  it('многострочная цитата — каждая строка с префиксом `> `', () => {
    expect(buildReplyBody('a\nb', 'c')).toBe('> a\n> b\n\nc')
  })

  it('CRLF нормализуется в LF (без `\\r` в результате)', () => {
    const body = buildReplyBody('a\r\nb', 'c')
    expect(body).toBe('> a\n> b\n\nc')
    expect(body).not.toContain('\r')
  })

  it('вложенная цитата `> x` становится `> > x`', () => {
    expect(buildReplyBody('> x', 'c')).toBe('> > x\n\nc')
  })

  it('fenced code block сохраняется (каждая строка префиксится)', () => {
    expect(buildReplyBody('```\ncode\n```', 'c')).toBe('> ```\n> code\n> ```\n\nc')
  })
})
