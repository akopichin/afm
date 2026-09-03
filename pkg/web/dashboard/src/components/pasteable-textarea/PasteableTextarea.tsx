import { useCallback, useEffect, useRef, type ChangeEvent, type KeyboardEvent, type ReactElement } from 'react'
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'
import { useImagePaste } from '../../hooks/use-image-paste'
import { useCaretInsert } from '../../hooks/use-caret-insert'
import { useFileBrowser } from '../file-browser'

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
  // Показывает кнопку "Attach project file" (Task 14, файловый браузер —
  // Task 13): открывает пикер файлов проекта и вставляет собранные референсы
  // в текущую позицию каретки. По умолчанию выключено — большинство мест, где
  // используется PasteableTextarea (custom-answer в DialogChannel,
  // AgentNoteModal, pre-note), не должны предлагать вложение файлов проекта —
  // только per-line комментарии в PlanPanel/DialogChannel (см. бриф Task 14).
  // Компонент остаётся пригодным для использования ВНЕ FileBrowserProvider,
  // пока этот проп выключен: см. AttachFileButton ниже — вызов useFileBrowser()
  // живёт в отдельном компоненте, монтируемом только когда allowFileReferences=true.
  allowFileReferences?: boolean
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
  allowFileReferences = false,
}: PasteableTextareaProps): ReactElement {
  const autoGrowRef = useAutoGrowTextarea(value, maxHeight)
  const { nodeRef, attachments, uploadError, onPaste, removeAttachment } = useImagePaste(stageId, value, onChange)
  const { nodeRef: caretNodeRef, insertAtCaret } = useCaretInsert(value, onChange)

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

  const showStrip = attachments.length > 0 || uploadError !== null || allowFileReferences

  return (
    <div className="pasteable-textarea-wrap">
      {showStrip && (
        <div className="pasteable-attachments">
          {attachments.map((attachment) => (
            <div key={attachment.id} className={`pasteable-attachment${attachment.uploading ? ' uploading' : ''}`}>
              <img src={attachment.previewUrl} alt="Pasted screenshot" />
              <button
                type="button"
                className="pasteable-attachment-remove"
                aria-label="Remove pasted image"
                onClick={() => removeAttachment(attachment.id)}
              >
                ✕
              </button>
            </div>
          ))}
          {uploadError !== null && <span className="pasteable-attachment-error">{uploadError}</span>}
          {allowFileReferences && <AttachFileButton onInsert={insertAtCaret} />}
        </div>
      )}
      <textarea
        ref={setRefs}
        className={className}
        value={value}
        placeholder={placeholder}
        autoFocus={autoFocus}
        disabled={disabled}
        onChange={handleChange}
        onPaste={onPaste}
        onKeyDown={onKeyDown}
      />
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
function AttachFileButton({ onInsert }: { onInsert: (text: string) => void }): ReactElement {
  const { pickFiles } = useFileBrowser()

  // Гвард от вставки в уже неактуальную цель (бриф Task 14, п.6 — "stale-picker
  // guard"): пока модалка пикера открыта, пользователь может уйти с этой конкретной
  // строки-комментария (сменить стадию/вопрос, закрыть форму комментария) —
  // PasteableTextarea (и эта кнопка) тогда размонтируется, но callback, сохранённый в
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

  function handleClick(): void {
    pickFiles((refs) => {
      if (!mountedRef.current) {
        window.alert('Target comment is no longer available')
        return
      }
      onInsert(refs.join('\n'))
    })
  }

  return (
    <button type="button" className="pasteable-attach-btn" onClick={handleClick}>
      Attach project file
    </button>
  )
}
