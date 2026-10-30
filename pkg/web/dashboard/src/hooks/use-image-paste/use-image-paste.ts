import { useLayoutEffect, useRef, useState, type ClipboardEvent, type MutableRefObject } from 'react'
import { AttachmentUploadError, uploadAttachment } from '../../api/run-client'

export type PasteAttachment = {
  id: string
  previewUrl: string
  uploading: boolean
  // failed сохраняет чип с Retry/Remove; errorMsg показывает причину сбоя загрузки.
  failed: boolean
  errorMsg: string | null
}

export type AttachmentActions = {
  snapshot: () => ImagePasteDraft
  discard: (sent: ImagePasteDraft) => void
}

export type UseImagePasteResult = {
  attachmentActions: AttachmentActions
  nodeRef: MutableRefObject<HTMLTextAreaElement | null>
  attachments: PasteAttachment[]
  onPaste: (event: ClipboardEvent<HTMLTextAreaElement>) => void
  // uploadFiles — тот же путь загрузки, что и вставка из буфера, но для файлов, выбранных явно.
  uploadFiles: (files: File[]) => Promise<void>
  // retryAttachment — повторная загрузка ранее упавшего вложения (тот же File).
  retryAttachment: (id: string) => void
  removeAttachment: (id: string) => void
}

export type AttachmentRecord = PasteAttachment & { insertedText: string | null; file: File }
export type ImagePasteDraft = readonly AttachmentRecord[]

function errorMessageFor(err: unknown): string {
  const status = err instanceof AttachmentUploadError ? err.status : 0
  return status === 413 ? 'Image too large (max 10 MB)' : status === 415 ? 'Unsupported image type' : 'Upload failed'
}

// Backs PasteableTextarea's paste handling: uploads a pasted clipboard image via uploadAttachment and
// splices "[Screenshot: <path>]\n" into the controlled value at the caret.
export function useImagePaste(
  stageId: string,
  value: string,
  onChange: (value: string) => void,
  draft?: ImagePasteDraft,
  onDraftChange?: (draft: ImagePasteDraft) => void,
): UseImagePasteResult {
  const nodeRef = useRef<HTMLTextAreaElement | null>(null)
  const valueRef = useRef(value)
  valueRef.current = value
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange
  const removedIds = useRef<Set<string>>(new Set())
  const nextId = useRef(Math.max(0, ...(draft ?? []).map((a) => Number(a.id) + 1)))
  const pendingCaret = useRef<number | null>(null)

  const [attachments, setAttachmentState] = useState<AttachmentRecord[]>(() => [...(draft ?? [])])
  const recordsRef = useRef(attachments)
  const reportRef = useRef(onDraftChange)
  reportRef.current = onDraftChange
  function setAttachments(update: (previous: AttachmentRecord[]) => AttachmentRecord[]): void {
    const next = update(recordsRef.current)
    recordsRef.current = next
    setAttachmentState(next)
    reportRef.current?.(next)
  }

  // Поколение композера, привязанное к stageId. Панель DialogChannel рендерится с постоянным key="dialog",
  // поэтому при переходе A → B ЭТОТ хук остаётся тем же.
  const genRef = useRef(0)
  const stageIdRef = useRef(stageId)
  useLayoutEffect(() => {
    if (stageIdRef.current !== stageId) {
      recordsRef.current = [...(draft ?? [])]
      setAttachmentState(recordsRef.current)
      removedIds.current.clear()
      nextId.current = Math.max(0, ...(draft ?? []).map((a) => Number(a.id) + 1))
    }
    stageIdRef.current = stageId
    const report = onDraftChange
    return () => {
      genRef.current += 1
      const records = recordsRef.current
      if (report !== undefined) {
        if (records.some((a) => a.uploading)) {
          recordsRef.current = records.map((a) => a.uploading ? { ...a, uploading: false, failed: true, errorMsg: 'Upload interrupted — retry to attach' } : a)
          report(recordsRef.current)
        }
      } else records.forEach((a) => URL.revokeObjectURL(a.previewUrl))
    }
  }, [stageId])

  useLayoutEffect(() => {
    if (pendingCaret.current === null) return
    const el = nodeRef.current
    if (el !== null) {
      el.selectionStart = pendingCaret.current
      el.selectionEnd = pendingCaret.current
    }
    pendingCaret.current = null
  }, [value])

  // performUpload — сама загрузка для УЖЕ созданной записи. Используется и первичной загрузкой
  // (uploadOne), и повтором (retryAttachment).
  async function performUpload(id: string, file: File, previewUrl: string, caret: number): Promise<{ caret: number } | null> {
    const gen = genRef.current
    setAttachments((prev) => prev.map((a) => (a.id === id ? { ...a, uploading: true, failed: false, errorMsg: null } : a)))
    try {
      const { path } = await uploadAttachment(stageId, file)
      // После смены стадии игнорируем upload старой стадии, чтобы не вставить ссылку в новый composer.
      if (gen !== genRef.current) return null
      if (removedIds.current.has(id)) {
        URL.revokeObjectURL(previewUrl)
        return null
      }
      const baseValue = valueRef.current
      const clampedCaret = Math.min(caret, baseValue.length)
      const inserted = `[Screenshot: ${path}]\n`
      const before = baseValue.slice(0, clampedCaret)
      const after = baseValue.slice(clampedCaret)
      pendingCaret.current = before.length + inserted.length
      onChangeRef.current(before + inserted + after)
      setAttachments((prev) => prev.map((a) => (a.id === id ? { ...a, uploading: false, insertedText: inserted } : a)))
      return { caret: before.length + inserted.length }
    } catch (err) {
      // Устаревший (чужая стадия) ответ — тихо игнорируем.
      if (gen !== genRef.current) return null
      if (removedIds.current.has(id)) {
        URL.revokeObjectURL(previewUrl)
        setAttachments((prev) => prev.filter((a) => a.id !== id))
        return null
      }
      // Оставляем запись как failed (превью не отзываем — нужно для Retry-чипа).
      setAttachments((prev) => prev.map((a) => (a.id === id ? { ...a, uploading: false, failed: true, errorMsg: errorMessageFor(err) } : a)))
      return null
    }
  }

  async function uploadOne(file: File, caret: number): Promise<{ caret: number } | null> {
    const id = String(nextId.current)
    nextId.current += 1
    const previewUrl = URL.createObjectURL(file)
    setAttachments((prev) => [...prev, { id, previewUrl, uploading: true, failed: false, errorMsg: null, insertedText: null, file }])
    return performUpload(id, file, previewUrl, caret)
  }

  // Общий последовательный проход загрузки — используется и вставкой из буфера,
  // и явным выбором файлов (uploadFiles). Каретка продвигается между вставками.
  async function runUploads(files: File[], startCaret: number): Promise<void> {
    let caret = startCaret
    const generation = genRef.current
    for (const file of files) {
      if (generation !== genRef.current) return
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

  // uploadFiles — путь для явно выбранных изображений. Возвращаем промис, чтобы вызывающий мог сбросить
  // value input'а после завершения.
  async function uploadFiles(files: File[]): Promise<void> {
    const images = files.filter((f) => f.type.startsWith('image/'))
    if (images.length === 0) return
    const caret = nodeRef.current?.selectionStart ?? valueRef.current.length
    await runUploads(images, caret)
  }

  // retryAttachment — повтор упавшей загрузки тем же файлом. Стартует с текущей каретки/конца значения;
  // сам performUpload переведёт чип обратно в uploading и на успехе вставит ссылку.
  function retryAttachment(id: string): void {
    const target = attachments.find((a) => a.id === id)
    if (target === undefined || !target.failed) return
    const caret = nodeRef.current?.selectionStart ?? valueRef.current.length
    void performUpload(id, target.file, target.previewUrl, caret)
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

  const attachmentActions: AttachmentActions = {
    snapshot: () => recordsRef.current,
    discard: (sent) => {
      const urls = new Set(sent.map((a) => a.previewUrl))
      recordsRef.current.filter((a) => urls.has(a.previewUrl)).forEach((a) => {
        removedIds.current.add(a.id)
        URL.revokeObjectURL(a.previewUrl)
      })
      setAttachments((current) => current.filter((a) => !urls.has(a.previewUrl)))
    },
  }

  return { attachmentActions, nodeRef, attachments, onPaste: onPaste as (event: ClipboardEvent<HTMLTextAreaElement>) => void, uploadFiles, retryAttachment, removeAttachment }
}
