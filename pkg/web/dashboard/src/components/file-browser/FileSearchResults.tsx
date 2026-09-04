import { type ReactElement } from 'react'
import type { SearchResult, TreeEntry } from '../../api/files-client'

type FileSearchResultsProps = {
  // null — поиск ещё не запускался/очищен; result с entries — ответ бэкенда.
  result: SearchResult | null
  loading: boolean
  error: string | null
  onOpenFile: (entry: TreeEntry) => void
  onToggleSelect: (entry: TreeEntry) => void
  isSelected: (path: string) => boolean
  activePath: string | null
}

// dirPrefix возвращает каталог файла с завершающим слэшем ("pkg/server/") или
// пустую строку для файла в корне — приглушённый префикс перед именем.
function dirPrefix(path: string): string {
  const slash = path.lastIndexOf('/')
  return slash === -1 ? '' : path.slice(0, slash + 1)
}

// Плоский список результатов поиска: переиспользует классы дерева
// (file-tree-row/-name) ради единого вида и того же чекбокса/клика. Имя файла —
// основной текст, каталог — приглушённый префикс. Клавиатурную навигацию
// намеренно не тащим (MVP): клик открывает превью, чекбокс — выбор.
export function FileSearchResults({ result, loading, error, onOpenFile, onToggleSelect, isSelected, activePath }: FileSearchResultsProps): ReactElement {
  if (error !== null) return <div className="file-tree-hint file-tree-error">{error}</div>
  if (loading && result === null) return <div className="file-tree-hint">Searching…</div>
  if (result === null) return <div className="file-tree-hint">Type to search</div>
  if (result.entries.length === 0) return <div className="file-tree-hint">No files match</div>

  return (
    <div className="file-search-results" role="list" aria-label="Search results">
      {result.entries.map((entry) => {
        const prefix = dirPrefix(entry.path)
        return (
          <div
            key={entry.path}
            role="listitem"
            data-kind="file"
            className={`file-tree-row file-search-row${activePath === entry.path ? ' active' : ''}`}
            onClick={() => onOpenFile(entry)}
          >
            <input
              type="checkbox"
              aria-label={`Select ${entry.name}`}
              checked={isSelected(entry.path)}
              onClick={(e) => e.stopPropagation()}
              onChange={() => onToggleSelect(entry)}
            />
            <span className="file-tree-icon file-tree-icon-file" aria-hidden="true">📄</span>
            <span className="file-search-name">
              {prefix !== '' && <span className="file-search-dir">{prefix}</span>}
              <span className="file-tree-name">{entry.name}</span>
            </span>
          </div>
        )
      })}
      {result.truncated && <div className="file-tree-hint">Showing first 200 results — refine your search</div>}
    </div>
  )
}
