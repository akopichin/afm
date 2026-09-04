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
// Full status word for screen readers — the visible badge letter is aria-hidden
// (color is decorative), so the status must reach the accessible name some other
// way, or an added file is indistinguishable from a modified/deleted one.
const STATUS_LABEL: Record<ChangeStatus, string> = { modified: 'Modified', added: 'Added', deleted: 'Deleted' }

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
// Контейнер с aria-label="Changed files" оборачивает ВСЕ состояния (loading/
// error/пусто/список), а не только непустой список — иначе переключение на
// Unstaged/vs HEAD в репозитории без изменений оставляло бы панель вообще без
// доступного имени (FileBrowserModal ждёт именно эту метку, чтобы понять, что
// панель изменений уже смонтирована и поисковая колонка ей уступила место).
export function ChangedFilesList({ result, error, onOpenFile, onToggleSelect, isSelected, activePath }: ChangedFilesListProps): ReactElement {
  return (
    <div className="file-search-results" role="list" aria-label="Changed files">
      {error !== null ? (
        <div className="file-tree-hint file-tree-error">{error}</div>
      ) : result === null ? (
        <div className="file-tree-hint">Loading changes…</div>
      ) : result.entries.length === 0 ? (
        <div className="file-tree-hint">No changes</div>
      ) : (
        <>
          {result.entries.map((entry: ChangeEntry) => {
            const prefix = dirPrefix(entry.path)
            const badge = (
              <>
                <span className={`change-badge change-badge-${entry.status}`} aria-hidden="true">
                  {BADGE[entry.status]}
                </span>
                {/* Screen-reader-only status word: the visible letter above is
                    aria-hidden, so without this an added row sounds identical to
                    a modified/deleted one. */}
                <span className="file-browser-sr-only">{STATUS_LABEL[entry.status]}: </span>
              </>
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
        </>
      )}
    </div>
  )
}
