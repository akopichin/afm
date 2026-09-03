import type { ReactElement } from 'react'
import type { FileContent } from '../../api/files-client'
import { highlight } from './highlight'

type FileViewerProps = {
  content: FileContent | null
  loading: boolean
  error: string | null
}

// Показывает содержимое выбранного файла. ЕДИНСТВЕННОЕ место во всём
// дереве file-browser, где используется dangerouslySetInnerHTML — и оно
// заведомо безопасно: строка приходит только из highlight() (highlight.ts),
// которая либо экранирует исходник целиком (язык 'plain'/незарегистрированный),
// либо прогоняет его через hljs.highlight (сам hljs экранирует текст внутри
// найденных токенов). Контент НИКОГДА не идёт через markdown-it — в отличие
// от плана/диалога (PlanPanel/DialogChannel), это чужой исходный код проекта,
// а не markdown, написанный агентом.
export function FileViewer({ content, loading, error }: FileViewerProps): ReactElement {
  if (loading) return <div className="file-viewer-hint">Loading file…</div>
  if (error !== null) return <div className="file-viewer-hint file-viewer-error">{error}</div>
  if (content === null) return <div className="file-viewer-hint">Select a file to preview</div>

  const html = highlight(content.language, content.content)

  return (
    <pre className="file-viewer-source">
      <code className={`hljs language-${content.language}`} dangerouslySetInnerHTML={{ __html: html }} />
    </pre>
  )
}
