import { useLayoutEffect, useRef, useState, type ClipboardEvent, type MutableRefObject } from 'react'
import { AttachmentUploadError, uploadAttachment } from '../../api/run-client'

export type PasteAttachment = {
  id: string
  previewUrl: string
  uploading: boolean
}

export type UseImagePasteResult = {
  nodeRef: MutableRefObject<HTMLTextAreaElement | null>
  attachments: PasteAttachment[]
  uploadError: string | null
  onPaste: (event: ClipboardEvent<HTMLTextAreaElement>) => void
  // uploadFiles — тот же путь загрузки, что и вставка из буфера, но для файлов,
  // выбранных явно (native <input type=file> — пункт «Upload image…»). Стартует
  // с текущей позиции каретки (или конца значения), загружает последовательно и
  // вставляет «[Screenshot: <path>]» — реюз, а не второй механизм.
  uploadFiles: (files: File[]) => Promise<void>
  removeAttachment: (id: string) => void
}

type AttachmentRecord = PasteAttachment & { insertedText: string | null }

const ERROR_DISPLAY_MS = 4000

// Backs PasteableTextarea's paste handling: uploads a pasted clipboard image
// via uploadAttachment and splices "[Screenshot: <path>]\n" into the
// controlled value at the caret. Multiple images in one paste are uploaded
// sequentially (not in parallel), with caret advanced between each insertion.
// At each upload resolution, reads the live textarea value (valueRef.current),
// handling concurrent edits safely (e.g., user typing or deleting while upload
// is pending).
export function useImagePaste(
  stageId: string,
  value: string,
  onChange: (value: string) => void,
): UseImagePasteResult {
  const nodeRef = useRef<HTMLTextAreaElement | null>(null)
  const valueRef = useRef(value)
  valueRef.current = value
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange
  const removedIds = useRef<Set<string>>(new Set())
  const nextId = useRef(0)
  const errorTimer = useRef<number | undefined>(undefined)
  const pendingCaret = useRef<number | null>(null)

  const [attachments, setAttachments] = useState<AttachmentRecord[]>([])
  const [uploadError, setUploadError] = useState<string | null>(null)

  useLayoutEffect(() => {
    if (pendingCaret.current === null) return
    const el = nodeRef.current
    if (el !== null) {
      el.selectionStart = pendingCaret.current
      el.selectionEnd = pendingCaret.current
    }
    pendingCaret.current = null
  }, [value])

  function showError(message: string): void {
    setUploadError(message)
    if (errorTimer.current !== undefined) window.clearTimeout(errorTimer.current)
    errorTimer.current = window.setTimeout(() => setUploadError(null), ERROR_DISPLAY_MS)
  }

  async function uploadOne(file: File, caret: number): Promise<{ caret: number } | null> {
    const id = String(nextId.current)
    nextId.current += 1
    const previewUrl = URL.createObjectURL(file)
    setAttachments((prev) => [...prev, { id, previewUrl, uploading: true, insertedText: null }])

    try {
      const { path } = await uploadAttachment(stageId, file)

      if (removedIds.current.has(id)) {
        URL.revokeObjectURL(previewUrl)
        return null
      }

      const baseValue = valueRef.current
      const clampedCaret = Math.min(caret, baseValue.length)
      const inserted = `[Screenshot: ${path}]\n`
      const before = baseValue.slice(0, clampedCaret)
      const after = baseValue.slice(clampedCaret)
      const next = before + inserted + after
      pendingCaret.current = before.length + inserted.length
      onChangeRef.current(next)
      setAttachments((prev) =>
        prev.map((a) => (a.id === id ? { ...a, uploading: false, insertedText: inserted } : a)),
      )
      return { caret: before.length + inserted.length }
    } catch (err) {
      URL.revokeObjectURL(previewUrl)
      setAttachments((prev) => prev.filter((a) => a.id !== id))
      if (!removedIds.current.has(id)) {
        const status = err instanceof AttachmentUploadError ? err.status : 0
        showError(
          status === 413
            ? 'Image too large (max 10 MB)'
            : status === 415
              ? 'Unsupported image type'
              : 'Upload failed',
        )
      }
      return null
    }
  }

  // Общий последовательный проход загрузки — используется и вставкой из буфера,
  // и явным выбором файлов (uploadFiles). Каретка продвигается между вставками.
  async function runUploads(files: File[], startCaret: number): Promise<void> {
    let caret = startCaret
    for (const file of files) {
      const result = await uploadOne(file, caret)
      if (result !== null) {
        caret = result.caret
      }
    }
  }

  function onPaste(event: ClipboardEvent<HTMLTextAreaElement>): Promise<void> | undefined {
    const items = event.clipboardData?.items
    if (items === undefined || items === null) return undefined

    const files: File[] = []
    for (let i = 0; i < items.length; i++) {
      const item = items[i]
      if (item !== undefined && item.kind === 'file' && item.type.startsWith('image/')) {
        const file = item.getAsFile()
        if (file !== null) files.push(file)
      }
    }
    if (files.length === 0) return undefined

    event.preventDefault()
    const startCaret = event.currentTarget.selectionStart ?? valueRef.current.length
    return runUploads(files, startCaret)
  }

  // uploadFiles — путь для явно выбранных изображений (пункт «Upload image…»).
  // Отфильтровываем не-изображения (input accept — подсказка, не гарантия) и
  // стартуем с текущей каретки/конца значения. Возвращаем промис, чтобы вызывающий
  // мог сбросить value input'а после завершения.
  async function uploadFiles(files: File[]): Promise<void> {
    const images = files.filter((f) => f.type.startsWith('image/'))
    if (images.length === 0) return
    const caret = nodeRef.current?.selectionStart ?? valueRef.current.length
    await runUploads(images, caret)
  }

  function removeAttachment(id: string): void {
    const target = attachments.find((a) => a.id === id)
    if (target === undefined) return

    URL.revokeObjectURL(target.previewUrl)
    if (target.insertedText !== null) {
      const idx = valueRef.current.indexOf(target.insertedText)
      if (idx !== -1) {
        onChangeRef.current(valueRef.current.slice(0, idx) + valueRef.current.slice(idx + target.insertedText.length))
      }
    } else {
      removedIds.current.add(id)
    }
    setAttachments((prev) => prev.filter((a) => a.id !== id))
  }

  return { nodeRef, attachments, uploadError, onPaste: onPaste as (event: ClipboardEvent<HTMLTextAreaElement>) => void, uploadFiles, removeAttachment }
}
