import { useEffect, useRef, useState, type ReactElement } from 'react'
import { getContent, getDiff, getRoots, getSearch, type FileContent, type FileDiff, type RootView, type SearchResult, type TreeEntry } from '../../api/files-client'
import { DiffViewer } from './DiffViewer'
import { FileSearchResults } from './FileSearchResults'
import { FileTree } from './FileTree'
import { FileViewer } from './FileViewer'

export type SelectedFile = { root: string; path: string; displayPath: string; reference: string }

export type FileBrowserModalProps = {
  // 'browse' — свободный просмотр проекта, кнопка снизу копирует референсы в
  // буфер. 'picker' — вызвано из pickFiles(onInsert), кнопка снизу отдаёт
  // референсы вызывающему (обычно PasteableTextarea, Task 14) и закрывает модалку.
  mode: 'browse' | 'picker'
  selection: SelectedFile[]
  onToggleSelect: (root: string, entry: TreeEntry) => void
  onRemoveSelect: (root: string, path: string) => void
  onClose: () => void
  onSubmit: () => void
}

type ActiveFile = { root: string; entry: TreeEntry }
type Tab = 'FILE' | 'DIFF'

const FOCUSABLE_SELECTOR = 'button:not([disabled]), [href], input:not([disabled]), select, textarea, [tabindex]:not([tabindex="-1"])'

// Живой файл может прийти с невалидной/пустой modified_at (см. бэкенд —
// content.go отдаёт то, что вернул stat()) — тогда просто ничего не
// показываем вместо "Invalid Date" в заголовке.
function formatModifiedAt(modifiedAt: string): string | null {
  if (modifiedAt === '') return null
  const d = new Date(modifiedAt)
  return Number.isNaN(d.getTime()) ? null : d.toLocaleString()
}

// Полноэкранная модалка файлового браузера: слева — список root'ов проекта +
// ленивое дерево (FileTree) выбранного root'а, справа — хлебная крошка (с
// меткой root'а — см. бриф: "identical relative paths from different roots
// don't look like one file") + таб FILE/DIFF, снизу — чипы выбранных файлов и
// кнопка Copy/Insert references. Владеет только своим UI-состоянием (root/дерево/
// таб/превью) — саму selection и mode ей передаёт FileBrowserProvider, чтобы
// выбор переживал закрытие/переоткрытие модалки (Modal размонтируется, когда
// закрыта — см. бриф "Renders FileBrowserModal when open").
export function FileBrowserModal({ mode, selection, onToggleSelect, onRemoveSelect, onClose, onSubmit }: FileBrowserModalProps): ReactElement {
  const [roots, setRoots] = useState<RootView[]>([])
  const [rootsError, setRootsError] = useState<string | null>(null)
  const [selectedRoot, setSelectedRoot] = useState<string | null>(null)
  const [activeFile, setActiveFile] = useState<ActiveFile | null>(null)
  const [activeTab, setActiveTab] = useState<Tab>('FILE')

  const [content, setContent] = useState<FileContent | null>(null)
  const [contentLoading, setContentLoading] = useState(false)
  const [contentError, setContentError] = useState<string | null>(null)
  // Reload — отдельные loading/error от начальной загрузки (contentLoading/
  // contentError): те двое блокируют весь FileViewer ("Loading file…"/
  // ошибка на весь пейн), а Reload по дизайну обязан НЕ стирать уже
  // показанный content — ни в полёте (никакого "loading flicker" на 304),
  // ни при ошибке (см. бриф finding 10). Поэтому свой флаг + свой баннер.
  const [reloading, setReloading] = useState(false)
  const [reloadError, setReloadError] = useState<string | null>(null)

  const [diff, setDiff] = useState<FileDiff | null>(null)
  const [diffLoading, setDiffLoading] = useState(false)
  const [diffError, setDiffError] = useState<Error | null>(null)

  const [query, setQuery] = useState('')
  const [searchResult, setSearchResult] = useState<SearchResult | null>(null)
  const [searchLoading, setSearchLoading] = useState(false)
  const [searchError, setSearchError] = useState<string | null>(null)
  // Поколение поиска: инкрементируется на КАЖДЫЙ запуск эффекта (смена запроса
  // или root'а). Поздний ответ устаревшего запроса/root'а сверяется с текущим
  // поколением и отбрасывается — та же техника, что rootGenerationRef в
  // FileTree и activeFileRef в handleReload.
  const searchGenRef = useRef(0)

  const modalRef = useRef<HTMLDivElement | null>(null)
  // handleReload — обработчик клика, а не эффект: у него нет своего cleanup,
  // чтобы пометить "cancelled" в замыкании, как это делают эффекты загрузки
  // выше/ниже. Вместо этого держим "текущий activeFile" в ref, обновляемом на
  // каждом рендере, и после await сверяем с ним ЗАПРОШЕННЫЙ файл — тот же
  // смысл, что и у cancelled-флага, просто через ref вместо замыкания эффекта.
  const activeFileRef = useRef<ActiveFile | null>(activeFile)
  activeFileRef.current = activeFile

  useEffect(() => {
    let cancelled = false
    void getRoots()
      .then((r) => {
        if (cancelled) return
        setRoots(r)
        if (r.length > 0 && r[0] !== undefined) setSelectedRoot(r[0].id)
      })
      .catch((e: unknown) => {
        if (cancelled) return
        setRootsError(e instanceof Error ? e.message : 'failed to load roots')
      })
    return () => {
      cancelled = true
    }
  }, [])

  // Загружаем контент только когда реально показываем таб FILE — DIFF не
  // нужен, пока пользователь на него не переключился (см. следующий эффект).
  useEffect(() => {
    if (activeFile === null || activeTab !== 'FILE') return
    let cancelled = false
    setContent(null)
    setContentError(null)
    setReloadError(null)
    setReloading(false)
    setContentLoading(true)
    void getContent(activeFile.root, activeFile.entry.path)
      .then((c) => {
        if (cancelled) return
        setContentLoading(false)
        if (c !== undefined) setContent(c)
      })
      .catch((e: unknown) => {
        if (cancelled) return
        setContentLoading(false)
        setContentError(e instanceof Error ? e.message : 'failed to load file')
      })
    return () => {
      cancelled = true
    }
  }, [activeFile, activeTab])

  useEffect(() => {
    if (activeFile === null || activeTab !== 'DIFF') return
    let cancelled = false
    setDiff(null)
    setDiffError(null)
    setDiffLoading(true)
    void getDiff(activeFile.root, activeFile.entry.path)
      .then((d) => {
        if (cancelled) return
        setDiffLoading(false)
        setDiff(d)
      })
      .catch((e: unknown) => {
        if (cancelled) return
        setDiffLoading(false)
        setDiffError(e instanceof Error ? e : new Error('failed to load diff'))
      })
    return () => {
      cancelled = true
    }
  }, [activeFile, activeTab])

  // Поиск с debounce ~250мс и отменой устаревшего запроса. Пустой запрос —
  // немедленно сбрасываем результаты (дерево показывается снова), без сетевого
  // вызова. Смена root'а при непустом запросе повторяет поиск в новом root'е
  // (эффект зависит и от selectedRoot).
  useEffect(() => {
    if (selectedRoot === null) return
    const trimmed = query.trim()
    if (trimmed === '') {
      setSearchResult(null)
      setSearchError(null)
      setSearchLoading(false)
      return
    }
    const generation = ++searchGenRef.current
    const controller = new AbortController()
    // Сбрасываем результаты СРАЗУ, а не только при ошибке/успехе — иначе при
    // смене root'а (или запроса) устаревшие строки предыдущего root'а остаются
    // на экране под loading=true, и клик по строке в этом окне мог открыть файл
    // из ЧУЖОГО root'а. Generation guard/abort ниже не спасают от этого:
    // они лишь не дают ЗАПИСАТЬ устаревший ответ, но не чистят уже показанное.
    setSearchResult(null)
    setSearchLoading(true)
    setSearchError(null)
    const timer = setTimeout(() => {
      void getSearch(selectedRoot, trimmed, controller.signal)
        .then((r) => {
          if (generation !== searchGenRef.current) return
          setSearchLoading(false)
          setSearchResult(r)
        })
        .catch((e: unknown) => {
          // Отменённый запрос (abort) и устаревшее поколение — не ошибки UI.
          if (controller.signal.aborted || generation !== searchGenRef.current) return
          setSearchLoading(false)
          setSearchError(e instanceof Error ? e.message : 'search failed')
        })
    }, 250)
    return () => {
      clearTimeout(timer)
      controller.abort()
    }
  }, [query, selectedRoot])

  // Esc закрывает, Tab крутит фокус по кругу внутри модалки (простая ловушка
  // фокуса — без сторонней библиотеки, весь фокусируемый набор пересчитывается
  // на каждый Tab, т.к. состав кнопок меняется вместе с selection/activeFile).
  useEffect(() => {
    const modal = modalRef.current
    modal?.querySelector<HTMLElement>(FOCUSABLE_SELECTOR)?.focus()

    function onKeyDown(e: KeyboardEvent): void {
      if (e.key === 'Escape') {
        e.preventDefault()
        onClose()
        return
      }
      if (e.key !== 'Tab' || modal === null) return
      const items = Array.from(modal.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR))
      const first = items[0]
      const last = items[items.length - 1]
      if (first === undefined || last === undefined) return
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault()
        last.focus()
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault()
        first.focus()
      }
    }

    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [onClose])

  function openFile(root: string, entry: TreeEntry): void {
    setActiveFile({ root, entry })
    setActiveTab('FILE')
  }

  // Reload — повторный запрос ТОГО ЖЕ файла с If-None-Match: currentEtag
  // (в отличие от начальной загрузки выше, которая всегда идёт без etag).
  // 304 (getContent вернул undefined) — файл не менялся, оставляем content/
  // etag/modifiedAt как есть, никакого "loading flicker". 200 — заменяем
  // целиком. Ошибка — показываем инлайн-баннером, НЕ трогая content.
  //
  // Гонка (найдена ревью): пока Reload файла A летит, пользователь может
  // кликнуть файл B — эффект загрузки B применит свой (быстрый) ответ, а
  // МЕДЛЕННЫЙ ответ Reload'а A придёт позже и без проверки перезаписал бы
  // content B содержимым A. Фиксируем запрошенный файл (requested) и после
  // await сверяем с activeFileRef.current — если пользователь успел
  // переключиться на другой файл, ответ считается устаревшим и отбрасывается
  // целиком (включая reloading/reloadError — тот файл больше не на экране,
  // его reloading уже сброшен эффектом загрузки выше).
  async function handleReload(): Promise<void> {
    if (activeFile === null || content === null) return
    const requested = activeFile
    setReloading(true)
    setReloadError(null)
    let updated: FileContent | undefined
    let failure: string | null = null
    try {
      updated = await getContent(requested.root, requested.entry.path, content.etag)
    } catch (e: unknown) {
      failure = e instanceof Error ? e.message : 'failed to reload file'
    }
    const current = activeFileRef.current
    if (current === null || current.root !== requested.root || current.entry.path !== requested.entry.path) return
    setReloading(false)
    if (failure !== null) {
      setReloadError(failure)
      return
    }
    if (updated !== undefined) setContent(updated)
  }

  function rootLabel(rootId: string): string {
    return roots.find((r) => r.id === rootId)?.label ?? rootId
  }

  const submitLabel = mode === 'picker' ? 'Insert references' : 'Copy references'

  return (
    <div className="modal-overlay file-browser-overlay" role="dialog" aria-modal="true" aria-label="Browse project files">
      <div className="modal-content file-browser-modal" ref={modalRef}>
        <header className="file-browser-header">
          <h2>{mode === 'picker' ? 'Insert file references' : 'Browse project files'}</h2>
          <button type="button" className="icon-btn" aria-label="Close" onClick={onClose}>
            ✕
          </button>
        </header>

        <div className="file-browser-body">
          <aside className="file-browser-roots">
            {rootsError !== null && <div className="file-tree-hint file-tree-error">{rootsError}</div>}
            <ul className="file-browser-root-list">
              {roots.map((r) => (
                <li key={r.id}>
                  <button
                    type="button"
                    aria-pressed={r.id === selectedRoot}
                    className={`file-browser-root${r.id === selectedRoot ? ' active' : ''}`}
                    onClick={() => setSelectedRoot(r.id)}
                  >
                    {r.label}
                  </button>
                </li>
              ))}
            </ul>
            {selectedRoot !== null && (
              <>
                <div className="file-search-box">
                  <input
                    type="text"
                    className="file-search-input"
                    placeholder={`Search in ${rootLabel(selectedRoot)}…`}
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                    aria-label={`Search in ${rootLabel(selectedRoot)}`}
                  />
                  {query !== '' && (
                    <button type="button" className="file-search-clear icon-btn" aria-label="Clear search" onClick={() => setQuery('')}>
                      ✕
                    </button>
                  )}
                </div>

                {query.trim() !== '' ? (
                  <FileSearchResults
                    result={searchResult}
                    loading={searchLoading}
                    error={searchError}
                    onOpenFile={(entry) => openFile(selectedRoot, entry)}
                    onToggleSelect={(entry) => onToggleSelect(selectedRoot, entry)}
                    isSelected={(path) => selection.some((f) => f.root === selectedRoot && f.path === path)}
                    activePath={activeFile?.root === selectedRoot ? activeFile.entry.path : null}
                  />
                ) : null}

                {/* Дерево остаётся смонтированным во время поиска (hidden, не
                    размонтирование) — очистка запроса возвращает раскрытые
                    каталоги без повторной загрузки. */}
                <div className="file-browser-tree-wrap" hidden={query.trim() !== ''}>
                  <FileTree
                    root={selectedRoot}
                    onOpenFile={(entry) => openFile(selectedRoot, entry)}
                    onToggleSelect={(entry) => onToggleSelect(selectedRoot, entry)}
                    isSelected={(path) => selection.some((f) => f.root === selectedRoot && f.path === path)}
                    activePath={activeFile?.root === selectedRoot ? activeFile.entry.path : null}
                  />
                </div>
              </>
            )}
          </aside>

          <section className="file-browser-content">
            <div className="file-browser-breadcrumb-row">
              <div className="file-browser-breadcrumb">
                {activeFile === null ? 'Select a file to preview' : `${rootLabel(activeFile.root)} / ${activeFile.entry.path}`}
              </div>
              {activeFile !== null && activeTab === 'FILE' && content !== null && (
                <span className="file-browser-file-meta">
                  {formatModifiedAt(content.modifiedAt) !== null && (
                    <span className="file-browser-modified">Modified {formatModifiedAt(content.modifiedAt)}</span>
                  )}
                  <button type="button" className="file-browser-reload-btn" disabled={reloading} onClick={() => void handleReload()}>
                    {reloading ? 'Reloading…' : 'Reload'}
                  </button>
                </span>
              )}
            </div>
            {reloadError !== null && activeTab === 'FILE' && <div className="file-browser-reload-error">{reloadError}</div>}
            <div className="file-browser-tabs" role="tablist">
              <button
                type="button"
                role="tab"
                aria-selected={activeTab === 'FILE'}
                className={`file-browser-tab${activeTab === 'FILE' ? ' active' : ''}`}
                onClick={() => setActiveTab('FILE')}
              >
                FILE
              </button>
              <button
                type="button"
                role="tab"
                aria-selected={activeTab === 'DIFF'}
                className={`file-browser-tab${activeTab === 'DIFF' ? ' active' : ''}`}
                onClick={() => setActiveTab('DIFF')}
              >
                DIFF
              </button>
            </div>
            <div className="file-browser-pane">
              {activeTab === 'FILE' ? (
                <FileViewer content={content} loading={contentLoading} error={contentError} />
              ) : (
                <DiffViewer diff={diff} loading={diffLoading} error={diffError} />
              )}
            </div>
          </section>
        </div>

        <footer className="file-browser-footer">
          <div className="file-browser-chips">
            {/* f.displayPath уже приходит от бэкенда в виде "<root label>/<path>"
                (workspace.FS.Reference — см. pkg/server/workspace/content.go) —
                повторно приписывать rootLabel(f.root) спереди значило бы задвоить
                метку root'а в каждом чипе. */}
            {selection.map((f) => (
              <span key={`${f.root}:${f.path}`} className="file-chip">
                {f.displayPath}
                <button type="button" aria-label={`Remove ${f.displayPath}`} onClick={() => onRemoveSelect(f.root, f.path)}>
                  ✕
                </button>
              </span>
            ))}
          </div>
          <button type="button" className="btn btn-approve" disabled={selection.length === 0} onClick={onSubmit}>
            {submitLabel}
          </button>
        </footer>
      </div>
    </div>
  )
}
