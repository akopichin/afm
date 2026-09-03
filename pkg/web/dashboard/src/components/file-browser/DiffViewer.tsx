import type { ReactElement } from 'react'
import { FilesApiError, type FileDiff } from '../../api/files-client'

type DiffViewerProps = {
  diff: FileDiff | null
  loading: boolean
  error: Error | null
}

// Рендерит FileDiff построчно. НИКАКОГО dangerouslySetInnerHTML — в отличие
// от FileViewer, строки диффа не подсвечиваются (unified diff — не исходный
// код одного языка) и кладутся в DOM как обычные текстовые React-узлы, что
// уже само по себе безопасно (React экранирует текст ребёнка).
export function DiffViewer({ diff, loading, error }: DiffViewerProps): ReactElement {
  if (loading) return <div className="file-viewer-hint">Loading diff…</div>

  if (error !== null) {
    if (error instanceof FilesApiError && error.code === 'diff_unavailable') {
      return <div className="file-viewer-hint">Diff is not available for this file (no git history found)</div>
    }
    return <div className="file-viewer-hint file-viewer-error">{`Failed to load diff: ${error.message}`}</div>
  }

  if (diff === null) return <div className="file-viewer-hint">Select a file to see its diff</div>

  if (diff.binary) return <div className="file-viewer-hint">Binary file — diff not shown</div>

  // split('\n') на строке с завершающим \n (обычный случай unified diff)
  // даёт лишний хвостовой '' элемент — убираем его, чтобы не рисовать пустую
  // строку в конце; настоящие пустые context-строки внутри диффа не трогаем.
  const lines = diff.diff.length > 0 ? diff.diff.split('\n') : []
  if (lines.length > 0 && lines[lines.length - 1] === '') lines.pop()
  const hasChanges = diff.status !== 'clean' && lines.length > 0

  return (
    <div className="diff-viewer">
      {diff.truncated && <div className="diff-banner">Diff truncated — showing only the first portion</div>}
      {!hasChanges ? (
        <div className="file-viewer-hint">No changes</div>
      ) : (
        <pre className="diff-lines">
          {lines.map((line, index) => (
            <div key={index} className={`diff-line ${diffLineClass(line)}`}>
              {line}
            </div>
          ))}
        </pre>
      )}
    </div>
  )
}

function diffLineClass(line: string): string {
  if (line.startsWith('@@')) return 'diff-line-hunk'
  if (line.startsWith('+++') || line.startsWith('---')) return 'diff-line-header'
  if (line.startsWith('+')) return 'diff-line-add'
  if (line.startsWith('-')) return 'diff-line-del'
  return 'diff-line-context'
}
