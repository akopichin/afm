import { useEffect, useRef, useState, type ReactElement, type ReactNode } from 'react'
import { getTree, type TreeEntry } from '../../api/files-client'

type FileTreeProps = {
  root: string
  // Клик по строке файла — открыть предпросмотр (FILE-таб модалки).
  onOpenFile: (entry: TreeEntry) => void
  // Клик по чекбоксу — переключить выбор (родитель сам решает, добавлять
  // или убирать, и сам зовёт getReference перед добавлением — см. бриф
  // Task 13: "checkbox first calls getReference... THEN adds/removes").
  onToggleSelect: (entry: TreeEntry) => void
  isSelected: (path: string) => boolean
  activePath: string | null
}

// Состояние одной раскрытой директории: то, что реально пришло с бэкенда —
// страницы конкатенируются по мере "Load more", nextCursor двигает то, что
// запросить следующим.
type DirState = {
  entries: TreeEntry[]
  nextCursor?: string
  loading: boolean
  error: string | null
}

// Ленивое дерево файлов проекта: верхний уровень (path — '.') грузится сразу
// при монтировании/смене root; поддиректории — только по клику на раскрытие,
// и только один раз (повторный клик просто схлопывает/разворачивает уже
// загруженное). Клавиатура: ArrowUp/ArrowDown двигают фокус по видимым
// строкам (roving tabIndex), Enter открывает файл или раскрывает/схлопывает
// директорию — символьные ссылки (kind: 'symlink') всегда лист, без чекбокса
// и без предпросмотра (workspace.FS их read всё равно отклоняет).
export function FileTree({ root, onOpenFile, onToggleSelect, isSelected, activePath }: FileTreeProps): ReactElement {
  const [dirs, setDirs] = useState<Record<string, DirState>>({})
  const [expanded, setExpanded] = useState<Set<string>>(new Set(['.']))
  const [focusedPath, setFocusedPath] = useState<string | null>(null)
  const rowRefs = useRef<Map<string, HTMLDivElement>>(new Map())

  // Номер "поколения" root'а — инкрементируется СИНХРОННО при смене root
  // (в эффекте ниже, до первого await), а не читается из замыкания над
  // проп root: loadDir захватывает текущее значение ДО своего await, и
  // сравнивает с ним же после — так поздний ответ устаревшего root'а
  // отбрасывается независимо от того, в каком порядке резолвятся промисы
  // (старый может ответить и раньше, и позже нового). Сравнение по числу,
  // а не по строке root — корректно и при переключении туда-обратно
  // (A→B→A): второй заход на A получает новый номер и не совпадёт со
  // старым, ещё летящим запросом первого захода.
  const rootGenerationRef = useRef(0)

  // Смена root — новое дерево с нуля: старое состояние (пути другого root'а)
  // не имеет смысла и не должно мелькать, пока грузится новое.
  useEffect(() => {
    rootGenerationRef.current += 1
    setDirs({})
    setExpanded(new Set(['.']))
    setFocusedPath(null)
    void loadDir(root, '.')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [root])

  async function loadDir(forRoot: string, path: string, cursor?: string): Promise<void> {
    const generation = rootGenerationRef.current
    setDirs((prev) => ({
      ...prev,
      [path]: { entries: prev[path]?.entries ?? [], nextCursor: prev[path]?.nextCursor, loading: true, error: null },
    }))
    try {
      const page = await getTree(forRoot, path, cursor)
      if (generation !== rootGenerationRef.current) return // root сменился, пока запрос летел — не применяем устаревший ответ
      setDirs((prev) => ({
        ...prev,
        [path]: {
          entries: cursor === undefined ? page.entries : [...(prev[path]?.entries ?? []), ...page.entries],
          nextCursor: page.nextCursor,
          loading: false,
          error: null,
        },
      }))
    } catch (e) {
      if (generation !== rootGenerationRef.current) return
      setDirs((prev) => ({
        ...prev,
        // nextCursor СОХРАНЯЕТСЯ (не сбрасывается в undefined): при ошибке
        // "Load more" это курсор той же страницы, которую только что не
        // удалось загрузить — Retry должен продолжить именно с него, а не
        // спрятать кнопку и оставить остаток каталога недостижимым. Для
        // самой первой (не пагинационной) загрузки курсора и так не было.
        [path]: {
          entries: prev[path]?.entries ?? [],
          nextCursor: prev[path]?.nextCursor,
          loading: false,
          error: e instanceof Error ? e.message : 'failed to load',
        },
      }))
    }
  }

  function toggleDir(entry: TreeEntry): void {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(entry.path)) {
        next.delete(entry.path)
      } else {
        next.add(entry.path)
      }
      return next
    })
    if (dirs[entry.path] === undefined) void loadDir(root, entry.path)
  }

  function activate(entry: TreeEntry): void {
    if (entry.kind === 'directory') {
      toggleDir(entry)
    } else if (entry.kind === 'file' && entry.selectable) {
      onOpenFile(entry)
    }
    // symlink, а также не-selectable файл (спецфайл вроде FIFO/сокета/устройства,
    // который workspace.FS всё равно отклонит на чтение): намеренно ничего не
    // делаем — не раскрывается, не открывается.
  }

  const visible = flattenVisible(dirs, expanded)

  // currentPath приходит из entry строки, которая ловит событие (замыкание
  // renderEntry), а не из state focusedPath — так навигация корректна, даже
  // если .focus() вызван программно (в т.ч. в тестах) без промежуточного
  // рендера: onFocus и keydown могут случиться в одном синхронном тике,
  // раньше, чем React применит setFocusedPath из предыдущего события.
  function moveFocus(currentPath: string, delta: 1 | -1): void {
    const index = visible.findIndex((v) => v.entry.path === currentPath)
    if (index === -1) return
    const nextItem = visible[index + delta]
    if (nextItem === undefined) return
    setFocusedPath(nextItem.entry.path)
    rowRefs.current.get(nextItem.entry.path)?.focus()
  }

  function handleKeyDown(entry: TreeEntry, e: React.KeyboardEvent): void {
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        moveFocus(entry.path, 1)
        break
      case 'ArrowUp':
        e.preventDefault()
        moveFocus(entry.path, -1)
        break
      case 'ArrowRight':
        if (entry.kind === 'directory' && !expanded.has(entry.path)) {
          e.preventDefault()
          toggleDir(entry)
        }
        break
      case 'ArrowLeft':
        if (entry.kind === 'directory' && expanded.has(entry.path)) {
          e.preventDefault()
          toggleDir(entry)
        }
        break
      case 'Enter':
        e.preventDefault()
        activate(entry)
        break
      default:
        break
    }
  }

  function renderEntry(entry: TreeEntry, depth: number): ReactNode {
    const dirState = entry.kind === 'directory' ? dirs[entry.path] : undefined
    const isExpanded = entry.kind === 'directory' && expanded.has(entry.path)
    const focused = focusedPath === entry.path || (focusedPath === null && visible[0]?.entry.path === entry.path)

    return (
      <div key={entry.path} className="file-tree-branch">
        <div
          role="treeitem"
          aria-expanded={entry.kind === 'directory' ? isExpanded : undefined}
          aria-selected={activePath === entry.path}
          data-kind={entry.kind}
          className={`file-tree-row${activePath === entry.path ? ' active' : ''}`}
          style={{ paddingLeft: `${depth * 16 + 6}px` }}
          tabIndex={focused ? 0 : -1}
          ref={(el) => {
            if (el) rowRefs.current.set(entry.path, el)
            else rowRefs.current.delete(entry.path)
          }}
          onFocus={() => setFocusedPath(entry.path)}
          onKeyDown={(e) => handleKeyDown(entry, e)}
          onClick={() => activate(entry)}
        >
          <span className="file-tree-toggle" aria-hidden="true">
            {entry.kind === 'directory' ? (isExpanded ? '▾' : '▸') : ''}
          </span>
          {entry.kind === 'file' && entry.selectable && (
            <input
              type="checkbox"
              aria-label={`Select ${entry.name}`}
              checked={isSelected(entry.path)}
              onClick={(e) => e.stopPropagation()}
              onChange={() => onToggleSelect(entry)}
            />
          )}
          <span className={`file-tree-icon file-tree-icon-${entry.kind}`} aria-hidden="true">
            {entry.kind === 'directory' ? '📁' : entry.kind === 'symlink' ? '🔗' : '📄'}
          </span>
          <span className="file-tree-name">{entry.name}</span>
        </div>

        {entry.kind === 'directory' && isExpanded && (
          <div className="file-tree-children">
            {dirState?.loading && dirState.entries.length === 0 && <div className="file-tree-hint">Loading…</div>}
            {dirState?.error !== null && dirState?.error !== undefined && <div className="file-tree-hint file-tree-error">{dirState.error}</div>}
            {dirState?.entries.map((child) => renderEntry(child, depth + 1))}
            {renderLoadMore(entry.path, dirState, (depth + 1) * 16 + 6)}
          </div>
        )}
      </div>
    )
  }

  // Общая кнопка пагинации для каталога — используется и на верхнем уровне,
  // и для любой раскрытой поддиректории (renderEntry выше). При ошибке
  // предыдущей страницы (dirState.error) курсор уже сохранён вызывающей
  // стороной (loadDir) — здесь просто меняем подпись на "Retry…", чтобы было
  // явно видно, что это повторная попытка, а не следующая страница. onClick
  // не меняется: тот же loadDir с тем же nextCursor.
  function renderLoadMore(path: string, dirState: DirState | undefined, paddingPx?: number): ReactNode {
    if (dirState?.nextCursor === undefined) return null
    const failed = dirState.error !== null && dirState.error !== undefined
    return (
      <button
        type="button"
        className="file-tree-load-more"
        style={paddingPx === undefined ? undefined : { paddingLeft: `${paddingPx}px` }}
        disabled={dirState.loading}
        onClick={() => void loadDir(root, path, dirState.nextCursor)}
      >
        {failed ? 'Retry…' : 'Load more…'}
      </button>
    )
  }

  const top = dirs['.']

  return (
    <div role="tree" aria-label="Project files" className="file-tree">
      {top?.loading && top.entries.length === 0 && <div className="file-tree-hint">Loading…</div>}
      {top?.error !== null && top?.error !== undefined && <div className="file-tree-hint file-tree-error">{top.error}</div>}
      {top?.entries.map((entry) => renderEntry(entry, 0))}
      {renderLoadMore('.', top)}
    </div>
  )
}

type VisibleItem = { entry: TreeEntry; depth: number }

// Плоский список видимых строк в порядке отображения — нужен только для
// клавиатурной навигации (ArrowUp/ArrowDown должны знать "следующая видимая
// строка", а не "следующий элемент массива" — раскрытые поддиректории
// вклиниваются между родителем и его следующим соседом).
function flattenVisible(dirs: Record<string, DirState>, expanded: Set<string>, path = '.', depth = 0): VisibleItem[] {
  const dir = dirs[path]
  if (dir === undefined) return []
  const items: VisibleItem[] = []
  for (const entry of dir.entries) {
    items.push({ entry, depth })
    if (entry.kind === 'directory' && expanded.has(entry.path)) {
      items.push(...flattenVisible(dirs, expanded, entry.path, depth + 1))
    }
  }
  return items
}
