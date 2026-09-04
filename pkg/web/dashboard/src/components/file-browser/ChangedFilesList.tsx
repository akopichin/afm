import { type ReactElement } from 'react'
import type { ChangeList, ChangeEntry, ChangeStatus, TreeEntry } from '../../api/files-client'

type ChangedFilesListProps = {
  // null — изменения ещё не загружены/очищены; result с entries — ответ бэкенда.
  result: ChangeList | null
  loading: boolean
  error: string | null
  onOpenFile: (entry: TreeEntry) => void
  onToggleSelect: (entry: TreeEntry) => void
  isSelected: (path: string) => boolean
  activePath: string | null
}

const BADGE: Record<ChangeStatus, string> = { modified: 'M', added: 'A', deleted: 'D' }

// dirPrefix возвращает каталог файла с завершающим слэшем ("pkg/server/") или
// пустую строку для файла в корне — приглушённый префикс перед именем.
function dirPrefix(path: string): string {
  const slash = path.lastIndexOf('/')
  return slash === -1 ? '' : path.slice(0, slash + 1)
}

// Плоский список изменённых файлов. Цвет badge — не единственный сигнал: буква
// статуса (M/A/D) всегда видна текстом. Selectable-строка открывается настоящей
// кнопкой и имеет чекбокс (как FileSearchResults); неселектируемая
// (deleted/vanished) строка приглушена, некликабельна и несёт доступный текст
// причины вместо чекбокса/кнопки.
export function ChangedFilesList({ result, loading, error, onOpenFile, onToggleSelect, isSelected, activePath }: ChangedFilesListProps): ReactElement {
  if (error !== null) return <div className="file-tree-hint file-tree-error">{error}</div>
  if (loading && result === null) return <div className="file-tree-hint">Loading changes…</div>
  if (result === null) return <div className="file-tree-hint">Loading changes…</div>
  if (result.entries.length === 0) return <div className="file-tree-hint">No changes</div>

  return (
    <div className="file-search-results" role="list" aria-label="Changed files">
      {result.entries.map((entry: ChangeEntry) => {
        const prefix = dirPrefix(entry.path)
        const badge = (
          <span className={`change-badge change-badge-${entry.status}`} aria-hidden="true">
            {BADGE[entry.status]}
          </span>
        )
        if (!entry.selectable) {
          return (
            <div key={entry.path} role="listitem" data-kind="file" className="file-tree-row file-search-row file-change-row-disabled">
              {badge}
              <span className="file-search-name">
                {prefix !== '' && <span className="file-search-dir">{prefix}</span>}
                <span className="file-tree-name">{entry.name}</span>
              </span>
              <span className="file-change-reason">Deleted or unavailable in the working tree</span>
            </div>
          )
        }
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
              {badge}
              <span className="file-search-name">
                {prefix !== '' && <span className="file-search-dir">{prefix}</span>}
                <span className="file-tree-name">{entry.name}</span>
              </span>
            </button>
          </div>
        )
      })}
      {result.truncated && <div className="file-tree-hint">Some changes are not shown</div>}
    </div>
  )
}
