// splitImageMarkers — сегментация текста сообщения агента на markdown-текст и
// ссылки на опубликованные стадией картинки. Картинка адресуется standalone-маркером
// `[AFM image: <name>]` (отдельной строкой), где <name> — opaque-имя артефакта из
// безопасного charset. Всё, что не является таким маркером (маркер внутри fenced-кода,
// маркер посреди строки, невалидное имя), остаётся обычным md-текстом и рендерится
// как есть. Cap MAX_FEED_IMAGES img-сегментов на сообщение — лишние маркеры остаются
// текстом (ограничивает число параллельных GET/декодирований от одного сообщения).

export type Segment = { type: 'md'; text: string } | { type: 'img'; name: string }

// Максимум картинок в одном сообщении ленты. Сверх лимита маркеры остаются текстом.
export const MAX_FEED_IMAGES = 20

// Полная (после trim) строка-маркер: `[AFM image: <name>]`, name из charset
// [A-Za-z0-9._-]+ (без `/`, `\`, пробелов, `..`-эскейпов). Тот же charset, что у
// валидатора имени артефакта на бэкенде.
const MARKER_RE = /^\[AFM image: ([A-Za-z0-9._-]+)\]$/

export function splitImageMarkers(text: string): Segment[] {
  const lines = text.split('\n')
  const segments: Segment[] = []
  let buffer: string[] = []
  let inCode = false
  let imageCount = 0

  const flush = () => {
    if (buffer.length === 0) return
    const md = buffer.join('\n')
    buffer = []
    // Чисто-пробельные разделители (пустые строки вокруг маркеров) не порождают
    // видимого md — не плодим пустые сегменты.
    if (md.trim() !== '') segments.push({ type: 'md', text: md })
  }

  for (const line of lines) {
    if (line.trim().startsWith('```')) {
      inCode = !inCode
      buffer.push(line)
      continue
    }

    if (!inCode && imageCount < MAX_FEED_IMAGES) {
      const m = MARKER_RE.exec(line.trim())
      if (m !== null) {
        flush()
        segments.push({ type: 'img', name: m[1]! })
        imageCount++
        continue
      }
    }

    buffer.push(line)
  }

  flush()
  return segments
}
