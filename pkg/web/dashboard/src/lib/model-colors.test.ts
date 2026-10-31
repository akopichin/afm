import { describe, expect, it } from 'vitest'
import { EXTENDED_FIRST_ORDINAL, extendedHue, modelColor, modelColorStyle, PALETTE_SIZE } from './model-colors'

describe('modelColor', () => {
  it('takes the first twelve ordinals from the token palette', () => {
    expect(PALETTE_SIZE).toBe(12)
    expect(modelColor(0)).toBe('var(--model-color-0)')
    expect(modelColor(11)).toBe('var(--model-color-11)')
  })

  it('generates a colour for each later ordinal instead of cycling the palette', () => {
    const generated = Array.from({ length: 30 }, (_, i) => modelColor(EXTENDED_FIRST_ORDINAL + i))
    expect(generated.every((color) => color.startsWith('oklch(var(--model-extra-lightness) var(--model-extra-chroma) '))).toBe(true)
    expect(new Set(generated).size).toBe(generated.length)
  })

  it('is a pure function of the ordinal, so a colour never moves', () => {
    expect(modelColor(14)).toBe(modelColor(14))
    expect(extendedHue(14)).toBe(extendedHue(14))
  })

  it('gives the Unknown bucket and a bad ordinal the neutral colour', () => {
    expect(modelColor(null)).toBe('var(--model-color-unknown)')
    expect(modelColor(-1)).toBe('var(--model-color-unknown)')
    expect(modelColor(1.5)).toBe('var(--model-color-unknown)')
  })

  it('exposes the colour as one custom property for every mark of the model', () => {
    expect(modelColorStyle(2)).toEqual({ '--model-color': 'var(--model-color-2)' })
  })
})
