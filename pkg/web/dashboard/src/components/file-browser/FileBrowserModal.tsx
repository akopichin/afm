import { useEffect, useRef, useState, type ReactElement } from 'react'
import {
  getChanged,
  getRoots,
  getSearch,
  type ChangeList,
  type RootView,
  type SearchResult,
  type TreeEntry,
} from '../../api/files-client'
import { addNote, listNotes, pauseFlow, type AddNoteRequest } from '../../api/run-client'
import type { ReviewNote } from '../../types'
import { ChangedFilesList } from './ChangedFilesList'
import { DiffViewer } from './DiffViewer'
import { FileSearchResults } from './FileSearchResults'
import { FileTree } from './FileTree'
import { FileViewer } from './FileViewer'
import { useFilePreview, type ActiveFile, type PreviewTab } from './use-file-preview'

export type SelectedFile = { root: string; path: string; displayPath: string; reference: string }

// sameLine matches the backend's note-dedup key semantics: two line pointers
// denote the same note iff both are file-level (null) or both the same number.
// Strict equality already covers all three cases (null/null, null/number,
// number/number).
function sameLine(a: number | null, b: number | null): boolean {
  return a === b
}

// Совпадает с use-status.ts's FlowStatus['flowPauseState'] — см. её же
// комментарий в FileViewer.tsx/ReviewBanner.tsx для семантики. Продублирован
// намеренно тем же приёмом: эта клетка не должна тянуть весь модуль use-status
// ради одного литерала.
type FlowPauseState = 'none' | 'paused' | 'resuming'

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
  // Шорткат к ждущему действию (Finding #4 второго раунда): когда во время
  // открытого оверлея появляется/висит ожидание, в шапке показывается кнопка
  // «Question waiting»/«Approval waiting», закрывающая Files и открывающая его.
  // null/undefined — ожидания нет, кнопка не рендерится.
  attentionShortcut?: { label: string; onActivate: () => void } | null
  // Флоу-wide review-pause состояние (см. use-status.ts) — проброшено сюда из
  // App.tsx через FileBrowserProvider, чтобы FileViewer мог показывать
  // кликабельные строки/маркер комментария только пока флоу реально на паузе
  // (см. её же гейт canAnnotate). По умолчанию 'none' — те же вызывающие
  // (тесты), что не передают этот проп, получают прежнее поведение "аннотации
  // выключены".
  flowPauseState?: FlowPauseState
}

const FOCUSABLE_SELECTOR = 'button:not([disabled]), [href], input:not([disabled]), select, textarea, [tabindex]:not([tabindex="-1"])'

// Видимо-фокусируемые элементы модалки: FOCUSABLE_SELECTOR ловит и элементы,
// скрытые CSS-ом (напр. mobile-only «Toggle file tree» с display:none на
// desktop) — реальный .focus() по ним не срабатывает, и фокус остаётся снаружи
// (Finding #3, раунд 3). checkVisibility() (Chrome 105+) отсеивает display:none/
// visibility:hidden; в jsdom его нет — там оставляем всё (layout не считается).
function visibleFocusables(modal: HTMLElement): HTMLElement[] {
  const all = Array.from(modal.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR))
  return all.filter((el) => (typeof el.checkVisibility === 'function' ? el.checkVisibility() : true))
}

// Resizable left panel: the file list/tree column can be dragged wider/narrower
// via the divider between it and the preview pane. LEFT_MIN keeps it usable,
// RIGHT_MIN reserves room for the preview so the drag can't collapse it, and
// KEY_STEP is the keyboard (Arrow) increment. The width persists across opens.
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
type ViewMode = 'all' | 'index' | 'head'

export function FileBrowserModal({
  mode,
  selection,
  onToggleSelect,
  onRemoveSelect,
  onClose,
  onSubmit,
  attentionShortcut = null,
  flowPauseState = 'none',
}: FileBrowserModalProps): ReactElement {
  const [roots, setRoots] = useState<RootView[]>([])
  const [rootsError, setRootsError] = useState<string | null>(null)
  const [selectedRoot, setSelectedRoot] = useState<string | null>(null)
  const [activeFile, setActiveFile] = useState<ActiveFile | null>(null)
  const [activeTab, setActiveTab] = useState<PreviewTab>('FILE')

  // Left-panel width (px) + drag state. bodyRef anchors clientX to the body's
  // left edge; draggingRef gates the document-level move/up listeners.
  const [leftWidth, setLeftWidth] = useState<number>(loadLeftWidth)
  const bodyRef = useRef<HTMLDivElement | null>(null)
  const draggingRef = useRef(false)

  // Мобильный режим модалки (Finding #3 второго раунда): узкий вьюпорт → дерево
  // становится слайд-овером над превью (иначе на 390px превью сжато почти в
  // ноль). treeOpen управляет шторкой; на десктопе дерево всегда в колонке.
  const [isNarrowModal, setIsNarrowModal] = useState(false)
  const [treeOpen, setTreeOpen] = useState(false)
  useEffect(() => {
    const mq = window.matchMedia('(max-width: 720px)')
    const update = (): void => setIsNarrowModal(mq.matches)
    update()
    mq.addEventListener('change', update)
    return () => mq.removeEventListener('change', update)
  }, [])

  // Finding #4: закрытое mobile-дерево — inert (его root-кнопки/поиск/treeitem'ы
  // не должны оставаться в tab-order за экраном). На desktop / при открытой
  // шторке — интерактивно.
  useEffect(() => {
    const el = asideRef.current
    if (el === null) return
    el.inert = isNarrowModal && !treeOpen
  }, [isNarrowModal, treeOpen])

  // Управление фокусом шторки дерева (Finding #4): открытие → фокус на первый
  // фокусируемый элемент дерева; закрытие → возврат на тумблер. Только на мобиле.
  const prevTreeOpen = useRef(false)
  useEffect(() => {
    if (!isNarrowModal) { prevTreeOpen.current = treeOpen; return }
    if (treeOpen && !prevTreeOpen.current) {
      const el = asideRef.current
      if (el !== null) visibleFocusables(el)[0]?.focus()
    } else if (!treeOpen && prevTreeOpen.current) {
      treeToggleRef.current?.focus()
    }
    prevTreeOpen.current = treeOpen
  }, [treeOpen, isNarrowModal])

  // Переключатель вида левой панели: 'all' — дерево + поиск (как раньше),
  // 'index'/'head' — плоский список изменённых файлов (git-статус относительно
  // индекса/HEAD). changesRevision — ручной инкремент кнопкой Refresh, не
  // связанный ни с чем ещё — тот же приём, что нигде в файле не был нужен
  // раньше, потому что раньше нечего было "перезапрашивать по кнопке".
  const [viewMode, setViewMode] = useState<ViewMode>('all')
  const [changesRevision, setChangesRevision] = useState(0)
  const [changes, setChanges] = useState<ChangeList | null>(null)
  const [changesLoading, setChangesLoading] = useState(false)
  const [changesError, setChangesError] = useState<string | null>(null)
  const changesGenRef = useRef(0)

  const { content, contentLoading, contentError, diff, diffLoading, diffError, reloading, reloadError, reload: handleReload } = useFilePreview(activeFile, activeTab)

  // Optimistic-concurrency rev для FileViewer's addNote (см. её проп
  // expectedRev): flow-wide, а не per-file — тот же rev, что ReviewNotesModal
  // (Task 23) читает/пишет через listNotes/addNote/updateNote/deleteNote.
  // Загружается один раз при открытии модалки как базовая точка отсчёта;
  // best-effort (нет активного review-раунда/бэкенд без поддержки заметок —
  // остаётся 0, тот же дефолт, с которым стартует свежий стор на бэкенде).
  // Каждый успешный addNote корректирует её значением из ответа сервера, без
  // отдельного повторного listNotes() на каждую заметку.
  const [notesRev, setNotesRev] = useState(0)
  // The authoritative note list for the round, kept so FileViewer can rehydrate
  // a reopened file's saved markers/prefill (review-finding P2 #5). Loaded once
  // on open and kept in sync by every successful addNote below.
  const [notes, setNotes] = useState<ReviewNote[]>([])
  useEffect(() => {
    let cancelled = false
    void listNotes()
      .then((res) => {
        if (!cancelled) {
          setNotesRev(res.rev)
          setNotes(res.notes)
        }
      })
      .catch(() => {
        // Нет активного review-раунда либо бэкенд ещё не отдаёт эту ручку —
        // не критично, FileViewer просто не сможет ставить заметки, пока
        // flowPauseState не 'paused' (см. её canAnnotate).
      })
    return () => {
      cancelled = true
    }
  }, [])

  async function handleAddNote(body: AddNoteRequest): Promise<{ note: ReviewNote; rev: number }> {
    const result = await addNote(body)
    setNotesRev(result.rev)
    // Keep the authoritative list in sync so FileViewer's saved-note hydration
    // survives a file switch: replace a note with the same root/path/line
    // (the backend dedups on that key), otherwise append.
    setNotes((prev) => {
      const i = prev.findIndex(
        (n) => n.root === result.note.root && n.path === result.note.path && sameLine(n.line, result.note.line),
      )
      if (i === -1) return [...prev, result.note]
      const next = prev.slice()
      next[i] = result.note
      return next
    })
    return result
  }

  const [query, setQuery] = useState('')
  const [searchResult, setSearchResult] = useState<SearchResult | null>(null)
  const [searchLoading, setSearchLoading] = useState(false)
  const [searchError, setSearchError] = useState<string | null>(null)
  // Поколение поиска: инкрементируется на КАЖДЫЙ запуск эффекта (смена запроса
  // или root'а). Поздний ответ устаревшего запроса/root'а сверяется с текущим
  // поколением и отбрасывается — та же техника, что rootGenerationRef в
  // FileTree и useFilePreview.
  const searchGenRef = useRef(0)

  const modalRef = useRef<HTMLDivElement | null>(null)
  const closeBtnRef = useRef<HTMLButtonElement | null>(null)
  const asideRef = useRef<HTMLElement | null>(null)
  const treeToggleRef = useRef<HTMLButtonElement | null>(null)
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

  // Поиск с debounce ~250мс и отменой устаревшего запроса. Пустой запрос —
  // немедленно сбрасываем результаты (дерево показывается снова), без сетевого
  // вызова. Смена root'а при непустом запросе повторяет поиск в новом root'е
  // (эффект зависит и от selectedRoot).
  useEffect(() => {
    // Bump the generation on EVERY run — including the empty-query and
    // null-root branches — so clearing the query (or switching root) invalidates
    // any request still in flight from a prior non-empty run: its late
    // .then/.catch see a stale generation and bail, even if the aborted fetch
    // still resolves.
    const generation = ++searchGenRef.current
    // Вне 'all' поисковая панель скрыта (см. рендер ниже) — не даём скрытому
    // in-flight запросу дописать searchResult поверх невидимой (но по-прежнему
    // смонтированной ветки) панели изменений: как только режим переключился,
    // результат/ошибка/loading этого эффекта немедленно очищаются.
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
  }, [query, selectedRoot, viewMode])

  // Загрузка списка изменений для index/head. Как и поиск: generation растёт на
  // КАЖДОМ запуске (включая ветки all/null-root), поздний ответ устаревшего
  // режима/root'а отбрасывается по generation, старый список чистится сразу.
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

  // Стартовый фокус — на гарантированно ВИДИМУЮ кнопку Close (Finding #3), но
  // ТОЛЬКО при монтировании (deps []). Finding #1 раунда 4: раньше этот initial
  // focus жил в том же эффекте, что и keydown-ловушка (с зависимостью от
  // treeOpen), поэтому при открытии/закрытии mobile-дерева эффект перезапускался
  // и переносил фокус обратно на Close, перебивая фокус-эффект шторки. Теперь
  // начальный фокус отделён и не реагирует на смену состояния шторки.
  useEffect(() => {
    const modal = modalRef.current
    ;(closeBtnRef.current ?? (modal !== null ? visibleFocusables(modal)[0] : undefined))?.focus()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Esc закрывает, Tab крутит фокус по кругу внутри модалки. Актуальные
  // isNarrowModal/treeOpen читаем через ref, чтобы слушатель не переподписывался
  // на их смену (и не тянул за собой повторный initial focus, см. выше).
  const isNarrowModalRef = useRef(isNarrowModal)
  isNarrowModalRef.current = isNarrowModal
  const treeOpenRef = useRef(treeOpen)
  treeOpenRef.current = treeOpen
  useEffect(() => {
    const modal = modalRef.current
    function onKeyDown(e: KeyboardEvent): void {
      if (e.key === 'Escape') {
        e.preventDefault()
        // Finding #4 (раунд 3): открытый mobile-слайдовер дерева Escape закрывает
        // СНАЧАЛА его, а не весь оверлей Files.
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
  }, [onClose])

  // Persist the panel width so it survives close/reopen and reload.
  useEffect(() => {
    try {
      localStorage.setItem(LEFT_WIDTH_KEY, String(leftWidth))
    } catch {
      // localStorage unavailable — width just isn't persisted, no harm
    }
  }, [leftWidth])

  // Document-level drag listeners: attached once, gated by draggingRef so they
  // only act while a drag is in progress. Anchoring to bodyRef's left edge keeps
  // the math correct regardless of where the centered modal sits. The right pane
  // is kept at least RIGHT_MIN wide so a drag can't collapse the preview.
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

  // Keyboard resize (the divider is a focusable separator): Left/Right nudge the
  // width by KEY_STEP. The dynamic max needs the body width; fall back to a
  // static upper bound when it isn't measurable (jsdom / before first layout).
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

  function openFile(root: string, entry: TreeEntry): void {
    setActiveFile({ root, entry })
    setActiveTab('FILE')
    // На мобиле выбор файла = дерево своё отработало → закрываем шторку, показываем превью.
    if (isNarrowModal) setTreeOpen(false)
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
              aria-expanded={treeOpen}
              onClick={() => setTreeOpen((v) => !v)}
            >
              <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true">
                <path d="M4 6h16M4 12h16M4 18h16" />
              </svg>
            </button>
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
                  />
                </div>
              </>
            )}
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
                <FileViewer
                  content={content}
                  loading={contentLoading}
                  error={contentError}
                  root={activeFile?.root}
                  flowPauseState={flowPauseState}
                  pauseFlow={pauseFlow}
                  addNote={handleAddNote}
                  expectedRev={notesRev}
                  savedNotes={notes}
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
    </div>
  )
}
