import type { ReactElement } from 'react'
import { renderMarkdown } from './markdown'

type MarkdownRendererProps = {
  source: string
}

// Тонкая React-обёртка над npm-пакетом markdown-it. Конфигурация и спецобработка
// (спецсекции, чекбоксы, ссылки) — в markdown.ts; экземпляр парсера создаётся один
// раз на модуль. Соответствует renderMarkdownInto в текущем app.js.
export function MarkdownRenderer({ source }: MarkdownRendererProps): ReactElement {
  return <div className="md" dangerouslySetInnerHTML={{ __html: renderMarkdown(source) }} />
}
