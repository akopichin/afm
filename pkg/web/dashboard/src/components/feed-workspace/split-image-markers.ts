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

// Открывающая fence-строка CommonMark: ≥3 подряд backtick ИЛИ tilde (после
// необязательного отступа). Захватываем сам разделитель, чтобы закрытие считать
// корректно: закрыть fence может только строка из ТОГО ЖЕ символа и длиной не
// меньше открывающей (так markdown-it и трактует ```` ```` с вложенной ``` ```).
const FENCE_RE = /^ {0,3}(`{3,}|~{3,})/

export function splitImageMarkers(text: string): Segment[] {
  const lines = text.split('\n')
  const segments: Segment[] = []
  let buffer: string[] = []
  // fence !== null → мы внутри code-fence, открытого этим разделителем.
  let fence: { char: string; len: number } | null = null
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
    const fenceMatch = FENCE_RE.exec(line)
    if (fenceMatch !== null) {
      const marker = fenceMatch[1]!
      const char = marker[0]!
      const len = marker.length
      if (fence === null) {
        // Открытие: info-string (` ```ts `) допустима только на открывающей строке.
        fence = { char, len }
      } else if (char === fence.char && len >= fence.len && line.trim() === marker) {
        // Закрытие: тот же символ, длина ≥ открывающей, и БЕЗ info-string.
        fence = null
      }
      buffer.push(line)
      continue
    }

    if (fence !== null) {
      buffer.push(line)
      continue
    }

    if (imageCount < MAX_FEED_IMAGES) {
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
