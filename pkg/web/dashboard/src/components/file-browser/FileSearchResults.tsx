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

// Плоский список результатов поиска: переиспользует классы дерева ради единого
// вида. Имя файла — основной текст, каталог — приглушённый префикс. Открывающая
// часть строки — настоящая <button> (нативный фокус + Enter/Space), чтобы поиск
// не терял клавиатурную доступность дерева; чекбокс — отдельный сосед. В
// aria-label чекбокса — полный путь: одинаковые basename из разных каталогов
// иначе неразличимы для скринридера.
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
          >
            <input
              type="checkbox"
              aria-label={`Select ${entry.path}`}
              checked={isSelected(entry.path)}
              onChange={() => onToggleSelect(entry)}
            />
            <button type="button" className="file-search-open" onClick={() => onOpenFile(entry)}>
              <span className="file-tree-icon file-tree-icon-file" aria-hidden="true">📄</span>
              <span className="file-search-name">
                {prefix !== '' && <span className="file-search-dir">{prefix}</span>}
                <span className="file-tree-name">{entry.name}</span>
              </span>
            </button>
          </div>
        )
      })}
      {result.truncated && <div className="file-tree-hint">Some matches may be hidden — refine your search</div>}
    </div>
  )
}
