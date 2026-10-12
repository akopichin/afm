import { describe, it, expect } from 'vitest'
// Authoritative source: the status→palette mapping now lives ONCE in base/tokens.css; the per-skin
// index.css files only supply the raw palette (--mint/--amber/…). These tests lock both halves of that
// contract: the single-source mapping (green == done only) and the DRY consolidation (no skin redeclares it).
import tokensCss from '../../skins/base/tokens.css?raw'
import gogaCss from '../../skins/goga/index.css?raw'
import coffeeCss from '../../skins/coffee/index.css?raw'
import graphiteCss from '../../skins/graphite/index.css?raw'
import novacorpsCss from '../../skins/novacorps/index.css?raw'

// decl reads the LAST declared value of a custom property in a stylesheet string (cascade order).
const decl = (css: string, name: string): string | undefined => {
  const re = new RegExp(`${name}\\s*:\\s*([^;]+);`, 'g')
  let last: string | undefined
  for (const m of css.matchAll(re)) last = m[1]?.trim() ?? last
  return last
}

describe('status-color mapping lives once in base/tokens.css', () => {
  it('reserves success-green for done only', () => {
    expect(decl(tokensCss, '--c-done')).toBe('var(--mint)')
    // The blink fix: planning/ready are active states, so they take the active (amber) family, NOT green.
    expect(decl(tokensCss, '--c-planning')).toBe('var(--amber)')
    expect(decl(tokensCss, '--c-ready')).toBe('var(--amber)')
    expect(decl(tokensCss, '--c-planning')).not.toBe('var(--mint)')
    expect(decl(tokensCss, '--c-ready')).not.toBe('var(--mint)')
  })

  it('maps the remaining active states to amber', () => {
    for (const k of ['--c-running', '--c-awaiting', '--c-revising', '--c-retrying', '--c-paused']) {
      expect(decl(tokensCss, k)).toBe('var(--amber)')
    }
  })

  it('maps attention/neutral states to their own families', () => {
    expect(decl(tokensCss, '--c-failed')).toBe('var(--coral)')
    expect(decl(tokensCss, '--c-hook-failed')).toBe('var(--c-failed)')
    expect(decl(tokensCss, '--c-awaiting-user')).toBe('var(--violet)')
    expect(decl(tokensCss, '--c-pending')).toBe('var(--ink-dim)')
  })
})

describe('DRY: no skin index.css redeclares the status-color map', () => {
  it('every skin leaves --c-* to the base layer', () => {
    for (const [name, css] of [
      ['goga', gogaCss],
      ['coffee', coffeeCss],
      ['graphite', graphiteCss],
      ['novacorps', novacorpsCss],
    ] as const) {
      expect(css, `${name} must not declare --c-* (hoisted to tokens.css)`).not.toMatch(/--c-[a-z-]+\s*:/)
    }
  })
})
