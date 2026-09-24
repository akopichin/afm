import { useCallback, useEffect, useRef, useState, type ChangeEvent, type KeyboardEvent, type ReactElement } from 'react'
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'
import { useImagePaste } from '../../hooks/use-image-paste'
import { useCaretInsert } from '../../hooks/use-caret-insert'
import { useFileBrowserOptional } from '../file-browser'

type PasteableTextareaProps = {
  stageId: string
  value: string
  onChange: (value: string) => void
  placeholder?: string
  className?: string
  autoFocus?: boolean
  disabled?: boolean
  maxHeight?: number
  onKeyDown?: (event: KeyboardEvent<HTMLTextAreaElement>) => void
  // onSubmit — отправка по Cmd/Ctrl+Enter (единообразно для ВСЕХ полей ввода:
  // комментарий плана/вопроса, ответ, заметка агенту). Встроено в
  // PasteableTextarea, чтобы каждый потребитель не дублировал обработчик и это
  // не «отваливалось» при рефакторингах. Не вызывается, когда поле disabled.
  onSubmit?: () => void
  // Показывает кнопку "Attach project file" (Task 14, файловый браузер —
  // Task 13): открывает пикер файлов проекта и вставляет собранные референсы
  // в текущую позицию каретки. По умолчанию выключено — большинство мест, где
  // используется PasteableTextarea (custom-answer в DialogChannel,
  // AgentNoteModal, pre-note), не должны предлагать вложение файлов проекта —
  // только per-line комментарии в PlanPanel/DialogChannel (см. бриф Task 14).
  // Компонент остаётся пригодным для использования ВНЕ FileBrowserProvider,
  // пока этот проп выключен: см. AttachFileButton ниже — вызов useFileBrowser()
  // живёт в отдельном компоненте, монтируемом только когда allowFileReferences=true.
  // Кнопка также не рендерится, когда FileBrowserProvider отключён
  // (capabilities.file_browser=false — Finding 5): PlanPanel/DialogChannel
  // передают этот проп безусловно, реальный гейт живёт в провайдере/
  // useFileBrowserEnabled(), а не разбросан по каждой панели.
  allowFileReferences?: boolean
  // attachInline — кнопка Attach (и её скрытый file-input) рендерится ВНУТРИ
  // строки ввода рядом с textarea, а не на верхней полосе. Превью-миниатюры
  // по-прежнему остаются на полосе НАД строкой ввода — переезжает только кнопка.
  // По умолчанию выключено: дефолтная раскладка (полоса с кнопкой над textarea)
  // не меняется ни на байт. Включает FeedComposer, где строка ввода —
  // `[attach] [textarea] [✈]`.
  attachInline?: boolean
}

// Drop-in replacement for a plain <textarea>, used everywhere a user writes
// text that ends up in front of the agent (AgentNoteModal, DialogChannel's
// custom-answer and line-comment boxes, PlanPanel's line-comment box).
// Composes the existing auto-grow behavior with clipboard-image paste — Cmd+V
// with a screenshot on the clipboard uploads it and inserts a
// "[Screenshot: <path>]" reference at the caret; the agent reads the file
// itself when it decides to (see
// docs/superpowers/specs/2026-08-11-clipboard-image-paste-design.md).
export function PasteableTextarea({
  stageId,
  value,
  onChange,
  placeholder,
  className,
  autoFocus,
  disabled,
  maxHeight = 400,
  onKeyDown,
  onSubmit,
  allowFileReferences = false,
  attachInline = false,
}: PasteableTextareaProps): ReactElement {
  const autoGrowRef = useAutoGrowTextarea(value, maxHeight)
  const { nodeRef, attachments, onPaste, uploadFiles, retryAttachment, removeAttachment } = useImagePaste(stageId, value, onChange)
  const imageInputRef = useRef<HTMLInputElement | null>(null)
  const { nodeRef: caretNodeRef, insertAtCaret } = useCaretInsert(value, onChange)
  // Скрепка (Finding #6a второго раунда): поле, включившее allowFileReferences,
  // всегда получает загрузку изображения — attachment endpoint работает и в
  // host-режиме, не только в Docker. Проект-пикер («Choose project file…»)
  // дополнительно требует включённого файлового браузера (capability), но его
  // отсутствие БОЛЬШЕ не прячет весь Attach вместе с «Upload image…».
  // Скрепка не показывается, когда поле disabled (Finding #6b) — иначе можно
  // было вставить ссылку в задизейбленное поле (напр. allow_custom:false), и
  // сервер отвечал 400. allowFileReferences уже подразумевает FileBrowserProvider
  // (см. инвариант ниже), поэтому AttachMenu безопасно зовёт useFileBrowser().
  const showAttachButton = allowFileReferences && disabled !== true

  const setRefs = useCallback(
    (node: HTMLTextAreaElement | null) => {
      nodeRef.current = node
      caretNodeRef.current = node
      // Note: autoGrowRef is called after nodeRef is set, in a stable manner
      autoGrowRef(node)
    },
    [],
  )

  function handleChange(event: ChangeEvent<HTMLTextAreaElement>): void {
    onChange(event.target.value)
  }

  // Cmd/Ctrl+Enter → onSubmit (единый путь отправки во всех полях). Компонуется с
  // переданным onKeyDown: сначала обрабатываем submit, иначе делегируем дальше.
  function handleKeyDown(event: KeyboardEvent<HTMLTextAreaElement>): void {
    if (onSubmit !== undefined && !disabled && (event.metaKey || event.ctrlKey) && event.key === 'Enter') {
      event.preventDefault()
      onSubmit()
      return
    }
    onKeyDown?.(event)
  }

  // Кнопка Attach + её скрытый file-input — единый блок, рендерится РОВНО один
  // раз: либо на полосе (дефолт), либо во входной строке (attachInline). Общий
  // JSX-фрагмент гарантирует единственный `<input type=file>`.
  const attachControls = showAttachButton ? (
    <>
      <AttachMenu
        onInsertFileReference={insertAtCaret}
        onUploadImage={() => imageInputRef.current?.click()}
      />
      <input
        ref={imageInputRef}
        type="file"
        accept="image/*"
        multiple
        hidden
        onChange={(e) => {
          const files = Array.from(e.target.files ?? [])
          // Сброс value ДО await — иначе повторный выбор того же файла
          // не вызовет onChange (браузер сравнивает значения).
          e.target.value = ''
          if (files.length > 0) void uploadFiles(files)
        }}
      />
    </>
  ) : null

  const textarea = (
    <textarea
      ref={setRefs}
      className={className}
      value={value}
      placeholder={placeholder}
      autoFocus={autoFocus}
      disabled={disabled}
      onChange={handleChange}
      onPaste={onPaste}
      onKeyDown={handleKeyDown}
    />
  )

  // attachInline: кнопка ушла в строку ввода, поэтому полоса нужна только под
  // превью. Дефолт: полоса как раньше (превью + кнопка) — DOM не меняется.
  const showStrip = attachments.length > 0 || (showAttachButton && !attachInline)

  return (
    <div className="pasteable-textarea-wrap">
      {showStrip && (
        <div className="pasteable-attachments">
          {attachments.map((attachment) => {
            const reason = attachment.errorMsg ?? 'Upload failed'
            return (
              <div key={attachment.id} className="pasteable-attachment-item">
                <div className={`pasteable-attachment${attachment.uploading ? ' uploading' : ''}${attachment.failed ? ' failed' : ''}`}>
                  <img src={attachment.previewUrl} alt={attachment.failed ? `Failed upload: ${reason}` : 'Pasted screenshot'} />
                  {/* Упавшая загрузка НЕ исчезает — остаётся чипом с Retry + Remove
                      (Finding #6c), вместо авто-исчезновения через 4с. */}
                  {attachment.failed && (
                    <button
                      type="button"
                      className="pasteable-attachment-retry"
                      aria-label={`Retry upload (${reason})`}
                      onClick={() => retryAttachment(attachment.id)}
                    >
                      ↻
                    </button>
                  )}
                  <button
                    type="button"
                    className="pasteable-attachment-remove"
                    aria-label={attachment.failed ? 'Remove failed upload' : 'Remove pasted image'}
                    onClick={() => removeAttachment(attachment.id)}
                  >
                    ✕
                  </button>
                </div>
                {/* Причина видна текстом (Finding #6, раунд 3): на touch title
                    не показывается, поэтому дублируем видимой подписью + role=alert. */}
                {attachment.failed && (
                  <span className="pasteable-attachment-reason" role="alert">{reason}</span>
                )}
              </div>
            )
          })}
          {/* Дефолт: кнопка на полосе. attachInline: кнопка — в строке ввода ниже. */}
          {!attachInline && attachControls}
        </div>
      )}
      {attachInline ? (
        <div className="pasteable-input-row">
          {attachControls}
          {textarea}
        </div>
      ) : (
        textarea
      )}
    </div>
  )
}

// Отдельный подкомпонент, а не инлайн-кнопка внутри PasteableTextarea: useFileBrowser()
// бросает исключение вне FileBrowserProvider (см. FileBrowserProvider.tsx), а
// PasteableTextarea обязан оставаться пригодным для использования без провайдера,
// пока allowFileReferences=false. Вынеся вызов хука в компонент, который монтируется
// ТОЛЬКО когда allowFileReferences=true, мы просто не вызываем useFileBrowser(),
// когда он не нужен — вместо того чтобы пытаться вызвать хук условно внутри одного
// компонента (что нарушило бы Rules of Hooks).
//
// Скрепка-меню (Finding #6): раньше здесь была одна кнопка «Attach project file»,
// ведущая только в pickFiles — изображение с компьютера можно было добавить лишь
// неочевидной вставкой из буфера. Теперь скрепка раскрывает два явных пути:
// «Choose project file…» (тот же pickFiles) и «Upload image…» (нативный выбор
// файла → uploadFiles, реюз пути загрузки вставки из буфера).
function AttachMenu({
  onInsertFileReference,
  onUploadImage,
}: {
  onInsertFileReference: (text: string) => void
  onUploadImage: () => void
}): ReactElement | null {
  // Не throwing-версия: «Upload image…» доступен и без FileBrowserProvider;
  // «Choose project file…» — только когда провайдер есть и enabled (Docker).
  const fb = useFileBrowserOptional()
  const enabled = fb?.enabled ?? false
  const [open, setOpen] = useState(false)
  const wrapRef = useRef<HTMLDivElement | null>(null)
  const attachBtnRef = useRef<HTMLButtonElement | null>(null)

  // Гвард от вставки в уже неактуальную цель (бриф Task 14, п.6 — "stale-picker
  // guard"): пока модалка пикера открыта, пользователь может уйти с этой конкретной
  // строки-комментария (сменить стадию/вопрос, закрыть форму комментария) —
  // PasteableTextarea (и это меню) тогда размонтируется, но callback, сохранённый в
  // FileBrowserProvider (onInsertRef), всё равно будет вызван по клику "Insert
  // references" — это обычная функция, не привязанная к рендер-циклу React.
  // mountedRef отличает "цель всё ещё та же" от "цель уже пропала".
  const mountedRef = useRef(true)
  useEffect(
    () => () => {
      mountedRef.current = false
    },
    [],
  )

  // Закрытие меню кликом вне / по Escape (тот же приём, что и у кебаба стадии).
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent): void => {
      if (wrapRef.current !== null && !wrapRef.current.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: globalThis.KeyboardEvent): void => {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  // enabled (fileBrowserEnabled) гейтит ТОЛЬКО проект-пикер; «Upload image…»
  // доступен всегда (Finding #6a). Само меню монтируется вызывающим при
  // allowFileReferences (внутри FileBrowserProvider), поэтому useFileBrowser()
  // выше безопасен.
  function chooseProjectFile(): void {
    // Finding #7 (второй раунд): FileBrowserProvider запоминает opener как
    // document.activeElement на момент pickFiles(). Если бы мы просто закрыли
    // меню, активным был бы ПУНКТ меню «Choose project file…», который тут же
    // размонтируется → provider на закрытии увидит isConnected===false и не
    // вернёт фокус. Поэтому СНАЧАЛА возвращаем фокус на стабильную кнопку Attach
    // (она остаётся в DOM), и только потом открываем пикер — provider захватит
    // именно её, и фокус корректно вернётся на неё после закрытия оверлея.
    attachBtnRef.current?.focus()
    setOpen(false)
    // Пункт рендерится только при enabled → fb гарантированно не null.
    fb?.pickFiles((refs) => {
      if (!mountedRef.current) {
        window.alert('Target comment is no longer available')
        return
      }
      onInsertFileReference(refs.join('\n'))
    })
  }

  function uploadImage(): void {
    setOpen(false)
    onUploadImage()
  }

  return (
    <div className="pasteable-attach" ref={wrapRef}>
      <button
        ref={attachBtnRef}
        type="button"
        className="pasteable-attach-btn"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label="Attach"
        onClick={() => setOpen((v) => !v)}
      >
        <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="M21 11.5 12.5 20a5 5 0 0 1-7-7l8-8a3.2 3.2 0 0 1 4.5 4.5l-8 8a1.5 1.5 0 0 1-2-2l7.5-7.5" />
        </svg>
        <span className="pasteable-attach-label">Attach</span>
      </button>
      {open && (
        <ul className="pasteable-attach-menu" role="menu">
          {enabled && (
            <li role="none">
              <button type="button" role="menuitem" onClick={chooseProjectFile}>Choose project file…</button>
            </li>
          )}
          <li role="none">
            <button type="button" role="menuitem" onClick={uploadImage}>Upload image…</button>
          </li>
        </ul>
      )}
    </div>
  )
}
