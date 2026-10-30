import { describe, expect, it } from 'vitest'
import tokensCss from '../../skins/base/tokens.css?raw'
import {
  COST_RAIL_TEXT_TOKENS,
  DIALOG_TEXT_TOKENS,
  MEASURED_TEXT_TOKENS,
  MODES,
  NEUTRAL_TEXT_TOKENS,
  SKINS,
  TONE_TEXT_TOKENS,
  colorOf,
  contrast,
  paletteOf,
  type Vars,
} from './palette'

// Checks WCAG contrast of the semantic pairs the dashboard relies on in every skin x mode palette.

const SURFACES = ['--surface-canvas', '--surface-raised', '--surface-overlay', '--surface-agent-message', '--surface-user-message', '--surface-tool']
const SOLID_SURFACES = ['--surface-canvas', '--surface-raised']
const FILLED_STATUSES = ['--c-planning', '--c-awaiting', '--c-revising', '--c-running', '--c-done', '--c-failed', '--c-retrying', '--c-awaiting-user', '--c-paused']

type Check = { fg: string; bg: string; min: number }

// Pairs every palette must satisfy. 4.5 is WCAG AA text; 3 is AA for large text, glyphs and UI boundaries.
const CHECKS: Check[] = [
  ...SURFACES.flatMap((bg) => NEUTRAL_TEXT_TOKENS.map((fg) => ({ fg, bg, min: 4.5 }))),
  // Tone text (links, syntax highlighting, chips) straight on every surface, before any tint is added.
  ...SURFACES.flatMap((bg) => TONE_TEXT_TOKENS.map((fg) => ({ fg, bg, min: 4.5 }))),
  ...SOLID_SURFACES.flatMap((bg) => [
    { fg: '--text-placeholder', bg, min: 4.5 },
    { fg: '--text-disabled', bg, min: 3 },
    { fg: '--control-border', bg, min: 3 },
    { fg: '--focus-ring', bg, min: 3 },
    ...['--attention', '--success', '--danger', '--accent-primary', '--c-pending', '--c-awaiting-user'].map((fg) => ({ fg, bg, min: 4.5 })),
    ...['--c-pending', ...FILLED_STATUSES].map((fg) => ({ fg, bg, min: 3 })),
  ]),
  // Filled rail node: the stage number and the done check are drawn in the canvas colour on the status fill.
  ...FILLED_STATUSES.map((bg) => ({ fg: '--surface-canvas', bg, min: 4.5 })),
  // Primary buttons (Approve, Revise, Send) draw canvas-coloured text on the accent or success fill, and on its
  // hover fill (a filter would lighten the text with it).
  ...['--accent-primary', '--success', '--accent-primary-hover', '--success-hover'].map((bg) => ({ fg: '--surface-canvas', bg, min: 4.5 })),
  // File-status badges (M, A, D) draw the raised-surface colour on the tone fill.
  ...['--attention', '--success', '--danger'].map((bg) => ({ fg: '--surface-raised', bg, min: 4.5 })),
  // Board task cards.
  ...['todo', 'in-progress', 'done'].map((kind) => ({ fg: `--task-${kind}`, bg: `--task-${kind}-bg`, min: 4.5 })),
]

describe('skin x mode palettes keep AA contrast for the semantic pairs', () => {
  it('measures every token the stylesheets may paint as text', () => {
    const measuredElsewhere = [...DIALOG_TEXT_TOKENS, ...COST_RAIL_TEXT_TOKENS]
    expect(MEASURED_TEXT_TOKENS.filter((token) => !CHECKS.some(({ fg }) => fg === token) && !measuredElsewhere.includes(token))).toEqual([])
  })

  for (const [skin, css] of Object.entries(SKINS)) {
    for (const mode of MODES) {
      it(`${skin} ${mode}`, () => {
        const vars = paletteOf(css, mode)
        const failures = CHECKS.flatMap(({ fg, bg, min }) => {
          const ratio = contrast(fg, [bg], vars)
          return ratio >= min ? [] : [`${fg} on ${bg}: ${ratio.toFixed(2)} < ${min}`]
        })
        expect(failures).toEqual([])
      })
    }
  }
})

// Tints are painted from the -rgb tokens, so every palette must keep them equal to the tone they tint.
const RGB_TONES = ['--accent-primary', '--attention', '--success', '--danger']

describe('--*-rgb tint tokens equal the resolved tone in every palette', () => {
  for (const [skin, css] of Object.entries(SKINS)) {
    for (const mode of MODES) {
      it(`${skin} ${mode}`, () => {
        const vars = paletteOf(css, mode)
        const mismatches = RGB_TONES.flatMap((tone) => {
          const { r, g, b } = colorOf(vars, tone)
          const declared = vars.get(`${tone}-rgb`)?.replace(/\s+/g, '')
          return declared === `${r},${g},${b}` ? [] : [`${tone}-rgb is ${declared ?? 'undeclared'}, ${tone} is ${r},${g},${b}`]
        })
        expect(mismatches).toEqual([])
      })
    }
  }
})

// Small text sits on a translucent tint of its own tone. skins-contract.test.ts caps every shipped tint at this
// alpha, so measuring each text token at the cap on every surface covers all selectors without listing them.
const TINT_BUDGET = 0.18
// Two stacked washes, e.g. a selected row inside an attention card.
const STACKED_TINTS = [0.1, 0.16]
// Muted text inside a wash is swapped for secondary text by the neutral-wash rule, so only secondary is measured on
// washes, at the full budget. Skin rules that replace a hover fill with a tone wash use these alphas (coffee plan rows).
const PLAN_ROW_WASH_ALPHAS = [0.06, 0.09, 0.14]

// A surface is the opaque colour plus any translucent layers the dashboard puts on it.
const TEXT_SURFACES: [string, string[]][] = [
  ['canvas', ['--surface-canvas']],
  ['raised', ['--surface-raised']],
  ['overlay', ['--surface-overlay']],
  ['tool', ['--surface-tool']],
  ['user message', ['--surface-user-message']],
  ['agent message', ['--surface-agent-message']],
]
const HOVER = '--surface-hover'

// Mirrors the small-text recipe the CSS uses for a tone that has no dedicated --*-text token.
const toneText = (tone: string): string => `color-mix(in srgb, var(${tone}) var(--tone-text-mix), var(--text-primary))`

type ToneText = { label: string; fg: string; tone: string }

// Every status colour the CSS feeds into the text recipe (rail, chips, dots), read from tokens.css.
const STATUS_TONES = [...tokensCss.matchAll(/^\s*(--c-[\w-]+):/gm)].map((m) => m[1]!)

const TONE_TEXTS: ToneText[] = [
  { label: '--accent-text', fg: '--accent-text', tone: '--accent-primary' },
  { label: '--attention-text', fg: '--attention-text', tone: '--attention' },
  { label: '--success-text', fg: '--success-text', tone: '--success' },
  { label: '--danger-text', fg: '--danger-text', tone: '--danger' },
  ...[...STATUS_TONES, '--cost-warning'].map((tone) => ({ label: `${tone} recipe`, fg: toneText(tone), tone })),
]
const ALL_TONES = TONE_TEXTS.map(({ tone }) => tone)
// Tones that tint neutral text: the four semantic tones plus the dialog violet (--violet is the awaiting-user colour).
const WASH_TONES = [...RGB_TONES, '--c-awaiting-user']
// Secondary text sits on a light accent wash: selected and unread rows (pane surfaces) and Team reply quotes. A quote in
// the feed has its own opaque canvas, and yours sits on the primary text colour, so the user surface never gets one.
const ROW_WASH_ALPHA = 0.1
const QUOTE_WASH_ALPHA = 0.08
const PANE_SURFACES = ['canvas', 'raised', 'overlay']

// tintOf paints the tone at the given alpha the way the CSS does: from the -rgb token when the tone has one, from
// the resolved colour otherwise (status colours reach the CSS through color-mix on the tone itself).
function tintOf(vars: Vars, tone: string, alpha: number): string {
  if (RGB_TONES.includes(tone)) return `rgba(var(${tone}-rgb), ${alpha})`
  const { r, g, b } = colorOf(vars, tone)
  return `rgba(${r}, ${g}, ${b}, ${alpha})`
}

describe('small text keeps AA on every surface under a tint at the alpha budget', () => {
  for (const [skin, css] of Object.entries(SKINS)) {
    for (const mode of MODES) {
      it(`${skin} ${mode}`, () => {
        const vars = paletteOf(css, mode)
        const failures: string[] = []
        const measure = (label: string, fg: string, surface: string, layers: string[]): void => {
          const ratio = contrast(fg, layers, vars)
          if (ratio < 4.5) failures.push(`${label} on ${surface}: ${ratio.toFixed(2)} < 4.5`)
        }
        for (const [surface, base] of TEXT_SURFACES) {
          for (const { label, fg, tone } of TONE_TEXTS) {
            measure(`${label} on own tint`, fg, surface, [...base, tintOf(vars, tone, TINT_BUDGET)])
            measure(`${label} on stacked own tints`, fg, surface, [...base, ...STACKED_TINTS.map((a) => tintOf(vars, tone, a))])
          }
          for (const tone of ALL_TONES) {
            measure(`--text-primary on ${tone} tint`, '--text-primary', surface, [...base, tintOf(vars, tone, TINT_BUDGET)])
          }
          // Syntax highlighting puts every tone text on the wash of a hovered or commented file line, so a tone text
          // also has to hold on the wash of each other semantic tone.
          for (const { label, fg } of TONE_TEXTS.slice(0, TONE_TEXT_TOKENS.length)) {
            if (surface === 'user message') break
            for (const tone of RGB_TONES) {
              measure(`${label} on a ${tone} wash`, fg, surface, [...base, tintOf(vars, tone, TINT_BUDGET)])
            }
          }
          for (const text of ['--text-muted', '--text-secondary']) {
            measure(`${text} on a hover`, text, surface, [...base, HOVER])
          }
          for (const tone of WASH_TONES) {
            measure(`--text-secondary on a ${tone} wash`, '--text-secondary', surface, [...base, tintOf(vars, tone, TINT_BUDGET)])
            // A user bubble holds no hoverable rows and no wash inside a wash; its only tint is the plain one above.
            if (surface === 'user message') continue
            measure(`--text-secondary on a hovered ${tone} wash`, '--text-secondary', surface, [...base, HOVER, tintOf(vars, tone, TINT_BUDGET)])
            measure(`--text-secondary on stacked ${tone} washes`, '--text-secondary', surface, [...base, ...STACKED_TINTS.map((a) => tintOf(vars, tone, a))])
          }
          for (const alpha of PLAN_ROW_WASH_ALPHAS) {
            measure(`--text-secondary on a plan row ${alpha} attention wash`, '--text-secondary', surface, [...base, tintOf(vars, '--attention', alpha)])
          }
          if (surface !== 'user message') {
            measure('--text-secondary on a quote wash', '--text-secondary', surface, [...base, tintOf(vars, '--accent-primary', QUOTE_WASH_ALPHA)])
          }
          if (PANE_SURFACES.includes(surface)) {
            measure('--text-secondary on a row wash', '--text-secondary', surface, [...base, tintOf(vars, '--accent-primary', ROW_WASH_ALPHA)])
          }
        }
        expect(failures).toEqual([])
      })
    }
  }
})

// Skin-specific text colours are literals per palette, so each is measured on the surfaces it is painted on.
describe('skin-specific text tokens keep AA where they are painted', () => {
  // The dialog panel sits on a pane surface under a wash up to the budget: the dialog violet, or the accent in graphite.
  const DIALOG_SURFACES = ['--surface-canvas', '--surface-raised', '--surface-overlay']
  const dialogWashTone = (skin: string): string => (skin === 'graphite' ? '--accent-primary' : '--c-awaiting-user')
  // The cost figure rests on the rail surface and turns to the strong token on a hovered or selected row.
  const RAIL_SURFACES = ['--surface-canvas', '--surface-raised', '--surface-overlay']
  for (const [skin, css] of Object.entries(SKINS)) {
    for (const mode of MODES) {
      it(`${skin} ${mode}`, () => {
        const vars = paletteOf(css, mode)
        const failures: string[] = []
        const measure = (label: string, fg: string, layers: string[]): void => {
          const ratio = contrast(fg, layers, vars)
          if (ratio < 4.5) failures.push(`${label}: ${ratio.toFixed(2)} < 4.5`)
        }
        for (const surface of DIALOG_SURFACES) {
          for (const fg of DIALOG_TEXT_TOKENS) {
            measure(`${fg} on ${surface}`, fg, [surface])
            measure(`${fg} on ${surface} under a dialog wash`, fg, [surface, tintOf(vars, dialogWashTone(skin), TINT_BUDGET)])
          }
        }
        for (const surface of RAIL_SURFACES) {
          measure(`--cost-rail on ${surface}`, '--cost-rail', [surface])
          measure(`--cost-rail-strong on ${surface}`, '--cost-rail-strong', [surface])
          measure(`--cost-rail-strong on ${surface} under a hover`, '--cost-rail-strong', [surface, HOVER])
          for (const tone of ['--accent-primary', '--attention']) {
            measure(`--cost-rail-strong on ${surface} under a ${tone} wash`, '--cost-rail-strong', [surface, tintOf(vars, tone, TINT_BUDGET)])
          }
        }
        expect(failures).toEqual([])
      })
    }
  }
})

// A board task card (and the progress note of its details) sits on a fixed-hue task fill, the same in every skin. The
// card swaps muted text for secondary (asserted in board-assistant-contract.test.ts), so primary and secondary are measured.
describe('skin text tokens keep AA on the board task fills', () => {
  for (const [skin, css] of Object.entries(SKINS)) {
    for (const mode of MODES) {
      it(`${skin} ${mode}`, () => {
        const vars = paletteOf(css, mode)
        const failures = ['todo', 'in-progress', 'done'].flatMap((kind) =>
          ['--text-primary', '--text-secondary'].flatMap((fg) => {
            const ratio = contrast(fg, [`--task-${kind}-bg`], vars)
            return ratio >= 4.5 ? [] : [`${fg} on --task-${kind}-bg: ${ratio.toFixed(2)} < 4.5`]
          }),
        )
        expect(failures).toEqual([])
      })
    }
  }
})
