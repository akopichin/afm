// Тонкая обёртка над highlight.js/lib/core с ЯВНЫМ списком из 7 грамматик —
// go/typescript/javascript/python/yaml/markdown/bash.
import hljs from 'highlight.js/lib/core'
import bash from 'highlight.js/lib/languages/bash'
import go from 'highlight.js/lib/languages/go'
import javascript from 'highlight.js/lib/languages/javascript'
import markdown from 'highlight.js/lib/languages/markdown'
import python from 'highlight.js/lib/languages/python'
import typescript from 'highlight.js/lib/languages/typescript'
import yaml from 'highlight.js/lib/languages/yaml'

hljs.registerLanguage('go', go)
hljs.registerLanguage('typescript', typescript)
hljs.registerLanguage('javascript', javascript)
hljs.registerLanguage('python', python)
hljs.registerLanguage('yaml', yaml)
hljs.registerLanguage('markdown', markdown)
hljs.registerLanguage('bash', bash)

export function escapeHtml(source: string): string {
  return source.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

// highlight возвращает HTML-строку: подсвеченную для языка из зарегистрированных семи, иначе — просто
// экранированный исходник как есть.
export function highlight(language: string, source: string): string {
  if (language === 'plain' || !hljs.getLanguage(language)) {
    return escapeHtml(source)
  }
  return hljs.highlight(source, { language }).value
}

// splitHighlightedLines splits an already-highlighted HTML string into one HTML fragment per source line —
// needed by FileViewer's per-line render.
export function splitHighlightedLines(html: string, source: string): string[] {
  // Match the backend's line-count semantics: it counts only REAL lines by trimming exactly one trailing
  // newline and treats an empty file as 0 lines.
  if (source === '') return []
  // Mirror the backend exactly: trim one trailing newline, and if nothing remains the file has zero real
  // lines (a source of only "\n" is 0 lines, not one empty line).
  const trimmed = source.endsWith('\n') ? source.slice(0, -1) : source
  if (trimmed === '') return []
  const rawLines = html.split('\n')
  if (source.endsWith('\n')) rawLines.pop()
  const openTags: string[] = []
  const tagRe = /<span class="[^"]*">|<\/span>/g

  return rawLines.map((rawLine) => {
    let line = openTags.join('')
    let lastIndex = 0
    tagRe.lastIndex = 0
    let match: RegExpExecArray | null
    while ((match = tagRe.exec(rawLine)) !== null) {
      line += rawLine.slice(lastIndex, match.index) + match[0]
      lastIndex = match.index + match[0].length
      if (match[0] === '</span>') {
        openTags.pop()
      } else {
        openTags.push(match[0])
      }
    }
    line += rawLine.slice(lastIndex)
    line += '</span>'.repeat(openTags.length)
    return line
  })
}
