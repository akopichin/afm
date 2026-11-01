// A small reader of skin CSS for the contract tests: what each rule declares, without the browser cascade.

export type Block = { prelude: string; body: string }
export type Rule = { media: string; selectors: string[]; body: string }

// Splits CSS into its top-level blocks; an at-rule keeps its raw body so a media query can be parsed again.
export function blocks(css: string): Block[] {
  const source = css.replace(/\/\*[\s\S]*?\*\//g, '')
  const found: Block[] = []
  let depth = 0
  let start = 0
  let bodyStart = 0
  for (let i = 0; i < source.length; i++) {
    if (source[i] === '{') {
      if (depth === 0) bodyStart = i + 1
      depth++
    } else if (source[i] === '}') {
      depth--
      if (depth === 0) {
        found.push({ prelude: source.slice(start, bodyStart - 1).trim(), body: source.slice(bodyStart, i) })
        start = i + 1
      }
    }
  }
  return found
}

const selectorsOf = (prelude: string): string[] => prelude.split(',').map((s) => s.replace(/\s+/g, ' ').trim())

// The value one rule body gives a property; a later declaration in the body wins.
export function valueOf(body: string, property: string): string | undefined {
  let value: string | undefined
  for (const declaration of body.split(';')) {
    const colon = declaration.indexOf(':')
    if (colon > 0 && declaration.slice(0, colon).trim() === property) value = declaration.slice(colon + 1).trim()
  }
  return value
}

// The value a property finally takes for an exact selector at the top level: the last declaring rule wins.
export function declared(css: string, selector: string, property: string): string | undefined {
  let value: string | undefined
  for (const { prelude, body } of blocks(css)) {
    if (prelude.startsWith('@') || !selectorsOf(prelude).includes(selector)) continue
    value = valueOf(body, property) ?? value
  }
  return value
}

// The rules of every `@media <query>` block in source order, ready for `declared`.
export const media = (css: string, query: string): string =>
  blocks(css)
    .filter((b) => b.prelude === `@media ${query}`)
    .map((b) => b.body)
    .join('\n')

// Every style rule with the media query it sits in ('' at the top level).
export function rules(css: string, query = ''): Rule[] {
  return blocks(css).flatMap(({ prelude, body }) => {
    if (prelude.startsWith('@media')) return rules(body, prelude.slice('@media'.length).trim())
    if (prelude.startsWith('@')) return []
    return [{ media: query, selectors: selectorsOf(prelude), body }]
  })
}
