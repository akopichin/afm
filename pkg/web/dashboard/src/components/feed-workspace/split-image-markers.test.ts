import { describe, expect, test } from 'vitest'
import { splitImageMarkers, MAX_FEED_IMAGES, type Segment } from './split-image-markers'

// splitImageMarkers — сегментирует текст сообщения агента на md-текст и img-маркеры
// `[AFM image: <name>]` (отдельной строкой, вне fenced-кода, валидный charset).
// Всё сомнительное (маркер в коде / посреди строки / с невалидным именем) остаётся
// текстом. Cap MAX_FEED_IMAGES img-сегментов на сообщение.

const imgNames = (segs: Segment[]): string[] =>
  segs.filter((s): s is { type: 'img'; name: string } => s.type === 'img').map((s) => s.name)

describe('splitImageMarkers', () => {
  test('0 маркеров → один md-сегмент с исходным текстом (переносы сохранены)', () => {
    const segs = splitImageMarkers('hello\nworld')
    expect(segs).toEqual([{ type: 'md', text: 'hello\nworld' }])
  })

  test('пустой текст → нет сегментов', () => {
    expect(splitImageMarkers('')).toEqual([])
    expect(splitImageMarkers('   \n  ')).toEqual([])
  })

  test('1 маркер отдельной строкой → img-сегмент', () => {
    const segs = splitImageMarkers('[AFM image: chart.png]')
    expect(segs).toEqual([{ type: 'img', name: 'chart.png' }])
  })

  test('N маркеров + текст → чередование md/img сохраняет порядок', () => {
    const segs = splitImageMarkers('a\n[AFM image: x.png]\nb\n[AFM image: y.jpg]\nc')
    expect(segs).toEqual([
      { type: 'md', text: 'a' },
      { type: 'img', name: 'x.png' },
      { type: 'md', text: 'b' },
      { type: 'img', name: 'y.jpg' },
      { type: 'md', text: 'c' },
    ])
  })

  test('маркер внутри fenced-кода → остаётся текстом', () => {
    const text = '```\n[AFM image: nope.png]\n```'
    const segs = splitImageMarkers(text)
    expect(imgNames(segs)).toEqual([])
    expect(segs).toEqual([{ type: 'md', text }])
  })

  test('маркер внутри ~~~ tilde-fenced-кода → остаётся текстом', () => {
    const text = '~~~\n[AFM image: nope.png]\n~~~'
    const segs = splitImageMarkers(text)
    expect(imgNames(segs)).toEqual([])
    expect(segs).toEqual([{ type: 'md', text }])
  })

  test('маркер в 4-backtick fence с вложенной ```-строкой → остаётся текстом', () => {
    // Вложенная ```-строка (len 3 < 4) НЕ закрывает 4-backtick fence, поэтому
    // маркер после неё всё ещё внутри кода — не должен стать картинкой.
    const text = '````\n```\n[AFM image: nope.png]\n````'
    const segs = splitImageMarkers(text)
    expect(imgNames(segs)).toEqual([])
  })

  test('маркер ПОСЛЕ закрытого fence → распознаётся как картинка', () => {
    const segs = splitImageMarkers('```\ncode\n```\n[AFM image: after.png]')
    expect(imgNames(segs)).toEqual(['after.png'])
  })

  test('маркер посреди строки → остаётся текстом', () => {
    const segs = splitImageMarkers('see this [AFM image: mid.png] inline')
    expect(imgNames(segs)).toEqual([])
    expect(segs).toEqual([{ type: 'md', text: 'see this [AFM image: mid.png] inline' }])
  })

  test('невалидное имя (пробел/слэш/спецсимволы) → остаётся текстом', () => {
    for (const bad of [
      '[AFM image: has space.png]',
      '[AFM image: sub/dir.png]',
      '[AFM image: ../escape.png]',
      '[AFM image: ]',
      '[AFM image: a%b.png]',
      '[AFM image: a&b.png]',
      '[AFM image: a#b.png]',
      '[AFM image: a?b.png]',
      '[AFM image: a"b.png]',
      '[AFM image: a<b.png]',
    ]) {
      const segs = splitImageMarkers(bad)
      expect(imgNames(segs), bad).toEqual([])
      expect(segs, bad).toEqual([{ type: 'md', text: bad }])
    }
  })

  test('control-символы в имени → остаётся текстом', () => {
    const bad = '[AFM image: ab.png]'
    const segs = splitImageMarkers(bad)
    expect(imgNames(segs)).toEqual([])
  })

  test('маркер с окружающими пробелами (trim) всё ещё распознаётся', () => {
    const segs = splitImageMarkers('   [AFM image: chart.png]   ')
    expect(segs).toEqual([{ type: 'img', name: 'chart.png' }])
  })

  test('cap: не более MAX_FEED_IMAGES img-сегментов, лишние маркеры → текст', () => {
    const total = MAX_FEED_IMAGES + 5
    const lines: string[] = []
    for (let i = 0; i < total; i++) lines.push(`[AFM image: img${i}.png]`)
    const segs = splitImageMarkers(lines.join('\n'))
    expect(imgNames(segs)).toHaveLength(MAX_FEED_IMAGES)
    // Лишние маркеры уехали в md-текст.
    const mdText = segs
      .filter((s): s is { type: 'md'; text: string } => s.type === 'md')
      .map((s) => s.text)
      .join('\n')
    expect(mdText).toContain(`[AFM image: img${MAX_FEED_IMAGES}.png]`)
  })

  test('MAX_FEED_IMAGES равен 20', () => {
    expect(MAX_FEED_IMAGES).toBe(20)
  })

  test('соседние md-строки коалесцируются в один сегмент', () => {
    const segs = splitImageMarkers('line1\nline2\n[AFM image: x.png]\nline3\nline4')
    expect(segs).toEqual([
      { type: 'md', text: 'line1\nline2' },
      { type: 'img', name: 'x.png' },
      { type: 'md', text: 'line3\nline4' },
    ])
  })
})
