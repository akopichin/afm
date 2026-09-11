import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactElement, type ReactNode } from 'react'
import { getReference, type TreeEntry } from '../../api/files-client'
import { FileBrowserModal, type SelectedFile } from './FileBrowserModal'

type FileBrowserMode = 'browse' | 'picker'

// Совпадает с use-status.ts's FlowStatus['flowPauseState'] — продублирован тем
// же приёмом, что и в FileViewer.tsx/FileBrowserModal.tsx/ReviewBanner.tsx (не
// тянуть весь модуль use-status ради одного литерала).
type FlowPauseState = 'none' | 'paused' | 'resuming'

type FileBrowserContextValue = {
  // Открывает модалку в свободном режиме просмотра проекта (кнопка снизу —
  // "Copy references", кладёт выбранное в буфер обмена).
  openBrowser: () => void
  // Открывает модалку в режиме выбора файлов для вставки: onInsert получает
  // собранные референсы выбранных файлов и вызывается по клику на "Insert
  // references" (который тут же закрывает модалку).
  pickFiles: (onInsert: (references: string[]) => void) => void
  // capabilities.file_browser с бэкенда (Finding 5 фикс-раунда: раньше гейт
  // жил только в FlowHeader, поэтому comment-пикеры в PlanPanel/DialogChannel
  // оставались видимыми и в хост-режиме с выключенным браузером файлов, а
  // клик по ним бил в отключённый /api/files/*). false — единственный
  // источник правды "показывать ли кнопку 'Attach project file'/'Open
  // project files' и можно ли вообще открыть модалку".
  enabled: boolean
}

const FileBrowserContext = createContext<FileBrowserContextValue | null>(null)

export function useFileBrowser(): FileBrowserContextValue {
  const ctx = useContext(FileBrowserContext)
  if (ctx === null) throw new Error('useFileBrowser must be used within a FileBrowserProvider')
  return ctx
}

// Лёгкий вариант useFileBrowser() для мест, которым нужен только флаг
// enabled ДО того, как решать, монтировать ли что-то, зовущее useFileBrowser()
// (см. PasteableTextarea: showStrip считается снаружи AttachFileButton, чтобы
// не рисовать пустую полосу с отступом, когда кнопки в ней всё равно не
// будет). useContext, в отличие от useFileBrowser(), не бросает исключение
// вне провайдера — это сохраняет для PasteableTextarea инвариант "работает
// без FileBrowserProvider, пока файловые пропсы выключены".
export function useFileBrowserEnabled(): boolean {
  const ctx = useContext(FileBrowserContext)
  return ctx?.enabled ?? false
}

// useFileBrowserOptional() — как useFileBrowser(), но НЕ бросает вне провайдера
// (возвращает null). Нужен скрепке-меню: «Upload image…» работает без файлового
// браузера (и без провайдера), а «Choose project file…» гейтится наличием ctx +
// enabled. Так PasteableTextarea с allowFileReferences остаётся пригодным к
// рендеру без FileBrowserProvider (например, AgentNoteModal в юнит-тестах).
export function useFileBrowserOptional(): FileBrowserContextValue | null {
  return useContext(FileBrowserContext)
}

type FileBrowserProviderProps = {
  children: ReactNode
  // Смена flowName/startedAt = новый прогон флоу — выбор файлов от
  // предыдущего прогона больше не имеет смысла (см. бриф Task 13: "cleared
  // when the run changes"), поэтому провайдер сбрасывает и selection, и
  // открытую модалку сам, без участия вызывающего кода.
  flowName: string
  startedAt: string
  // capabilities.file_browser, проброшенный из App.tsx (см. use-status.ts).
  // По умолчанию true — большинство существующих мест монтируют провайдер
  // напрямую (тесты, App.tsx до первого /api/status) и ожидают обычное
  // поведение; App.tsx сам передаёт явное значение из статуса рана, где
  // неизвестное/загружающееся состояние уже трактуется как false
  // (см. DEFAULT_STATUS.capabilities в use-status.ts).
  enabled?: boolean
  // Флоу-wide review-pause состояние (см. use-status.ts), проброшенное в
  // FileBrowserModal → FileViewer, чтобы аннотирование строк файла было живым
  // только пока флоу реально на паузе (Task 23's "make annotation live"). По
  // умолчанию 'none' — существующие вызывающие (тесты, места без review-pause
  // фичи) не обязаны его передавать.
  flowPauseState?: FlowPauseState
  // Finding #4 второго раунда. onOpenChange — сообщает наверх (App), открыт ли
  // оверлей: App использует это как suppression, чтобы новое ожидание не
  // авто-открывалось ЗА непрозрачной модалкой. attentionShortcut — ждущее
  // действие, к которому модалка покажет кнопку-шорткат в шапке (закрывает Files
  // и открывает его). null — ожидания нет.
  onOpenChange?: (open: boolean) => void
  attentionShortcut?: { label: string; onActivate: () => void } | null
}

// JSON.stringify — не разделитель-символ, а безопасная сериализация пары:
// один составной ключ вместо конкатенации через любой символ-разделитель
// (пробел, запятая, ...) — такой разделитель ничем не гарантированно
// отсутствует внутри root/path, поэтому ("a", "b/c") и ("a/b", "c") могли бы
// схлопнуться в один и тот же ключ. JSON.stringify кодирует длины строк
// явно, коллизия невозможна в принципе, а не "маловероятна".
//
// (Раньше здесь был шаблонный литерал с сырым байтом 0x00 вместо строки-
// разделителя — валидный в рантайме JS, но превращавший сам .tsx-файл в
// невалидный UTF-8: git видел файл как бинарный, `git diff` печатал "Binary
// files differ", обычный построчный ревью кода был невозможен. JSON.stringify
// исключает контрольные байты в исходнике вообще, а не просто чинит один
// конкретный разделитель.)
function selectionKey(root: string, path: string): string {
  return JSON.stringify([root, path])
}

// Контекст file-browser: держит mode/pending-onInsert/selection и монтирует
// FileBrowserModal только пока открыто (бриф: "Renders FileBrowserModal when
// open") — сама модалка полностью размонтируется на закрытии, поэтому вся
// её "долгоживущая" часть состояния (что выбрано) обязана жить здесь, а не в
// самой модалке.
export function FileBrowserProvider({ children, flowName, startedAt, enabled = true, flowPauseState = 'none', onOpenChange, attentionShortcut = null }: FileBrowserProviderProps): ReactElement {
  const [open, setOpen] = useState(false)
  const [mode, setMode] = useState<FileBrowserMode>('browse')
  const [selection, setSelection] = useState<Map<string, SelectedFile>>(new Map())
  const [selectionError, setSelectionError] = useState<string | null>(null)
  const onInsertRef = useRef<((references: string[]) => void) | null>(null)

  // Finding 7: генерация "эпохи" selection'а — счётчик, а не boolean, потому
  // что за время жизни провайдера таких эпох может смениться сколько угодно
  // (несколько прогонов флоу, несколько pick-сессий). toggleSelect запоминает
  // генерацию на момент старта запроса; если к моменту ответа она уже другая —
  // ответ применять нельзя, эта selection ему больше не принадлежит. pendingRef
  // отдельно защищает от двойного клика по одному и тому же файлу, пока первый
  // запрос ещё летит (иначе — два параллельных getReference и потенциальный
  // двойной add).
  const generationRef = useRef(0)
  const pendingRef = useRef<Set<string>>(new Set())

  // Элемент, из которого открыли оверлей (кнопка Files / скрепка Attach). Модалка
  // — focus trap, поэтому при закрытии фокус должен вернуться туда, откуда пришёл
  // (иначе он «падает» на <body>, и клавиатурный пользователь теряет место). Ловим
  // на открытии, восстанавливаем на закрытии/сабмите.
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
    restoreOpener() // no-op на маунте (opener пуст) и когда кнопка уже исчезла
  }, [flowName, startedAt])

  // Сообщаем наверх факт открытия/закрытия (suppression в App, Finding #4).
  // Через ref, чтобы смена идентичности колбэка не дёргала эффект лишний раз.
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
    // Пикер закрывается (Esc/✕) без сабмита — если это была picker-сессия,
    // её эпоха тоже кончилась: следующий pickFiles() не должен унаследовать
    // ещё не разрешившийся getReference из отменённой сессии (см. Finding 7,
    // тот же принцип, что и submit() ниже).
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
          attentionShortcut={attentionShortcut}
          flowPauseState={flowPauseState}
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

// Буфер обмена недоступен в некоторых окружениях (не-HTTPS, jsdom без
// permissions, отказ пользователя) — best-effort, как и остальные
// неблокирующие побочные действия в дашборде (см. AppendNotice в orchestrator).
async function copyToClipboard(text: string): Promise<void> {
  try {
    await navigator.clipboard?.writeText(text)
  } catch {
    // не критично — референсы всё равно видны чипами в футере модалки
  }
}
