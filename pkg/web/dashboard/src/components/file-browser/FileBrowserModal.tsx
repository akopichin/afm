import { useEffect, useLayoutEffect, useRef, useState, type ReactElement } from 'react'
import {
  getChanged,
  getRoots,
  getSearch,
  type ChangeList,
  type RootView,
  type SearchResult,
  type TreeEntry,
} from '../../api/files-client'
import { ChangedFilesList } from './ChangedFilesList'
import { DiffViewer } from './DiffViewer'
import { FileCommentsList, type CommentTarget } from './FileCommentsList'
import { FileSearchResults } from './FileSearchResults'
import { FileTree } from './FileTree'
import { FileViewer } from './FileViewer'
import { useFilePreview, type ActiveFile, type PreviewTab } from './use-file-preview'
import { visibleFocusables } from '../../lib/focus-trap'
import { useComments } from '../comments/CommentProvider'
import { FileCommentSendDialog, FileCommentSendStatus } from '../comments/FileCommentSendDialog'
import { fileKey, isSendableFileComment } from '../comments/file-comment-source'
import { fileGroupKey } from './draft-comments'

export type SelectedFile = { root: string; path: string; displayPath: string; reference: string }

// BrowserView — что открыто в Files; провайдер хранит его между закрытиями модалки.
export type BrowserView = { root: string | null; file: ActiveFile | null; tab: PreviewTab; scrollTop: number }

export const EMPTY_BROWSER_VIEW: BrowserView = { root: null, file: null, tab: 'FILE', scrollTop: 0 }

export type FileBrowserModalProps = {
  initialView?: BrowserView
  onViewChange?: (view: BrowserView) => void
  // 'browse' — свободный просмотр проекта, кнопка снизу копирует референсы в буфер.
  mode: 'browse' | 'picker'
  selection: SelectedFile[]
  onToggleSelect: (root: string, entry: TreeEntry) => void
  onRemoveSelect: (root: string, path: string) => void
  onClose: () => void
  onSubmit: () => void
  // Кнопка ждущего действия закрывает Files и открывает вопрос или approval; без ожидания скрыта.
  attentionShortcut?: { label: string; onActivate: () => void } | null
}

// Resizable left panel: the file list/tree column can be dragged wider/narrower via the divider between it
// and the preview pane. The width persists across opens.
const LEFT_MIN = 220
const RIGHT_MIN = 300
const KEY_STEP = 24
const DEFAULT_LEFT_WIDTH = 300
const LEFT_WIDTH_KEY = 'afm.fileBrowser.leftWidth'

function loadLeftWidth(): number {
  try {
    const saved = Number(localStorage.getItem(LEFT_WIDTH_KEY))
    if (Number.isFinite(saved) && saved >= LEFT_MIN) return saved
  } catch {
    // localStorage unavailable (private mode / SSR) — fall back to the default
  }
  return DEFAULT_LEFT_WIDTH
}

// После успешной отправки открывший диалог элемент пропадает (карточка) или отключается (Send all (0)):
// фокус уходит на статус «Accepted», иначе на кнопку закрытия Files, чтобы Tab не утёк на страницу под модалкой.
function sendReturnTarget(opener: HTMLElement, modal: HTMLElement | null, closeButton: HTMLElement | null): HTMLElement | null {
  if (opener.isConnected && !(opener instanceof HTMLButtonElement && opener.disabled)) return opener
  return modal?.querySelector<HTMLElement>('.file-comment-send-status button') ?? closeButton
}

// Живой файл может прийти с невалидной/пустой modified_at (см. бэкенд — content.go отдаёт то, что вернул
// stat()) — тогда просто ничего не показываем вместо "Invalid Date" в заголовке.
function formatModifiedAt(modifiedAt: string): string | null {
  if (modifiedAt === '') return null
  const d = new Date(modifiedAt)
  return Number.isNaN(d.getTime()) ? null : d.toLocaleString()
}

// FileBrowserModal владеет деревом и preview; FileBrowserProvider хранит selection между закрытиями
// модалки.
type ViewMode = 'all' | 'index' | 'head' | 'base'

export function FileBrowserModal({
  mode,
  selection,
  onToggleSelect,
  onRemoveSelect,
  onClose,
  onSubmit,
  attentionShortcut = null,
  initialView = EMPTY_BROWSER_VIEW,
  onViewChange,
}: FileBrowserModalProps): ReactElement {
  const [roots, setRoots] = useState<RootView[]>([])
  const [rootsError, setRootsError] = useState<string | null>(null)
  const [selectedRoot, setSelectedRoot] = useState<string | null>(initialView.root)
  const [activeFile, setActiveFile] = useState<ActiveFile | null>(initialView.file)
  const [activeTab, setActiveTab] = useState<PreviewTab>(initialView.tab)
  // Левая панель: файлы или список комментариев; режим файлов (All/Unstaged/…) при этом не теряется.
  const [panel, setPanel] = useState<'files' | 'comments'>('files')
  // Строка, к которой нужно прокрутить после загрузки файла (переход из списка комментариев).
  const [focusLine, setFocusLine] = useState<{ root: string; path: string; line: number } | null>(null)
  const { comments, send } = useComments()
  // Диалог отправки: only — ключ одного комментария или null для Send all; opener — кнопка, на которую вернётся фокус.
  const [sendDialog, setSendDialog] = useState<{ only: string | null; opener: HTMLElement } | null>(null)
  const sendDialogOpen = sendDialog !== null
  const fileComments = comments.filter(isSendableFileComment)
  const fileCommentCount = fileComments.length
  const commentsPerFile = new Map<string, number>()
  for (const entry of fileComments) commentsPerFile.set(fileKey(entry), (commentsPerFile.get(fileKey(entry)) ?? 0) + 1)
  const commentCountIn = (root: string) => (path: string) => commentsPerFile.get(fileGroupKey(root, path)) ?? 0

  // Left-panel width (px) + drag state. bodyRef anchors clientX to the body's
  // left edge; draggingRef gates the document-level move/up listeners.
  const [leftWidth, setLeftWidth] = useState<number>(loadLeftWidth)
  const bodyRef = useRef<HTMLDivElement | null>(null)
  const draggingRef = useRef(false)

  // Мобильный режим модалки: узкий вьюпорт → дерево становится слайд-овером над превью. treeOpen управляет
  // шторкой; на десктопе дерево всегда в колонке.
  const [isNarrowModal, setIsNarrowModal] = useState(false)
  const [treeOpen, setTreeOpen] = useState(false)
  useEffect(() => {
    const mq = window.matchMedia('(max-width: 720px)')
    const update = (): void => setIsNarrowModal(mq.matches)
    update()
    mq.addEventListener('change', update)
    return () => mq.removeEventListener('change', update)
  }, [])

  // Закрытое mobile-дерево inert, чтобы скрытые элементы не попадали в tab-order.
  useEffect(() => {
    const el = asideRef.current
    if (el === null) return
    el.inert = isNarrowModal && !treeOpen
  }, [isNarrowModal, treeOpen])

  // На мобиле открытие фокусирует первый элемент дерева, закрытие возвращает фокус на тумблер.
  const prevTreeOpen = useRef(false)
  useEffect(() => {
    if (!isNarrowModal) { prevTreeOpen.current = treeOpen; return }
    if (treeOpen && !prevTreeOpen.current) {
      const el = asideRef.current
      if (el !== null) visibleFocusables(el)[0]?.focus()
    } else if (!treeOpen && prevTreeOpen.current) {
      // Фокус возвращается на кнопку, которая открыла шторку: дерево или комментарии.
      ;(panel === 'comments' ? commentsToggleRef : treeToggleRef).current?.focus()
    }
    prevTreeOpen.current = treeOpen
  }, [treeOpen, isNarrowModal, panel])

  // Переключатель вида левой панели: 'all' — дерево + поиск, 'index'/'head'/'base' — плоский список
  // изменённых файлов ('base' — изменения ветки worktree от точки форка, только если корень их содержит).
  const [viewMode, setViewMode] = useState<ViewMode>('all')
  const [changesRevision, setChangesRevision] = useState(0)
  const [changes, setChanges] = useState<ChangeList | null>(null)
  const [changesLoading, setChangesLoading] = useState(false)
  const [changesError, setChangesError] = useState<string | null>(null)
  const changesGenRef = useRef(0)

  // The "vs base" view exists only for a root that physically contains a linked git worktree.
  const selectedRootHasWorktrees = roots.find((r) => r.id === selectedRoot)?.hasWorktrees ?? false
  // If the selected root loses the capability (root switch), don't leave the view stuck on 'base'.
  useEffect(() => {
    if (viewMode === 'base' && !selectedRootHasWorktrees) setViewMode('all')
  }, [viewMode, selectedRootHasWorktrees])

  const { content, contentLoading, contentError, diff, diffLoading, diffError, reloading, reloadError, reload: handleReload } = useFilePreview(activeFile, activeTab)

  // Прокрутка восстанавливается один раз после загрузки восстановленного файла; любой другой выбор
  // начинает с верха, поэтому pending сбрасывается в openFile/selectTab.
  const pendingScrollRef = useRef<number | null>(initialView.file !== null ? initialView.scrollTop : null)
  const scrollTopRef = useRef(initialView.scrollTop)
  const previewReady = activeTab === 'FILE' ? content !== null : diff !== null
  useLayoutEffect(() => {
    const pending = pendingScrollRef.current
    if (pending === null || !previewReady || paneRef.current === null) return
    paneRef.current.scrollTop = pending
    pendingScrollRef.current = null
  }, [previewReady, content, diff])

  const onViewChangeRef = useRef(onViewChange)
  onViewChangeRef.current = onViewChange
  function reportView(): void {
    onViewChangeRef.current?.({ root: selectedRoot, file: activeFile, tab: activeTab, scrollTop: scrollTopRef.current })
  }
  useEffect(reportView, [selectedRoot, activeFile, activeTab]) // eslint-disable-line react-hooks/exhaustive-deps

  // Пока превью грузится, браузер зажимает scrollTop до 0 — это не выбор пользователя.
  function onPaneScroll(e: React.UIEvent<HTMLDivElement>): void {
    if (!previewReady || pendingScrollRef.current !== null) return
    scrollTopRef.current = e.currentTarget.scrollTop
    reportView()
  }

  const [query, setQuery] = useState('')
  const [searchResult, setSearchResult] = useState<SearchResult | null>(null)
  const [searchLoading, setSearchLoading] = useState(false)
  const [searchError, setSearchError] = useState<string | null>(null)
  // Поколение поиска: инкрементируется на КАЖДЫЙ запуск эффекта.
  const searchGenRef = useRef(0)

  const modalRef = useRef<HTMLDivElement | null>(null)
  const closeBtnRef = useRef<HTMLButtonElement | null>(null)
  const asideRef = useRef<HTMLElement | null>(null)
  const treeToggleRef = useRef<HTMLButtonElement | null>(null)
  const commentsToggleRef = useRef<HTMLButtonElement | null>(null)
  const paneRef = useRef<HTMLDivElement | null>(null)
  useEffect(() => {
    let cancelled = false
    void getRoots()
      .then((r) => {
        if (cancelled) return
        setRoots(r)
        // Восстановленный корень не перетираем запоздавшим ответом; исчезнувший корень заменяем первым.
        setSelectedRoot((prev) => (prev !== null && r.some((root) => root.id === prev) ? prev : (r[0]?.id ?? null)))
      })
      .catch((e: unknown) => {
        if (cancelled) return
        setRootsError(e instanceof Error ? e.message : 'failed to load roots')
      })
    return () => {
      cancelled = true
    }
  }, [])

  // Поиск с debounce ~250мс и отменой устаревшего запроса. Пустой запрос — немедленно сбрасываем
  // результаты (дерево показывается снова), без сетевого вызова.
  useEffect(() => {
    // Bump generation even for empty queries or null roots so every prior request becomes stale.
    const generation = ++searchGenRef.current
    // При смене режима очищаем скрытый поиск и инвалидируем его запросы.
    if (selectedRoot === null || viewMode !== 'all') {
      setSearchResult(null)
      setSearchError(null)
      setSearchLoading(false)
      return
    }
    const trimmed = query.trim()
    if (trimmed === '') {
      setSearchResult(null)
      setSearchError(null)
      setSearchLoading(false)
      return
    }
    const controller = new AbortController()
    // Сразу очищаем старые результаты при смене root/query: иначе клик во время loading откроет файл из
    // прежнего root.
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
  }, [query, selectedRoot, viewMode])

  // Загрузка списка изменений для index/head.
  useEffect(() => {
    const generation = ++changesGenRef.current
    if (viewMode === 'all' || selectedRoot === null) {
      setChanges(null)
      setChangesError(null)
      setChangesLoading(false)
      return
    }
    const controller = new AbortController()
    setChanges(null)
    setChangesError(null)
    setChangesLoading(true)
    void getChanged(selectedRoot, viewMode, controller.signal)
      .then((r) => {
        if (generation !== changesGenRef.current) return
        setChangesLoading(false)
        setChanges(r)
      })
      .catch((e: unknown) => {
        if (controller.signal.aborted || generation !== changesGenRef.current) return
        setChangesLoading(false)
        setChangesError(
          e !== null && typeof e === 'object' && 'code' in e && (e as { code?: unknown }).code === 'diff_unavailable'
            ? 'Not a git repository'
            : `Failed to load changes: ${e instanceof Error ? e.message : 'read_failed'}`,
        )
      })
    return () => controller.abort()
  }, [viewMode, selectedRoot, changesRevision])

  // Стартовый фокус — на гарантированно ВИДИМУЮ кнопку Close, но ТОЛЬКО при монтировании. Теперь начальный
  // фокус отделён и не реагирует на смену состояния шторки.
  useEffect(() => {
    const modal = modalRef.current
    ;(closeBtnRef.current ?? (modal !== null ? visibleFocusables(modal)[0] : undefined))?.focus()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Esc закрывает, Tab крутит фокус по кругу внутри модалки.
  const isNarrowModalRef = useRef(isNarrowModal)
  isNarrowModalRef.current = isNarrowModal
  const treeOpenRef = useRef(treeOpen)
  treeOpenRef.current = treeOpen
  useEffect(() => {
    // Пока открыт диалог отправки, Escape и Tab обрабатывает он: иначе один Escape закрыл бы и Files.
    if (sendDialogOpen) return
    const modal = modalRef.current
    function onKeyDown(e: KeyboardEvent): void {
      if (e.key === 'Escape') {
        e.preventDefault()
        // Escape сначала закрывает mobile-дерево, затем оверлей Files.
        if (isNarrowModalRef.current && treeOpenRef.current) {
          setTreeOpen(false)
          return
        }
        onClose()
        return
      }
      if (e.key !== 'Tab' || modal === null) return
      const items = visibleFocusables(modal)
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
  }, [onClose, sendDialogOpen])

  // Persist the panel width so it survives close/reopen and reload.
  useEffect(() => {
    try {
      localStorage.setItem(LEFT_WIDTH_KEY, String(leftWidth))
    } catch {
      // localStorage unavailable — width just isn't persisted, no harm
    }
  }, [leftWidth])

  // Document-level drag listeners: attached once, gated by draggingRef so they only act while a drag is in
  // progress.
  useEffect(() => {
    function onMove(e: MouseEvent): void {
      if (!draggingRef.current || bodyRef.current === null) return
      const rect = bodyRef.current.getBoundingClientRect()
      const max = Math.max(LEFT_MIN, rect.width - RIGHT_MIN)
      setLeftWidth(Math.max(LEFT_MIN, Math.min(e.clientX - rect.left, max)))
    }
    function onUp(): void {
      if (!draggingRef.current) return
      draggingRef.current = false
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
    }
    document.addEventListener('mousemove', onMove)
    document.addEventListener('mouseup', onUp)
    return () => {
      document.removeEventListener('mousemove', onMove)
      document.removeEventListener('mouseup', onUp)
    }
  }, [])

  function onResizeStart(e: React.MouseEvent): void {
    e.preventDefault()
    draggingRef.current = true
    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none' // no text selection mid-drag
  }

  // Keyboard resize: Left/Right nudge the width by KEY_STEP. The dynamic max needs the body width; fall
  // back to a static upper bound when it isn't measurable (jsdom / before first layout).
  function onResizeKey(e: React.KeyboardEvent): void {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return
    e.preventDefault()
    // width 0 means "not laid out yet" (jsdom / before first paint) — fall back
    // to a static upper bound rather than clamping to the useless LEFT_MIN.
    const bw = bodyRef.current?.getBoundingClientRect().width ?? 0
    const max = bw > 0 ? Math.max(LEFT_MIN, bw - RIGHT_MIN) : 900
    const delta = e.key === 'ArrowRight' ? KEY_STEP : -KEY_STEP
    setLeftWidth((w) => Math.max(LEFT_MIN, Math.min(w + delta, max)))
  }

  function resetScroll(): void {
    pendingScrollRef.current = null
    scrollTopRef.current = 0
  }

  function selectTab(tab: PreviewTab): void {
    resetScroll()
    setActiveTab(tab)
  }

  function openFile(root: string, entry: TreeEntry): void {
    resetScroll()
    setFocusLine(null)
    setActiveFile({ root, entry })
    setActiveTab('FILE')
    // На мобиле выбор файла = дерево своё отработало → закрываем шторку, показываем превью.
    if (isNarrowModal) setTreeOpen(false)
  }

  // Переход из списка комментариев: тот же файл не перезагружаем, а только прокручиваем к строке.
  function openComment({ root, path, line }: CommentTarget): void {
    setSelectedRoot(root)
    if (activeFile?.root === root && activeFile.entry.path === path && activeTab === 'FILE') {
      resetScroll()
      setFocusLine({ root, path, line })
      if (isNarrowModal) setTreeOpen(false)
      return
    }
    openFile(root, { name: path.split('/').pop() ?? path, path, kind: 'file', selectable: true })
    setFocusLine({ root, path, line })
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
          <div className="file-browser-header-actions">
            {/* Тумблер дерева (Finding #3): только на узком вьюпорте — открывает
                слайд-овер со списком файлов поверх превью. */}
            <button
              ref={treeToggleRef}
              type="button"
              className="file-browser-tree-toggle"
              aria-label="Toggle file tree"
              aria-expanded={treeOpen && panel === 'files'}
              onClick={() => {
                if (treeOpen && panel === 'files') {
                  setTreeOpen(false)
                  return
                }
                setPanel('files')
                setTreeOpen(true)
              }}
            >
              <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true">
                <path d="M4 6h16M4 12h16M4 18h16" />
              </svg>
            </button>
            {/* На узком экране левая панель — шторка, поэтому вход в комментарии лежит в шапке. */}
            {isNarrowModal && (
              <button
                ref={commentsToggleRef}
                type="button"
                className="file-browser-comments-toggle"
                aria-expanded={treeOpen && panel === 'comments'}
                onClick={() => {
                  if (treeOpen && panel === 'comments') {
                    setTreeOpen(false)
                    return
                  }
                  setPanel('comments')
                  setTreeOpen(true)
                }}
              >
                Comments ({fileCommentCount})
              </button>
            )}
            {/* Шорткат к ждущему действию (Finding #4): не даём ожиданию
                потеряться за непрозрачным оверлеем — закрывает Files и открывает
                нужный attention-воркспейс. */}
            {attentionShortcut !== null && (
              <button
                type="button"
                className="file-browser-attention-shortcut"
                onClick={() => {
                  onClose()
                  attentionShortcut.onActivate()
                }}
              >
                <span className="attn-dot" aria-hidden="true" />
                {attentionShortcut.label}
              </button>
            )}
            <button ref={closeBtnRef} type="button" className="icon-btn" aria-label="Close" onClick={onClose}>
              ✕
            </button>
          </div>
        </header>

        <div className={`file-browser-body${isNarrowModal ? ' narrow' : ''}${treeOpen ? ' tree-open' : ''}`} ref={bodyRef}>
          {/* Скрим под шторкой дерева (только на мобиле при открытой шторке). */}
          <button
            type="button"
            className="file-browser-tree-scrim"
            aria-label="Close file tree"
            tabIndex={isNarrowModal && treeOpen ? 0 : -1}
            onClick={() => setTreeOpen(false)}
          />
          <aside ref={asideRef} className="file-browser-roots" style={isNarrowModal ? undefined : { flexBasis: leftWidth }}>
            {!isNarrowModal && (
              <div className="file-browser-viewswitch file-browser-panelswitch" role="group" aria-label="Left panel">
                <button
                  type="button"
                  aria-pressed={panel === 'files'}
                  className={`file-browser-viewbtn${panel === 'files' ? ' active' : ''}`}
                  onClick={() => setPanel('files')}
                >
                  Files
                </button>
                <button
                  type="button"
                  aria-pressed={panel === 'comments'}
                  className={`file-browser-viewbtn${panel === 'comments' ? ' active' : ''}`}
                  onClick={() => setPanel('comments')}
                >
                  Comments ({fileCommentCount})
                </button>
              </div>
            )}
            {panel === 'comments' && (
              <FileCommentsList rootLabel={rootLabel} onOpen={openComment} />
            )}
            <div hidden={panel === 'comments'} className="file-browser-files-panel">
            <div className="file-browser-toolbar">
              <div className="file-browser-viewswitch" role="group" aria-label="File panel view">
                <button
                  type="button"
                  aria-pressed={viewMode === 'all'}
                  className={`file-browser-viewbtn${viewMode === 'all' ? ' active' : ''}`}
                  onClick={() => setViewMode('all')}
                >
                  All
                </button>
                <button
                  type="button"
                  aria-pressed={viewMode === 'index'}
                  title="Working tree vs index; includes untracked files"
                  className={`file-browser-viewbtn${viewMode === 'index' ? ' active' : ''}`}
                  onClick={() => setViewMode('index')}
                >
                  Unstaged
                </button>
                <button
                  type="button"
                  aria-pressed={viewMode === 'head'}
                  title="Working tree vs last commit; includes untracked files"
                  className={`file-browser-viewbtn${viewMode === 'head' ? ' active' : ''}`}
                  onClick={() => setViewMode('head')}
                >
                  vs HEAD
                </button>
                {selectedRootHasWorktrees && (
                  <button
                    type="button"
                    aria-pressed={viewMode === 'base'}
                    title="Worktree branch vs its fork point; shows work already committed on the branch"
                    className={`file-browser-viewbtn${viewMode === 'base' ? ' active' : ''}`}
                    onClick={() => setViewMode('base')}
                  >
                    vs base
                  </button>
                )}
              </div>
              {viewMode !== 'all' && (
                <button
                  type="button"
                  className="file-browser-refresh icon-btn"
                  aria-label="Refresh changed files"
                  disabled={changesLoading}
                  onClick={() => setChangesRevision((n) => n + 1)}
                >
                  ↻
                </button>
              )}
            </div>
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
                {viewMode === 'all' && (
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
                        showCheckboxes={mode === 'picker'}
                        commentCount={commentCountIn(selectedRoot)}
                      />
                    ) : null}
                  </>
                )}

                {viewMode !== 'all' ? (
                  <ChangedFilesList
                    result={changes}
                    loading={changesLoading}
                    error={changesError}
                    onOpenFile={(entry) => openFile(selectedRoot, entry)}
                    onToggleSelect={(entry) => onToggleSelect(selectedRoot, entry)}
                    isSelected={(path) => selection.some((f) => f.root === selectedRoot && f.path === path)}
                    activePath={activeFile?.root === selectedRoot ? activeFile.entry.path : null}
                    showCheckboxes={mode === 'picker'}
                    commentCount={commentCountIn(selectedRoot)}
                  />
                ) : null}

                {/* Дерево остаётся смонтированным вне 'all' и во время поиска
                    (hidden, не размонтирование) — переключение обратно
                    возвращает раскрытые каталоги без повторной загрузки. */}
                <div className="file-browser-tree-wrap" hidden={viewMode !== 'all' || query.trim() !== ''}>
                  <FileTree
                    root={selectedRoot}
                    onOpenFile={(entry) => openFile(selectedRoot, entry)}
                    onToggleSelect={(entry) => onToggleSelect(selectedRoot, entry)}
                    isSelected={(path) => selection.some((f) => f.root === selectedRoot && f.path === path)}
                    activePath={activeFile?.root === selectedRoot ? activeFile.entry.path : null}
                    showCheckboxes={mode === 'picker'}
                    commentCount={commentCountIn(selectedRoot)}
                  />
                </div>
              </>
            )}
            </div>
          </aside>

          <div
            className="file-browser-resizer"
            role="separator"
            aria-orientation="vertical"
            aria-label="Resize file panel"
            tabIndex={0}
            onMouseDown={onResizeStart}
            onKeyDown={onResizeKey}
          />

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
                onClick={() => selectTab('FILE')}
              >
                FILE
              </button>
              <button
                type="button"
                role="tab"
                aria-selected={activeTab === 'DIFF'}
                className={`file-browser-tab${activeTab === 'DIFF' ? ' active' : ''}`}
                onClick={() => selectTab('DIFF')}
              >
                DIFF
              </button>
            </div>
            <div className="file-browser-pane" ref={paneRef} onScroll={onPaneScroll}>
              {activeTab === 'FILE' ? (
                <FileViewer
                  content={content}
                  loading={contentLoading}
                  error={contentError}
                  root={activeFile?.root}
                  focus={focusLine}
                  onFocusHandled={() => setFocusLine(null)}
                  onSend={(key, opener) => setSendDialog({ only: key, opener })}
                />
              ) : (
                <DiffViewer diff={diff} loading={diffLoading} error={diffError} />
              )}
            </div>
          </section>
        </div>

        {/* Футер с выбором (чипы + «Insert/Copy references») — только в режиме
            attach (picker). В режиме просмотра (открыт из шапки) файлы не
            выбираются: чекбоксов нет, поэтому и футер выбора не нужен —
            модалка становится чистым просмотрщиком (дерево + превью + diff),
            закрывается кнопкой Close в шапке / Esc. */}
        {mode === 'browse' && (
          <footer className="file-browser-footer file-browser-send-footer">
            <FileCommentSendStatus />
            <button
              type="button"
              className="btn btn-approve"
              disabled={(fileCommentCount === 0 && send.operation === null) || send.pending}
              onClick={(event) => setSendDialog({ only: null, opener: event.currentTarget })}
            >
              Send all ({fileCommentCount})
            </button>
          </footer>
        )}
        {mode === 'picker' && (
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
        )}
      </div>
      {sendDialog !== null && (
        <FileCommentSendDialog
          only={sendDialog.only}
          onClose={() => setSendDialog(null)}
          onOpenSource={(target) => {
            setSendDialog(null)
            openComment(target)
          }}
          returnFocus={() => sendReturnTarget(sendDialog.opener, modalRef.current, closeBtnRef.current)}
        />
      )}
    </div>
  )
}
