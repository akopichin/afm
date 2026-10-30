import type { ReactElement } from 'react'
import { FilesApiError, type FileDiff } from '../../api/files-client'

type DiffViewerProps = {
  diff: FileDiff | null
  loading: boolean
  error: Error | null
}

// Рендерит FileDiff построчно.
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

  // split на строке с завершающим \n даёт лишний хвостовой '' элемент — убираем его, чтобы не рисовать
  // пустую строку в конце; настоящие пустые context-строки внутри диффа не трогаем.
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
          {diffRows(lines).map((row, index) => (
            <div key={index} className={`diff-line ${row.className}`}>
              <span className="diff-gutter">{row.gutter}</span>
              <span className="diff-text">{row.text}</span>
            </div>
          ))}
        </pre>
      )}
    </div>
  )
}

type DiffRow = { className: string; gutter: string; text: string }

const HUNK_START = /^@@ -\d+(?:,\d+)? \+(\d+)/

// diffRows превращает строки unified diff в строки с гутером: контекст — номер строки нового файла, добавление
// и удаление — знак; сам знак из текста убирается. Добавление тоже занимает строку нового файла — счётчик растёт.
function diffRows(lines: string[]): DiffRow[] {
  let newLine = 0
  let inHunk = false
  return lines.map((line): DiffRow => {
    const hunk = HUNK_START.exec(line)
    if (hunk !== null) {
      inHunk = true
      newLine = Number(hunk[1])
      return { className: 'diff-line-hunk', gutter: '', text: line }
    }
    const header = !inHunk && (line.startsWith('+++') || line.startsWith('---') || !/^[ +\-@\\]/.test(line))
    if (header || line.startsWith('\\')) return { className: 'diff-line-header', gutter: '', text: line }
    if (line.startsWith('+')) {
      newLine++
      return { className: 'diff-line-add', gutter: '+', text: line.slice(1) }
    }
    if (line.startsWith('-')) return { className: 'diff-line-del', gutter: '−', text: line.slice(1) }
    return { className: 'diff-line-context', gutter: String(newLine++), text: line.slice(1) }
  })
}
