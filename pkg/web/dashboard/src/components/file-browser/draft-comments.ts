// DraftComment — клиентский черновик коммента к строке файла в режиме
// "Without pause". Живёт только в памяти (FileBrowserProvider), серверу не
// отправляется напрямую — renderDraftComments превращает набор таких
// черновиков в текст, который уходит сайд-агенту как цитата.
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

// renderDraftComments рендерит черновики в markdown-текст — зеркало Go
// renderReviewFeedback (pkg/orchestrator/reviewpause.go), без warning-маркеров
// о дрейфе контента (здесь нет серверной сверки).
export function renderDraftComments(drafts: DraftComment[]): string {
  if (drafts.length === 0) return ''

  const byFile = new Map<string, DraftComment[]>()
  for (const d of drafts) {
    const group = byFile.get(d.displayPath)
    if (group === undefined) {
      byFile.set(d.displayPath, [d])
    } else {
      group.push(d)
    }
  }
  const order = Array.from(byFile.keys()).sort()

  let out = '## Review notes\n'
  for (const displayPath of order) {
    const group = [...(byFile.get(displayPath) ?? [])].sort((a, b) => a.line - b.line)
    const [first] = group
    if (first === undefined) continue
    out += `### ${first.reference}  ${JSON.stringify(displayPath)}\n`
    for (const d of group) {
      out += `- Line ${d.line}; original: ${JSON.stringify(d.origLineText)}: ${d.text}\n`
    }
  }
  return out
}
