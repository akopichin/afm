import { describe, expect, it } from 'vitest'
import usageCss from '../../skins/base/usage-analytics.css?raw'
import tokensCss from '../../skins/base/tokens.css?raw'

// CSS has no browser here, so these pin the structural promises the live acceptance measures: equal cell padding,
// a layout that follows the workspace width and a single colour source for every model mark.

const block = (selector: string): string => {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  return usageCss.match(new RegExp(`(?:^|\\n)${escaped}\\s*\\{([^}]*)\\}`))?.[1] ?? ''
}

describe('usage analytics stylesheet', () => {
  it('gives header and body cells the same horizontal padding', () => {
    const base = block('.usage-table th, .usage-table td')
    expect(base).toMatch(/padding:\s*12px;/)
    // every override of the horizontal padding names both cell kinds in one rule
    const overrides = [...usageCss.matchAll(/([^{}]*)\{[^}]*padding-inline[^}]*\}/g)].map((m) => m[1]!.trim())
    expect(overrides.length).toBeGreaterThan(0)
    for (const selector of overrides.filter((s) => s.startsWith('.usage-table th'))) expect(selector).toContain('.usage-table td')
  })

  it('turns the table into cards by the width of the workspace, not of the viewport', () => {
    expect(usageCss).toMatch(/\.usage-details\s*\{[^}]*container:\s*usage \/ inline-size/)
    expect(usageCss).toMatch(/@container usage \(max-width: 640px\)/)
    expect(usageCss).not.toMatch(/@media/)
  })

  it('keeps the table on screen at 1024px, where the workspace is about 724px wide', () => {
    // the narrowest container breakpoint is the card layout
    const widths = [...usageCss.matchAll(/@container usage \(max-width: (\d+)px\)/g)].map((m) => Number(m[1]))
    const cardsBelow = Math.min(...widths)
    expect(cardsBelow).toBeLessThan(724)
  })

  it('paints every model mark from the one --model-color property', () => {
    for (const selector of ['.usage-model-dot', '.usage-bar-fill', '.usage-timeline-job']) {
      expect(block(selector), selector).toMatch(/background:\s*var\(--model-color\)/)
    }
  })

  it('sticks the header inside the details scroll', () => {
    expect(block('.usage-table th')).toMatch(/position:\s*sticky/)
    expect(block('.usage-scroll')).toMatch(/overflow-y:\s*auto/)
  })

  it('declares the twelve model colours for both themes', () => {
    for (let i = 0; i < 12; i++) expect(tokensCss.match(new RegExp(`--model-color-${i}:`, 'g'))).toHaveLength(2)
  })
})
