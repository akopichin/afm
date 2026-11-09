import type { ReactElement } from 'react'
import { renderMarkdownCached } from './markdown'

type MarkdownRendererProps = {
  source: string
}

// Тонкая React-обёртка над npm-пакетом markdown-it. Конфигурация и спецобработка (спецсекции, чекбоксы,
// ссылки) — в markdown.ts; экземпляр парсера создаётся один раз на модуль.
export function MarkdownRenderer({ source }: MarkdownRendererProps): ReactElement {
  return <div className="md" dangerouslySetInnerHTML={{ __html: renderMarkdownCached(source) }} />
}
