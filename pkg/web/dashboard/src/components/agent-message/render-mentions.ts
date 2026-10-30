// Decorate only text produced by the shared safe renderer; links and code keep their literal content.
export function highlightMentions(html: string, handles: readonly string[]): string {
  if (handles.length === 0) return html
  const names = [...new Set(handles)].filter(Boolean).sort((a, b) => b.length - a.length)
  if (names.length === 0) return html
  const escaped = names.map((name) => name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'))
  const pattern = new RegExp(`(^|[^\\p{L}\\p{N}_@/])(@(?:${escaped.join('|')}))(?=$|[^\\p{L}\\p{N}_:-])`, 'giu')
  const template = document.createElement('template')
  template.innerHTML = html
  const walker = document.createTreeWalker(template.content, NodeFilter.SHOW_TEXT)
  const nodes: Text[] = []
  for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
    if (!node.parentElement?.closest('a, code, pre')) nodes.push(node as Text)
  }
  for (const node of nodes) {
    const text = node.data
    const fragment = document.createDocumentFragment()
    let end = 0
    for (const match of text.matchAll(pattern)) {
      const start = match.index + match[1]!.length
      fragment.append(text.slice(end, start))
      const chip = document.createElement('span')
      chip.className = 'message-mention'
      chip.textContent = match[2]!
      fragment.append(chip)
      end = start + match[2]!.length
    }
    if (end > 0) {
      fragment.append(text.slice(end))
      node.replaceWith(fragment)
    }
  }
  return template.innerHTML
}
