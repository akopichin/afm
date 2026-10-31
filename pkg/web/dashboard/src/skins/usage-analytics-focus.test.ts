import { afterEach, describe, expect, it } from 'vitest'
import usageCss from '../../skins/base/usage-analytics.css?raw'

// The focus look of the Models & agents controls: a filled tint instead of a rectangular outline, so keyboard focus
// stays visible without a border around every button. jsdom does not resolve color-mix()/var(), so the test reads the declarations of the rules that match the control.

const CONTROLS = [
  { name: 'tab', markup: '<nav class="usage-views"><button>Breakdown</button></nav>' },
  { name: 'segmented button', markup: '<div class="usage-segmented"><button aria-pressed="false">By model</button></div>' },
  { name: 'metric switch button', markup: '<div class="usage-metric-switch"><button aria-pressed="false">Cost</button></div>' },
  { name: 'pager button', markup: '<div class="usage-pager"><button class="usage-pager-button">next</button></div>' },
  { name: 'clear filters', markup: '<button class="usage-clear">Clear filters</button>' },
  { name: 'sort header', markup: '<table class="usage-table"><thead><tr><th><button>Cost</button></th></tr></thead></table>' },
  { name: 'back', markup: '<button class="usage-back">Back</button>' },
]

const mount = (markup: string, scoped: boolean): HTMLElement => {
  const host = document.createElement('div')
  host.className = scoped ? 'usage-details' : ''
  host.innerHTML = markup
  document.body.append(host)
  return host.querySelector('button, summary') as HTMLElement
}

// Declarations of the stylesheet rules that match the element in the given state, last rule wins.
const declared = (el: HTMLElement, focused: boolean): Map<string, string> => {
  const result = new Map<string, string>()
  const sheet = document.styleSheets[0]!
  for (const rule of Array.from(sheet.cssRules) as CSSStyleRule[]) {
    if (!rule.selectorText) continue
    const isFocusRule = rule.selectorText.includes(':focus')
    const applies = (focused || !isFocusRule) && el.matches(rule.selectorText.replace(/:focus(-visible)?/g, ''))
    if (!applies) continue
    for (const name of Array.from(rule.style)) result.set(name, rule.style.getPropertyValue(name))
  }
  return result
}

// Bare controls get the tint as a soft pill: horizontal padding around the label, cancelled by an equal negative margin
// so the label stays where it was (first tab on the content edge, table header columns in place).
const PILLS = [
  { name: 'tab', markup: '<nav class="usage-views"><button>Breakdown</button></nav>', pad: 12 },
  { name: 'back', markup: '<button class="usage-back">Back</button>', pad: 12 },
  { name: 'clear filters', markup: '<button class="usage-clear">Clear filters</button>', pad: 12 },
  { name: 'tokens toggle', markup: '<div class="usage-summary"><button class="usage-summary-tokens">tokens</button></div>', pad: 12 },
  { name: 'retry', markup: '<div class="usage-state-error"><button>Retry</button></div>', pad: 12 },
  { name: 'sort header', markup: '<table class="usage-table"><thead><tr><th><button class="usage-sort-button">Cost</button></th></tr></thead></table>', pad: 10 },
  { name: 'identity', markup: '<table class="usage-table"><tbody><tr><td><button class="usage-identity">Agent</button></td></tr></tbody></table>', pad: 12 },
  { name: 'bar row', markup: '<div class="usage-bars"><button class="usage-bar-row">row</button></div>', pad: 12 },
  { name: 'counting rules', markup: '<footer class="usage-footer"><details class="usage-method"><summary>How usage is counted</summary></details></footer>', pad: 12 },
]
const ROUNDED = [...PILLS.map(({ name, markup }) => ({ name, markup })), CONTROLS[3]!, { name: 'drill', markup: '<table class="usage-table"><tbody><tr><td><button class="usage-drill">go</button></td></tr></tbody></table>' }]

// Left/right side of a padding or margin declared as a one-to-four value shorthand.
const side = (declarations: Map<string, string>, property: 'padding' | 'margin', edge: 'left' | 'right'): number => {
  const parts = (declarations.get(property) ?? '').split(/\s+/)
  const horizontal = parts[edge === 'left' ? 3 : 1] ?? parts[1] ?? parts[0]
  return Number.parseFloat(horizontal ?? '')
}

const load = (): void => {
  const style = document.createElement('style')
  style.textContent = usageCss
  document.head.append(style)
}

// Border declarations that draw a line: the corner radius and a reset to 0/none draw nothing.
const frameLines = (declarations: Map<string, string>): string[] =>
  [...declarations]
    .filter(([name, value]) => name.startsWith('border') && !name.endsWith('radius') && !/^(0(px)?|none)$/.test(value.trim()))
    .map(([name]) => name)

describe('usage analytics focus pill', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    document.head.innerHTML = ''
  })

  for (const { name, markup, pad } of PILLS) {
    it(`${name}: ${pad}px side padding cancelled by the same negative margin`, () => {
      load()
      const idle = declared(mount(markup, true), false)
      expect(side(idle, 'padding', 'left')).toBe(pad)
      expect(side(idle, 'padding', 'right')).toBe(pad)
      expect(side(idle, 'margin', 'left')).toBe(-pad)
      expect(side(idle, 'margin', 'right')).toBe(-pad)
    })
  }

  for (const { name, markup } of ROUNDED) {
    it(`${name}: rounded corners from the radius token`, () => {
      load()
      expect(declared(mount(markup, true), false).get('border-radius')).toBe('var(--radius-sm)')
    })
  }

  it('keeps the segmented buttons square inside their rounded frame', () => {
    load()
    for (const markup of [CONTROLS[1]!.markup, CONTROLS[2]!.markup]) expect(declared(mount(markup, true), false).get('border-radius')).toBe('0')
  })

  it('draws the selected tab underline across the label only, whatever the tab padding', () => {
    expect(usageCss).toMatch(/\.usage-views button::after\s*\{[^}]*inset:\s*auto 12px 0/)
    expect(usageCss).toMatch(/\.usage-views button\[aria-current="page"\]::after\s*\{[^}]*var\(--accent-primary\)/)
    expect(usageCss).not.toMatch(/\.usage-views button[^{]*\{[^}]*border-bottom/)
  })
})

describe('usage analytics focus look', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    document.head.innerHTML = ''
  })

  for (const { name, markup } of CONTROLS) {
    it(`${name}: no outline, a tint that the unfocused control lacks`, () => {
      const style = document.createElement('style')
      style.textContent = usageCss
      document.head.append(style)
      const control = mount(markup, true)

      const idle = declared(control, false)
      const focused = declared(control, true)
      expect(focused.get('outline')).toBe('none')
      expect(idle.get('background-image')).toBeUndefined()
      expect(focused.get('background-image')).toMatch(/linear-gradient\(var\(--usage-focus-tint\)/)
      expect(focused.get('color')).toBe('var(--text-primary)')
    })
  }

  it('the Models & agents entry has the same borderless focus look', () => {
    expect(usageCss).not.toMatch(/\.usage-entry:focus-visible\s*\{[^}]*outline:\s*2px/)
    expect(usageCss).toMatch(/\.usage-entry:focus-visible/)
  })

  it('draws no rectangular focus outline on any control of the details', () => {
    const focusRules = [...usageCss.matchAll(/([^{}]*:focus-visible[^{}]*)\{([^}]*)\}/g)]
    const outlined = focusRules.filter(([, selector, body]) => /outline:\s*\d/.test(body!) && !/select/.test(selector!))
    // only the bar-fill emphasis (not a control border) may keep an outline
    expect(outlined.map(([, selector]) => selector!.trim()).filter((s) => !s.includes('.usage-bar-fill'))).toEqual([])
  })

  it('keeps the selected and highlighted fills out of the background shorthand, which would wipe the focus tint', () => {
    const selected = usageCss.match(/\[aria-pressed="true"\]\s*\{([^}]*)\}/)?.[1] ?? ''
    const highlighted = usageCss.match(/tr\.is-highlighted\s*\{([^}]*)\}/)?.[1] ?? ''
    for (const body of [selected, highlighted]) {
      expect(body).toMatch(/background-color:/)
      expect(body).not.toMatch(/(^|[;\s])background:/)
    }
  })

  it('paints the tint from semantic tokens only, below the shipped tint cap', () => {
    const tint = usageCss.match(/--usage-focus-tint:\s*([^;]+);/)?.[1] ?? ''
    expect(tint).toBe('color-mix(in srgb, var(--text-primary) 16%, transparent)')
  })
})

// By model / By agent is a frameless group where only the selected item is tinted; Compare stays an outlined pill.
describe('usage analytics switches', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    document.head.innerHTML = ''
  })

  it('draws no frame around the By model / By agent group or between its buttons', () => {
    load()
    const button = mount(CONTROLS[1]!.markup, true)
    expect(frameLines(declared(button.parentElement!, false))).toEqual([])
    expect(frameLines(declared(button, false))).toEqual([])
    expect(declared(button.parentElement!, false).get('border-radius')).toBe('var(--radius-sm)')
  })

  it('marks the selected group button with the accent tint the other button lacks', () => {
    load()
    const idle = declared(mount(CONTROLS[1]!.markup, true), false)
    const selected = declared(mount('<div class="usage-segmented"><button aria-pressed="true">By agent</button></div>', true), false)
    expect(idle.get('background-color')).toBeUndefined()
    expect(selected.get('background-color')).toMatch(/var\(--accent-primary\)/)
    expect(selected.get('color')).toBe('var(--text-primary)')
    expect(selected.get('font-weight')).toBe('600')
  })

  it('keeps the Compare metric group an outlined pill', () => {
    load()
    const group = declared(mount(CONTROLS[2]!.markup, true).parentElement!, false)
    expect(frameLines(group)).not.toEqual([])
    expect([...group.values()].join(' ')).toMatch(/var\(--control-border\)/)
    expect(group.get('border-radius')).toBe('999px')
  })
})
