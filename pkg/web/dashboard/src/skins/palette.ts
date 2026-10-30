import tokensCss from '../../skins/base/tokens.css?raw'
import graphiteCss from '../../skins/graphite/index.css?raw'
import gogaCss from '../../skins/goga/index.css?raw'
import coffeeCss from '../../skins/coffee/index.css?raw'
import novacorpsCss from '../../skins/novacorps/index.css?raw'

// Resolves each skin x mode palette the way the browser cascade does (tokens.css fallbacks under the skin's
// own :root[data-theme] block) and measures WCAG contrast over stacks of translucent layers.

export type Rgba = { r: number; g: number; b: number; a: number }
export type Vars = Map<string, string>

export const SKINS = { graphite: graphiteCss, goga: gogaCss, coffee: coffeeCss, novacorps: novacorpsCss }
export const MODES = ['dark', 'light'] as const

// Palette tokens the stylesheets paint as a bare text `color:`, with the pairs palette-contrast.test.ts measures for
// them. skins-contract.test.ts fails on any skin-declared token used as text colour that is missing here.
export const NEUTRAL_TEXT_TOKENS = ['--text-primary', '--text-secondary', '--text-muted', '--ink', '--ink-hi']
// Skin-specific text of the dialog panel and of the cost figure in the rail.
export const DIALOG_TEXT_TOKENS = ['--qa-answer', '--dialog-option-text']
export const COST_RAIL_TEXT_TOKENS = ['--cost-rail', '--cost-rail-strong']
// Tone text for links and syntax highlighting: the tone pulled toward the text colour, never the pure tone.
export const TONE_TEXT_TOKENS = ['--accent-text', '--attention-text', '--success-text', '--danger-text']
export const MEASURED_TEXT_TOKENS = [
  ...NEUTRAL_TEXT_TOKENS,
  ...DIALOG_TEXT_TOKENS,
  ...COST_RAIL_TEXT_TOKENS,
  ...TONE_TEXT_TOKENS,
  '--text-placeholder',
  '--text-disabled',
  '--surface-canvas',
  '--surface-raised',
]

// declarationsOf collects custom properties from every block whose selector is exactly `selector`.
function declarationsOf(css: string, selector: string): Vars {
  const vars: Vars = new Map()
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  for (const block of css.matchAll(new RegExp(`(?:^|\\n)${escaped}\\s*\\{([\\s\\S]*?)\\n\\}`, 'g'))) {
    for (const decl of block[1]!.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) vars.set(decl[1]!, decl[2]!.trim())
  }
  return vars
}

export function paletteOf(skinCss: string, mode: (typeof MODES)[number]): Vars {
  return new Map([
    ...declarationsOf(tokensCss, ':root'),
    ...declarationsOf(tokensCss, `:root[data-theme="${mode}"]`),
    ...declarationsOf(skinCss, `:root[data-theme="${mode}"]`),
  ])
}

// substituteVars inlines every var(--name) so a value becomes a literal colour, rgba(r, g, b, a) or color-mix().
function substituteVars(vars: Vars, text: string, depth = 0): string {
  if (depth > 8) throw new Error(`var() nesting too deep in ${text}`)
  return text.replace(/var\((--[\w-]+)\)/g, (_, name: string) => {
    const raw = vars.get(name)
    if (raw === undefined) throw new Error(`${name} is not declared`)
    return substituteVars(vars, raw, depth + 1)
  })
}

// splitTopLevel splits on commas outside parentheses.
function splitTopLevel(text: string): string[] {
  const parts: string[] = []
  let depth = 0
  let from = 0
  for (let i = 0; i < text.length; i++) {
    if (text[i] === '(') depth++
    else if (text[i] === ')') depth--
    else if (text[i] === ',' && depth === 0) {
      parts.push(text.slice(from, i).trim())
      from = i + 1
    }
  }
  parts.push(text.slice(from).trim())
  return parts
}

function parseLiteral(value: string): Rgba {
  const hex = value.match(/^#([0-9a-f]{3}|[0-9a-f]{6})$/i)
  if (hex) {
    const h = hex[1]!.length === 3 ? hex[1]!.replace(/./g, (c) => c + c) : hex[1]!
    return { r: parseInt(h.slice(0, 2), 16), g: parseInt(h.slice(2, 4), 16), b: parseInt(h.slice(4, 6), 16), a: 1 }
  }
  const rgb = value.match(/^rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*(?:,\s*([\d.]+)\s*)?\)$/)
  if (rgb) return { r: +rgb[1]!, g: +rgb[2]!, b: +rgb[3]!, a: rgb[4] === undefined ? 1 : +rgb[4] }
  throw new Error(`unsupported color: ${value}`)
}

// parseColor understands the forms the dashboard CSS uses, including opaque color-mix(in srgb, A p%, B).
function parseColor(value: string): Rgba {
  const mix = value.match(/^color-mix\(\s*in srgb\s*,([\s\S]*)\)$/)
  if (!mix) return parseLiteral(value)
  const [first, second] = splitTopLevel(mix[1]!).map((arg) => {
    const m = arg.match(/^([\s\S]*?)(?:\s+([\d.]+)%)?$/)!
    return { color: parseColor(m[1]!.trim()), share: m[2] === undefined ? undefined : +m[2] / 100 }
  })
  const share = first!.share ?? 1 - (second!.share ?? 0.5)
  const mixChannel = (a: number, b: number): number => Math.round(a * share + b * (1 - share))
  return {
    r: mixChannel(first!.color.r, second!.color.r),
    g: mixChannel(first!.color.g, second!.color.g),
    b: mixChannel(first!.color.b, second!.color.b),
    a: 1,
  }
}

// colorOf takes a token name (--text-muted) or a CSS expression (rgba(var(--accent-primary-rgb), 0.16)).
export function colorOf(vars: Vars, token: string): Rgba {
  return parseColor(substituteVars(vars, token.startsWith('--') ? `var(${token})` : token))
}

function over(top: Rgba, bottom: Rgba): Rgba {
  const mix = (t: number, b: number): number => Math.round(t * top.a + b * (1 - top.a))
  return { r: mix(top.r, bottom.r), g: mix(top.g, bottom.g), b: mix(top.b, bottom.b), a: 1 }
}

function luminance({ r, g, b }: Rgba): number {
  const lin = (c: number): number => {
    const s = c / 255
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b)
}

// contrast composites the layers bottom-first (a solid surface, then any translucent tints) and measures fg on the result.
export function contrast(fg: string, layers: string[], vars: Vars): number {
  const back = layers.map((layer) => colorOf(vars, layer)).reduce((below, layer) => over(layer, below))
  const front = over(colorOf(vars, fg), back)
  const [hi, lo] = [luminance(front), luminance(back)].sort((a, b) => b - a)
  return (hi! + 0.05) / (lo! + 0.05)
}
