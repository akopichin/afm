// buildReplyBody собирает тело Revise для ответа на мысль: цитата целиком как Markdown-blockquote, затем
// пустая строка и комментарий пользователя.
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
