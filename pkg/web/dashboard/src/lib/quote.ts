// buildReplyBody собирает тело ответа на мысль для Chat и Restart: цитата целиком как Markdown-blockquote, затем
// пустая строка и комментарий пользователя. Цитата добавляется только при отправке, в поле ввода не вставляется.
export function buildReplyBody(quote: string | null, comment: string): string {
  const c = comment.trim()
  if (quote === null) return c
  const q = quote
    .replace(/\r\n?/g, '\n')
    .split('\n')
    .map((l) => `> ${l}`)
    .join('\n')
  return `${q}\n\n${c}`
}
