import { describe, expect, it } from 'vitest'
import { EXTENDED_FIRST_ORDINAL, extendedHue, PALETTE_SIZE } from '../lib/model-colors'
import { colorOf, contrast, MODES, oklabDistance, oklchToRgba, paletteOf, SKINS, type Vars } from './palette'

// Model colours mark bars, dots and timeline jobs. Every mark also has a text label, so the bar to meet is the
// 3:1 floor for graphics on the two surfaces they sit on, plus enough spread to tell neighbours apart.

const SURFACES = ['--surface-raised', '--surface-canvas']
// 20 models: the 12 palette slots and 8 generated ones. Beyond that the colours get closer on the hue circle and
// the text label carries the identity.
const GENERATED = 8
const MIN_GRAPHIC_CONTRAST = 3
// A just-noticeable difference in OKLab is about 0.02; the first three colours are the ones every run uses.
const MIN_SPREAD = 0.02
const MIN_SPREAD_FROM_FIRST_THREE = 0.024

function colorsOf(vars: Vars): string[] {
  const palette = Array.from({ length: PALETTE_SIZE }, (_, i) => `--model-color-${i}`)
  const generated = Array.from({ length: GENERATED }, (_, i) => extendedColor(vars, EXTENDED_FIRST_ORDINAL + i))
  return [...palette, ...generated]
}

function extendedColor(vars: Vars, ordinal: number): string {
  return `oklch(${vars.get('--model-extra-lightness')} ${vars.get('--model-extra-chroma')} ${extendedHue(ordinal)})`
}

describe('model colours in every skin and mode', () => {
  for (const [skin, css] of Object.entries(SKINS)) {
    for (const mode of MODES) {
      const vars = paletteOf(css, mode)

      it(`${skin} ${mode}: every colour reaches 3:1 on both surfaces`, () => {
        const failures = colorsOf(vars).flatMap((color) =>
          SURFACES.flatMap((surface) => {
            const ratio = contrast(color, [surface], vars)
            return ratio >= MIN_GRAPHIC_CONTRAST ? [] : [`${color} on ${surface}: ${ratio.toFixed(2)}`]
          }),
        )
        expect(failures).toEqual([])
      })

      it(`${skin} ${mode}: colours are distinct and the first three are never repeated`, () => {
        const colors = colorsOf(vars).map((color) => colorOf(vars, color))
        const tooClose: string[] = []
        for (let i = 0; i < colors.length; i++) {
          for (let j = i + 1; j < colors.length; j++) {
            const min = i < 3 ? MIN_SPREAD_FROM_FIRST_THREE : MIN_SPREAD
            const distance = oklabDistance(colors[i]!, colors[j]!)
            if (distance < min) tooClose.push(`#${i} and #${j}: ${distance.toFixed(3)}`)
          }
        }
        expect(tooClose).toEqual([])
      })
    }
  }

  it('the Unknown bucket stays a neutral text-grade token', () => {
    for (const [skin, css] of Object.entries(SKINS)) {
      for (const mode of MODES) {
        expect(contrast('--model-color-unknown', ['--surface-raised'], paletteOf(css, mode)), `${skin} ${mode}`).toBeGreaterThanOrEqual(3)
      }
    }
  })
})

describe('OKLCH conversion used by the palette checks', () => {
  it('maps the lightness axis to white and black and an achromatic grey to equal channels', () => {
    expect(oklchToRgba(1, 0, 0)).toEqual({ r: 255, g: 255, b: 255, a: 1 })
    expect(oklchToRgba(0, 0, 0)).toEqual({ r: 0, g: 0, b: 0, a: 1 })
    const grey = oklchToRgba(0.6, 0, 120)
    expect(grey.r).toBe(grey.g)
    expect(grey.g).toBe(grey.b)
  })

  it('reads an oklch() literal like the browser resolves it and puts hue 29 on the red side', () => {
    const red = colorOf(new Map(), 'oklch(63% 0.2 29)')
    expect(red.r).toBeGreaterThan(red.g)
    expect(red.r).toBeGreaterThan(red.b)
  })

  it('measures identical colours as zero apart and different hues as apart', () => {
    const blue = oklchToRgba(0.7, 0.12, 250)
    expect(oklabDistance(blue, blue)).toBe(0)
    expect(oklabDistance(blue, oklchToRgba(0.7, 0.12, 40))).toBeGreaterThan(0.1)
  })
})
