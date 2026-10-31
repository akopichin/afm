import type { CSSProperties } from 'react'

// Цвет модели — чистая функция её порядкового номера в ране (color_ordinal с сервера), поэтому он не меняется
// от фильтров, сортировок и перезагрузки. Первые 12 цветов — токены skins/base/tokens.css, дальше хью идёт
// золотым углом: соседние номера далеко друг от друга, а цвета первых моделей не повторяются.
export const PALETTE_SIZE = 12
export const EXTENDED_FIRST_ORDINAL = PALETTE_SIZE

const GOLDEN_ANGLE_DEG = 137.508
const FIRST_EXTENDED_HUE_DEG = 40

export function extendedHue(ordinal: number): number {
  return ((ordinal - EXTENDED_FIRST_ORDINAL) * GOLDEN_ANGLE_DEG + FIRST_EXTENDED_HUE_DEG) % 360
}

export function modelColor(ordinal: number | null): string {
  if (ordinal === null || !Number.isInteger(ordinal) || ordinal < 0) return 'var(--model-color-unknown)'
  if (ordinal < PALETTE_SIZE) return `var(--model-color-${ordinal})`
  return `oklch(var(--model-extra-lightness) var(--model-extra-chroma) ${extendedHue(ordinal).toFixed(1)})`
}

// Каждая метка модели (точка, полоса, отрезок на шкале, легенда) рисуется из одного свойства --model-color.
export function modelColorStyle(ordinal: number | null): CSSProperties {
  return { '--model-color': modelColor(ordinal) } as CSSProperties
}
