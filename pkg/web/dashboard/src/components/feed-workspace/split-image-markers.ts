import { codeBlockLineRanges } from '../plan-panel/markdown'

// splitImageMarkers — сегментация текста сообщения агента на markdown-текст и ссылки на опубликованные
// стадией картинки.

export type Segment = { type: 'md'; text: string } | { type: 'img'; name: string }

// Максимум картинок в одном сообщении ленты. Сверх лимита маркеры остаются текстом.
export const MAX_FEED_IMAGES = 20

// Полная строка-маркер: `[AFM image: <name>]`, name из charset [A-Za-z0-9._-]+. Тот же charset, что у
// валидатора имени артефакта на бэкенде.
const MARKER_RE = /^\[AFM image: ([A-Za-z0-9._-]+)\]$/

export function splitImageMarkers(text: string): Segment[] {
  const lines = text.split('\n')
  const segments: Segment[] = []
  let buffer: string[] = []
  let imageCount = 0

  // 0-based строки, которые markdown-it считает кодом. Маркер внутри такого диапазона — это текст примера,
  // а не запрос картинки.
  const codeRanges = codeBlockLineRanges(text)
  const isCodeLine = (i: number): boolean => codeRanges.some(([start, end]) => i >= start && i < end)

  const flush = () => {
    if (buffer.length === 0) return
    const md = buffer.join('\n')
    buffer = []
    // Чисто-пробельные разделители (пустые строки вокруг маркеров) не порождают
    // видимого md — не плодим пустые сегменты.
    if (md.trim() !== '') segments.push({ type: 'md', text: md })
  }

  lines.forEach((line, i) => {
    if (!isCodeLine(i) && imageCount < MAX_FEED_IMAGES) {
      const m = MARKER_RE.exec(line.trim())
      if (m !== null) {
        flush()
        segments.push({ type: 'img', name: m[1]! })
        imageCount++
        return
      }
    }
    buffer.push(line)
  })

  flush()
  return segments
}
