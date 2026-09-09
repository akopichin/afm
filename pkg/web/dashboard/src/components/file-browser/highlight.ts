// Тонкая обёртка над highlight.js/lib/core с ЯВНЫМ списком из 4 грамматик —
// go/typescript/javascript/python (см. бриф Task 13). core (не index.js)
// нужен, чтобы не тянуть в бандл все ~190 грамматик highlight.js — дашборд
// показывает только код проекта afm, для которого этих четырёх достаточно.
//
// highlight() — единственная функция, чей вывод разрешено класть в
// dangerouslySetInnerHTML (см. FileViewer.tsx): hljs.highlight() сам
// экранирует спецсимволы найденного текста, оборачивая только распознанные
// токены в <span class="hljs-...">; escapeHtml — тот же контракт для языка
// 'plain' или нераспознанной грамматики, чтобы обе ветки одинаково безопасны
// класть в HTML.
import hljs from 'highlight.js/lib/core'
import go from 'highlight.js/lib/languages/go'
import javascript from 'highlight.js/lib/languages/javascript'
import python from 'highlight.js/lib/languages/python'
import typescript from 'highlight.js/lib/languages/typescript'

hljs.registerLanguage('go', go)
hljs.registerLanguage('typescript', typescript)
hljs.registerLanguage('javascript', javascript)
hljs.registerLanguage('python', python)

export function escapeHtml(source: string): string {
  return source.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

// highlight возвращает HTML-строку: подсвеченную (hljs сам экранирует текст
// внутри span'ов) для языка из зарегистрированных четырёх, иначе — просто
// экранированный исходник как есть (язык 'plain' — явный сигнал "не пытайся
// подсвечивать", как и любой язык, которого нет в hljs.getLanguage).
export function highlight(language: string, source: string): string {
  if (language === 'plain' || !hljs.getLanguage(language)) {
    return escapeHtml(source)
  }
  return hljs.highlight(source, { language }).value
}

// splitHighlightedLines splits an already-highlighted HTML string (the output
// of highlight() above) into one HTML fragment per source line — needed by
// FileViewer's per-line render (Task 20: a line must be its own addressable
// DOM node so it can be clicked to attach a review comment, the way
// PlanPanel's line-by-line review already works for plan markdown).
//
// A naive html.split('\n') is unsafe: hljs freely emits a <span class="hljs-…">
// that SPANS multiple output lines (a multi-line block comment or template
// string is one token, one <span>, with raw newlines inside it) — splitting
// on '\n' would leave that <span> unclosed on its own line and strand its
// closing </span> on a later, unrelated line, breaking both lines' HTML and
// bleeding highlight color onto text that was never part of the token.
//
// The fix: walk the tag stream once, tracking currently-open <span>s. Each
// output line starts with the tags still open from the previous line
// (reopened verbatim) and ends with those same tags closed (without popping
// them — they stay "open" going into the next line); only a real </span> pops
// the stack. Every returned line is therefore well-formed HTML on its own.
export function splitHighlightedLines(html: string, source: string): string[] {
  // Match the backend's line-count semantics (workspaceResolveFile in
  // cmd/afm/run.go): it counts only REAL lines by trimming exactly one trailing
  // newline (TrimSuffix(content, "\n")) and treats an empty file as 0 lines.
  //
  // The decision MUST be made from the SOURCE, not from the highlighted HTML.
  // highlight() preserves every source newline, so html has one '\n' per source
  // '\n'; when the source ends in a trailing newline it produces one extra
  // trailing fragment. That fragment is an empty string for plain text, but it
  // can be a token-closing tag (e.g. '</span>') when the final newline sits
  // INSIDE a multi-line token — so html.endsWith('\n') is unreliable (Go source
  // '/* hi\n' highlights to '<span class="hljs-comment">/* hi\n</span>', which
  // does not end in '\n'). Drop that last fragment iff the SOURCE ended in a
  // newline; the tag-rebalancing walk below re-closes any span it left open.
  if (source === '') return []
  // Mirror the backend exactly: trim one trailing newline, and if nothing
  // remains the file has zero real lines (a source of only "\n" is 0 lines,
  // not one empty line).
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
