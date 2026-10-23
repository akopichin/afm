// DraftComment — клиентский черновик коммента к строке файла. Серверу не отправляется напрямую —
// renderDraftComments превращает набор таких черновиков в текст сообщения агенту.
export type DraftComment = {
  root: string // id корня в workspace
  path: string // путь внутри корня (FileContent.path)
  displayPath: string // FileContent.displayPath, напр. "project/pkg/foo/bar.go"
  reference: string // FileContent.reference, напр. [AFM file: "/work/pkg/foo/bar.go"]
  line: number // 1-based
  origLineText: string // сырая строка исходника на момент коммента
  text: string // текст коммента
}

// draftKey — ключ черновика для upsert по (root, path, line). Строится через
// JSON.stringify массива — в отличие от конкатенации строк, это не даёт
// ложных коллизий между разными (root, path) при разных line.
export function draftKey(root: string, path: string, line: number): string {
  return JSON.stringify([root, path, line])
}

// fileGroupKey — ключ файла по (root, path): displayPath у разных корней с одной меткой совпадает, а файлы разные.
export function fileGroupKey(root: string, path: string): string {
  return JSON.stringify([root, path])
}

export type SourceDrift = {
  currentText: string | null // null — строки больше нет
  moveTo: number | null // единственная строка, где цитата встречается теперь
}

// rawSourceLines — строки файла без разметки подсветки: цитата комментария и сверка с дрейфом.
// Число строк совпадает с отображаемым (splitHighlightedLines): пустой файл — ноль строк.
export function rawSourceLines(content: string): string[] {
  const body = content.replace(/\n$/, '')
  return body === '' ? [] : body.split('\n')
}

// sourceDrift сверяет цитату комментария с текущим файлом; совпавшая строка — не дрейф, даже если ETag новый.
export function sourceDrift(quote: string, line: number, rawLines: string[]): SourceDrift | null {
  const current = rawLines[line - 1]
  if (current === quote) return null
  const matches = quote === '' ? [] : rawLines.flatMap((text, index) => (text === quote ? [index + 1] : []))
  return { currentText: current ?? null, moveTo: matches.length === 1 ? (matches[0] ?? null) : null }
}

let fileCommentCounter = 0

// newFileCommentKey — стабильный ID файлового комментария: root/path/line редактируемы (re-anchor), поэтому в ID не входят.
export function newFileCommentKey(): string {
  fileCommentCounter += 1
  return `file:${fileCommentCounter}`
}

const QUOTE_LIMIT = 160

// boundedQuote — цитата строки исходника не длиннее QUOTE_LIMIT символов; режем по code point, чтобы не ломать суррогатные пары.
function boundedQuote(line: string): string {
  const chars = Array.from(line)
  return chars.length <= QUOTE_LIMIT ? line : `${chars.slice(0, QUOTE_LIMIT).join('')}…`
}

// indentText — многострочный текст как продолжение пункта списка; пустые строки не получают хвостовых пробелов.
function indentText(text: string): string {
  return text
    .replace(/\r\n?/g, '\n')
    .split('\n')
    .map((line) => (line === '' ? '' : `  ${line}`))
    .join('\n')
}

function compareStrings(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0
}

// renderDraftComments рендерит файловые комментарии одним сообщением: ссылка на файл, номер строки,
// ограниченная цитата и текст с отступом. Цитата — JSON-строка, текст — с отступом: так ни код, ни комментарий
// не превращаются в заголовок или в адресата (адресат задаётся отдельно, текст @-упоминания не разбирается).
export function renderDraftComments(drafts: DraftComment[]): string {
  if (drafts.length === 0) return ''

  const byFile = new Map<string, DraftComment[]>()
  for (const d of drafts) {
    const key = fileGroupKey(d.root, d.path)
    byFile.set(key, [...(byFile.get(key) ?? []), d])
  }
  const files = Array.from(byFile, ([key, group]) => ({ key, group: [...group].sort((a, b) => a.line - b.line) })).sort(
    (a, b) => compareStrings(a.group[0]?.displayPath ?? '', b.group[0]?.displayPath ?? '') || compareStrings(a.key, b.key),
  )

  let out = '## File comments\n'
  for (const { group } of files) {
    const [first] = group
    if (first === undefined) continue
    out += `\n### ${first.reference}  ${JSON.stringify(first.displayPath)}\n`
    for (const d of group) {
      out += `- Line ${d.line}; source: ${JSON.stringify(boundedQuote(d.origLineText))}\n${indentText(d.text)}\n`
    }
  }
  return out
}
