import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactElement, type ReactNode } from 'react'
import { getReference, type TreeEntry } from '../../api/files-client'
import { copyToClipboard } from '../../lib/clipboard'
import { EMPTY_BROWSER_VIEW, FileBrowserModal, type BrowserView, type SelectedFile } from './FileBrowserModal'

type FileBrowserMode = 'browse' | 'picker'

type FileBrowserContextValue = {
  // Открывает модалку в свободном режиме просмотра проекта (кнопка снизу —
  // "Copy references", кладёт выбранное в буфер обмена).
  openBrowser: () => void
  // Открывает модалку в режиме выбора файлов для вставки: onInsert получает собранные референсы выбранных
  // файлов и вызывается по клику на "Insert references" (который тут же закрывает модалку).
  pickFiles: (onInsert: (references: string[]) => void) => void
  // capabilities.file_browser с бэкенда. false — единственный источник правды "показывать ли кнопку
  // 'Attach project file'/'Open project files' и можно ли вообще открыть модалку".
  enabled: boolean
}

const FileBrowserContext = createContext<FileBrowserContextValue | null>(null)

export function useFileBrowser(): FileBrowserContextValue {
  const ctx = useContext(FileBrowserContext)
  if (ctx === null) throw new Error('useFileBrowser must be used within a FileBrowserProvider')
  return ctx
}

// Лёгкий вариант useFileBrowser для мест, которым нужен только флаг enabled ДО того, как решать,
// монтировать ли что-то, зовущее useFileBrowser.
export function useFileBrowserEnabled(): boolean {
  const ctx = useContext(FileBrowserContext)
  return ctx?.enabled ?? false
}

// useFileBrowserOptional — как useFileBrowser, но НЕ бросает вне провайдера.
export function useFileBrowserOptional(): FileBrowserContextValue | null {
  return useContext(FileBrowserContext)
}

type FileBrowserProviderProps = {
  children: ReactNode
  // Новый flowName/startedAt сбрасывает selection и закрывает модалку предыдущего рана.
  flowName: string
  startedAt: string
  // capabilities.file_browser, проброшенный из App.tsx.
  enabled?: boolean
  // onOpenChange сообщает App о модалке, чтобы подавить auto-attention за оверлеем.
  onOpenChange?: (open: boolean) => void
  attentionShortcut?: { label: string; onActivate: () => void } | null
}

// JSON.stringify кодирует пару root/path без коллизий разделителей и контрольных байтов в исходнике.
function selectionKey(root: string, path: string): string {
  return JSON.stringify([root, path])
}

// FileBrowserProvider хранит selection и режим между закрытиями; модалка при закрытии размонтируется.
export function FileBrowserProvider({ children, flowName, startedAt, enabled = true, onOpenChange, attentionShortcut = null }: FileBrowserProviderProps): ReactElement {
  const [open, setOpen] = useState(false)
  const [mode, setMode] = useState<FileBrowserMode>('browse')
  const [selection, setSelection] = useState<Map<string, SelectedFile>>(new Map())
  const [selectionError, setSelectionError] = useState<string | null>(null)
  const onInsertRef = useRef<((references: string[]) => void) | null>(null)
  // Открытый файл/корень/вкладка/прокрутка переживают закрытие Files; ref, потому что перерисовка не нужна.
  const viewRef = useRef<BrowserView>(EMPTY_BROWSER_VIEW)

  // Finding 7: генерация "эпохи" selection'а — счётчик, а не boolean, потому что за время жизни провайдера
  // таких эпох может смениться сколько угодно.
  const generationRef = useRef(0)
  const pendingRef = useRef<Set<string>>(new Set())

  // Элемент, из которого открыли оверлей. Ловим на открытии, восстанавливаем на закрытии/сабмите.
  const openerRef = useRef<HTMLElement | null>(null)
  function captureOpener(): void {
    openerRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
  }
  function restoreOpener(): void {
    const el = openerRef.current
    openerRef.current = null
    // Кнопка ещё в DOM и видима? Вернём фокус. Если её больше нет (напр. сменился
    // прогон и шапка перерисовалась) — тихо ничего не делаем.
    if (el !== null && el.isConnected) el.focus()
  }

  function bumpGeneration(): void {
    generationRef.current += 1
    pendingRef.current.clear()
  }

  useEffect(() => {
    bumpGeneration()
    setOpen(false)
    setSelection(new Map())
    setSelectionError(null)
    onInsertRef.current = null
    viewRef.current = EMPTY_BROWSER_VIEW
    restoreOpener() // no-op на маунте (opener пуст) и когда кнопка уже исчезла
  }, [flowName, startedAt])

  // Сообщаем App об открытии/закрытии через ref, чтобы смена callback не перезапускала эффект.
  const onOpenChangeRef = useRef(onOpenChange)
  onOpenChangeRef.current = onOpenChange
  useEffect(() => {
    onOpenChangeRef.current?.(open)
  }, [open])

  const openBrowser = useCallback(() => {
    if (!enabled) return
    captureOpener()
    setMode('browse')
    onInsertRef.current = null
    setOpen(true)
  }, [enabled])

  const pickFiles = useCallback(
    (onInsert: (references: string[]) => void) => {
      if (!enabled) return
      captureOpener()
      setMode('picker')
      onInsertRef.current = onInsert
      setOpen(true)
    },
    [enabled],
  )

  const close = useCallback(() => {
    // Пикер закрывается без сабмита — если это была picker-сессия, её эпоха тоже кончилась: следующий
    // pickFiles не должен унаследовать ещё не разрешившийся getReference из отменённой сессии.
    if (mode === 'picker') bumpGeneration()
    setOpen(false)
    restoreOpener()
  }, [mode])

  const toggleSelect = useCallback(
    (root: string, entry: TreeEntry) => {
      const key = selectionKey(root, entry.path)
      if (selection.has(key)) {
        setSelection((prev) => {
          const next = new Map(prev)
          next.delete(key)
          return next
        })
        return
      }
      // Дедуп повторного клика по тому же файлу, пока первый getReference ещё
      // не разрешился — иначе двойной клик плодит два параллельных запроса.
      if (pendingRef.current.has(key)) return
      pendingRef.current.add(key)
      setSelectionError(null)
      const generation = generationRef.current
      void getReference(root, entry.path)
        .then((ref) => {
          pendingRef.current.delete(key)
          if (generation !== generationRef.current) return
          setSelection((prev) => {
            const next = new Map(prev)
            next.set(key, { root, path: entry.path, displayPath: ref.displayPath || entry.path, reference: ref.reference })
            return next
          })
        })
        .catch((e: unknown) => {
          pendingRef.current.delete(key)
          if (generation !== generationRef.current) return
          setSelectionError(e instanceof Error ? e.message : 'failed to reference file')
        })
    },
    [selection],
  )

  const removeSelect = useCallback((root: string, path: string) => {
    setSelection((prev) => {
      const next = new Map(prev)
      next.delete(selectionKey(root, path))
      return next
    })
  }, [])

  function submit(): void {
    const references = Array.from(selection.values()).map((f) => f.reference)
    if (mode === 'picker') {
      bumpGeneration()
      onInsertRef.current?.(references)
      setSelection(new Map())
      setOpen(false)
      restoreOpener()
      return
    }
    void copyToClipboard(references.join('\n'))
  }

  return (
    <FileBrowserContext.Provider value={{ openBrowser, pickFiles, enabled }}>
      {children}
      {open && (
        <FileBrowserModal
          mode={mode}
          selection={Array.from(selection.values())}
          onToggleSelect={toggleSelect}
          onRemoveSelect={removeSelect}
          onClose={close}
          onSubmit={submit}
          initialView={viewRef.current}
          onViewChange={(view) => {
            viewRef.current = view
          }}
          attentionShortcut={attentionShortcut}
        />
      )}
      {selectionError !== null && (
        <div className="file-browser-toast" role="alert">
          {selectionError}
        </div>
      )}
    </FileBrowserContext.Provider>
  )
}
