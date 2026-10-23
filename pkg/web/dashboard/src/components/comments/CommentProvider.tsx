import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from 'react'
import { useRetryableSubmit } from '../../hooks/use-retryable-submit'
import { useRunEpoch } from '../../hooks/use-run-epoch'
import {
  acknowledgeSent,
  discardComment,
  editComment,
  emptyCommentState,
  foldComment,
  foldFileComments,
  listComments,
  openComment,
  reanchorComment,
  rememberRecipient,
  saveAndFoldComment,
  saveComment,
  setCommentRecipient,
  type CommentEntry,
  type CommentInit,
  type CommentState,
  type Reanchor,
  type SentRevision,
} from './comment-state'

// FrozenSend — пакет, зафиксированный в момент нажатия Send: повтор после ошибки отправляет ровно его же,
// а новый ввод остаётся следующей редакцией.
export type FrozenSend = {
  recipientId: string
  route: 'mesh' | 'assistant'
  originStage: string
  body: string
  revisions: SentRevision[]
}

// Deliver не должен зависеть от размонтируемых компонентов: он может выполняться и повторяться после
// закрытия Files.
export type Deliver = (frozen: Readonly<FrozenSend>, requestId: string) => Promise<unknown>

export type CommentsApi = {
  comments: CommentEntry[]
  lastRecipientId: string
  open: (key: string, init: CommentInit) => void
  edit: (key: string, text: string) => void
  save: (key: string) => void
  fold: (key: string) => void
  // foldFile — сворачивает открытые формы файла при уходе из него; ссылка стабильна между рендерами.
  foldFile: (root: string, path: string) => void
  // saveAndFold — Save/Add: сохраняет и сворачивает форму; пустой текст ничего не меняет в сохранённом.
  saveAndFold: (key: string) => void
  discard: (key: string) => void
  setRecipient: (key: string, recipientId: string) => void
  reanchor: (key: string, target: Reanchor) => void
  // rememberRecipient — адресат по умолчанию для новых комментариев, не меняя существующие (Send all).
  rememberRecipient: (recipientId: string) => void
  // scroll — положение просмотра документа по ключу; живёт в ране, чтобы переживать размонтирование панели.
  scroll: { get: (key: string) => number | undefined; set: (key: string, top: number) => void }
  // acknowledge — подтверждение приёма вне send (план, ответ на вопрос): снимает только захваченные ревизии.
  // Функция привязана к эпохе рана, поэтому вызов, взятый до смены рана, ничего не тронет в новом.
  acknowledge: (sent: readonly SentRevision[]) => void
  send: {
    pending: boolean
    error: string | null
    operation: Readonly<FrozenSend> | null
    accepted: { count: number; recipientId: string } | null
    // start игнорируется, пока есть неразрешённая операция: разрешить её можно только retry или cancel.
    start: (frozen: FrozenSend, deliver: Deliver) => void
    retry: () => void
    cancel: () => void
    dismissAccepted: () => void
  }
}

type Accepted = { count: number; recipientId: string }
// RunComments — всё, что принадлежит одному ране; смена рана сбрасывает целиком.
type RunComments = { comments: CommentState; operation: Readonly<FrozenSend> | null; accepted: Accepted | null }

function emptyRun(epoch: number): RunComments {
  return { comments: emptyCommentState(epoch), operation: null, accepted: null }
}

const CommentContext = createContext<CommentsApi | null>(null)

export function useComments(): CommentsApi {
  const api = useContext(CommentContext)
  if (api === null) throw new Error('useComments must be used inside CommentProvider')
  return api
}

// CommentProvider — run-scoped хранилище комментариев и единственная операция отправки. Монтируется выше
// PlanPanel, DialogChannel и FileBrowserProvider, поэтому закрытие Files не отменяет запрос и не теряет текст.
export function CommentProvider({ runId, children }: { runId: string; children: ReactNode }): React.ReactElement {
  const epoch = useRunEpoch(runId)
  const [run, setRun] = useState(() => emptyRun(epoch))
  if (run.comments.epoch !== epoch) setRun(emptyRun(epoch))

  const updateComments = useCallback((change: (s: CommentState) => CommentState) => {
    setRun((r) => ({ ...r, comments: change(r.comments) }))
  }, [])

  // Фенс по epoch: ответ запроса прошлого рана не вызовет onSuccess в новом.
  const submitter = useRetryableSubmit<FrozenSend>('comments', String(epoch))
  const { submit } = submitter
  const deliverRef = useRef<Deliver | null>(null)

  const launch = useCallback(
    (frozen: FrozenSend, deliver: Deliver) => {
      // Состояние операции пишем внутри send: он вызывается, только если submit действительно запускает
      // запрос, поэтому второй Send при pending не подменяет ни пакет, ни deliver.
      submit(
        frozen,
        (payload, requestId) => {
          deliverRef.current = deliver
          setRun((r) => ({ ...r, operation: payload, accepted: null }))
          return deliver(payload, requestId)
        },
        {
          onSuccess: () =>
            setRun((r) => ({
              comments: acknowledgeSent(r.comments, epoch, frozen.revisions),
              operation: null,
              accepted: { count: frozen.revisions.length, recipientId: frozen.recipientId },
            })),
        },
      )
    },
    [submit, epoch],
  )

  const { operation } = run
  const start = useCallback(
    (frozen: FrozenSend, deliver: Deliver) => {
      if (operation === null) launch(frozen, deliver)
    },
    [operation, launch],
  )
  const retry = useCallback(() => {
    const deliver = deliverRef.current
    if (operation !== null && deliver !== null) launch(operation, deliver)
  }, [operation, launch])

  // Позиции прокрутки не влияют на рендер, поэтому лежат в ref; смена рана заводит новую карту.
  const scrollRef = useRef({ epoch, tops: new Map<string, number>() })
  if (scrollRef.current.epoch !== epoch) scrollRef.current = { epoch, tops: new Map() }
  const scroll = useMemo(
    () => ({
      get: (key: string) => scrollRef.current.tops.get(key),
      set: (key: string, top: number) => void scrollRef.current.tops.set(key, top),
    }),
    [],
  )

  const acknowledge = useCallback(
    (sent: readonly SentRevision[]) => updateComments((s) => acknowledgeSent(s, epoch, sent)),
    [updateComments, epoch],
  )

  const foldFile = useCallback(
    (root: string, path: string) => updateComments((s) => foldFileComments(s, root, path)),
    [updateComments],
  )

  const cancel = useCallback(() => setRun((r) => ({ ...r, operation: null })), [])
  const dismissAccepted = useCallback(() => setRun((r) => ({ ...r, accepted: null })), [])

  const api = useMemo<CommentsApi>(
    () => ({
      comments: listComments(run.comments),
      lastRecipientId: run.comments.lastRecipientId,
      open: (key, init) => updateComments((s) => openComment(s, key, init)),
      edit: (key, text) => updateComments((s) => editComment(s, key, text)),
      save: (key) => updateComments((s) => saveComment(s, key)),
      fold: (key) => updateComments((s) => foldComment(s, key)),
      foldFile,
      saveAndFold: (key) => updateComments((s) => saveAndFoldComment(s, key)),
      discard: (key) => updateComments((s) => discardComment(s, key)),
      setRecipient: (key, recipientId) => updateComments((s) => setCommentRecipient(s, key, recipientId)),
      reanchor: (key, target) => updateComments((s) => reanchorComment(s, key, target)),
      rememberRecipient: (recipientId) => updateComments((s) => rememberRecipient(s, recipientId)),
      scroll,
      acknowledge,
      send: {
        pending: submitter.pending,
        error: operation === null ? null : submitter.error,
        operation,
        accepted: run.accepted,
        start,
        retry,
        cancel,
        dismissAccepted,
      },
    }),
    [run, operation, submitter.pending, submitter.error, updateComments, foldFile, scroll, acknowledge, start, retry, cancel, dismissAccepted],
  )

  return <CommentContext.Provider value={api}>{children}</CommentContext.Provider>
}
