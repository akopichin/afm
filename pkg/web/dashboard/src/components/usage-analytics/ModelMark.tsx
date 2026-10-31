import type { ReactElement } from 'react'
import { modelColorStyle } from '../../lib/model-colors'

// ModelMark — цветная метка модели; цвет не единственный носитель смысла, рядом всегда стоит текст.
export function ModelMark({ ordinal }: { ordinal: number | null }): ReactElement {
  return <span className="usage-model-dot" style={modelColorStyle(ordinal)} aria-hidden="true" />
}
