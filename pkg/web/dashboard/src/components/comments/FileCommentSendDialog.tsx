import { useEffect, useMemo, useRef, useState, type ReactElement } from 'react'
import { useModalDialog } from '../../lib/focus-trap'
import type { DraftComment } from '../file-browser/draft-comments'
import { useSideAgentContext } from '../side-agent/side-agent-context'
import { CommentQuote } from './CommentCard'
import { useComments } from './CommentProvider'
import { CommentRecipientSelect } from './CommentRecipientSelect'
import { deliverComments, messageLimitProblem, buildCommentMessage } from './comment-delivery'
import { useCommentRecipientChoice, useCommentRecipients, type CommentRecipient } from './comment-recipients'
import { isDraft } from './comment-state'
import {
  confirmationToken,
  fetchSources,
  fileKey,
  isSendableFileComment,
  sourceStatus,
  type FileEntry,
  type FileFetch,
  type SourceStatus,
} from './file-comment-source'

export type SourceTarget = { root: string; path: string; line: number }

const plural = (count: number, word: string): string => `${count} ${word}${count === 1 ? '' : 's'}`

type Checked = { entry: FileEntry; status: SourceStatus }

// Расхождение источника требует решения пользователя, пока он его не подтвердил именно для этого состояния файла.
function needsConfirmation({ entry, status }: Checked, confirmed: ReadonlyMap<string, string>): boolean {
  return (status.kind === 'changed' || status.kind === 'missing') && confirmed.get(entry.key) !== confirmationToken(status)
}

type Readiness = { body: string; confirmCount: number; reason: string | null }

// readiness — единственное место, решающее, можно ли отправлять: им пользуются и кнопка, и проверка перед запросом.
function readiness(
  checked: Checked[],
  confirmed: ReadonlyMap<string, string>,
  choice: { recipient: CommentRecipient | null; problem: string | null },
  message: string,
  verifying: boolean,
): Readiness {
  const drafts: DraftComment[] = checked.map(({ entry }) => ({
    root: entry.anchor.root,
    path: entry.anchor.path,
    displayPath: entry.anchor.displayPath,
    reference: entry.anchor.reference,
    line: entry.anchor.line,
    origLineText: entry.quote,
    text: entry.text,
  }))
  const body = buildCommentMessage(drafts, message)
  const confirmCount = checked.filter((c) => needsConfirmation(c, confirmed)).length
  const { recipient, problem } = choice
  const reason = (): string | null => {
    if (problem !== null) return problem
    if (checked.length === 0) return 'No file comments to send'
    if (verifying || checked.some((c) => c.status.kind === 'checking')) return 'Checking the source files…'
    if (checked.some((c) => c.status.kind === 'error')) return 'Could not read the source — check again; your text is kept'
    if (confirmCount > 0) return 'Confirm the changed source before sending'
    return recipient === null ? null : messageLimitProblem(recipient.route, body)
  }
  return { body, confirmCount, reason: reason() }
}

export type FileCommentSendDialogProps = {
  // only — ключ одного комментария (Send на карточке); null — все неотправленные файловые комментарии (Send all).
  only: string | null
  onClose: () => void
  onOpenSource: (target: SourceTarget) => void
  returnFocus?: () => HTMLElement | null | undefined
}

// FileCommentSendDialog — единый диалог Send / Send all: превью пакета, сверка источника, один адресат. Сам запрос
// живёт в CommentProvider, поэтому закрытие диалога или Files его не отменяет.
export function FileCommentSendDialog({ only, onClose, onOpenSource, returnFocus }: FileCommentSendDialogProps): ReactElement {
  const modalRef = useRef<HTMLDivElement | null>(null)
  useModalDialog(modalRef, onClose, returnFocus)
  const { send } = useComments()

  // Принятый пакет закрывает диалог; ответ, уже бывший при открытии, не считается.
  const acceptedAtOpen = useRef(send.accepted)
  useEffect(() => {
    if (send.accepted !== null && send.accepted !== acceptedAtOpen.current) onClose()
  }, [send.accepted, onClose])

  return (
    <div className="modal-overlay send-dialog-overlay">
      <div className="modal-content send-dialog" role="dialog" aria-modal="true" aria-label="Send file comments" ref={modalRef}>
        <header className="send-dialog-header">
          <h2>Send comments</h2>
          <button type="button" className="icon-btn" aria-label="Close send dialog" onClick={onClose}>
            ✕
          </button>
        </header>
        {send.operation !== null ? <SendOperation onClose={onClose} /> : <SendCompose only={only} onClose={onClose} onOpenSource={onOpenSource} />}
      </div>
    </div>
  )
}

function SendCompose({ only, onClose, onOpenSource }: Pick<FileCommentSendDialogProps, 'only' | 'onClose' | 'onOpenSource'>): ReactElement {
  const { comments, discard, reanchor, setRecipient, rememberRecipient, send } = useComments()
  const { sendExactMessage } = useSideAgentContext()

  const targets = useMemo(
    () => comments.filter(isSendableFileComment).filter((e) => only === null || e.key === only),
    [comments, only],
  )
  const [chosenId, setChosenId] = useState(() => (only === null ? '' : (comments.find((c) => c.key === only)?.recipientId ?? '')))
  const { recipient, problem } = useCommentRecipientChoice(chosenId)
  const [message, setMessage] = useState('')
  const [fetched, setFetched] = useState<Map<string, FileFetch> | null>(null)
  const [confirmed, setConfirmed] = useState<ReadonlyMap<string, string>>(new Map())
  const [verifying, setVerifying] = useState(false)
  const verifyingRef = useRef(false)
  const aliveRef = useRef(true)

  const checked: Checked[] = targets.map((entry) => ({ entry, status: sourceStatus(entry, fetched?.get(fileKey(entry))) }))
  const state = readiness(checked, confirmed, { recipient, problem }, message, verifying)
  const canSend = state.reason === null

  // Источник сверяется при открытии; ответ закрытого диалога игнорируется.
  async function refresh(): Promise<Map<string, FileFetch>> {
    const result = await fetchSources(targets)
    if (aliveRef.current) setFetched(result)
    return result
  }
  useEffect(() => {
    aliveRef.current = true
    void refresh()
    return () => {
      aliveRef.current = false
    }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  function checkAgain(): void {
    setFetched(null)
    void refresh()
  }

  // Перед запросом источник читается заново: файл мог измениться, пока диалог был открыт. Отправляется ровно то, что
  // пользователь подтвердил кликом (значения этого render), а форма на время проверки заблокирована; правка после
  // клика поднимает ревизию комментария и поэтому переживает подтверждение.
  async function submit(): Promise<void> {
    if (verifyingRef.current || !canSend || recipient === null) return
    verifyingRef.current = true
    setVerifying(true)
    const result = await fetchSources(targets)
    verifyingRef.current = false
    if (!aliveRef.current) return
    setVerifying(false)
    setFetched(result)

    const fresh = targets.map((entry) => ({ entry, status: sourceStatus(entry, result.get(fileKey(entry))) }))
    const final = readiness(fresh, confirmed, { recipient, problem }, message, false)
    if (final.reason !== null) return

    if (only !== null) setRecipient(only, recipient.id)
    else rememberRecipient(recipient.id)
    send.start(
      {
        recipientId: recipient.id,
        route: recipient.route,
        originStage: recipient.originStage,
        body: final.body,
        revisions: targets.map((e) => ({ key: e.key, revision: e.revision })),
      },
      (frozen, requestId) => deliverComments(frozen, requestId, sendExactMessage),
    )
  }

  const files = groupByFile(checked)
  const confirm = (entry: FileEntry, status: SourceStatus, on: boolean): void =>
    setConfirmed((prev) => {
      const next = new Map(prev)
      if (on) next.set(entry.key, confirmationToken(status))
      else next.delete(entry.key)
      return next
    })

  return (
    <>
      <fieldset className="send-dialog-body" disabled={verifying}>
        <p className="send-dialog-summary">
          {targets.length === 0 ? 'No file comments to send' : `${plural(targets.length, 'comment')} in ${plural(files.length, 'file')}`}
        </p>
        {files.map((file) => (
          <section key={file.key} className="send-dialog-file" aria-label={file.displayPath}>
            <h3 className="send-dialog-file-title">{file.displayPath}</h3>
            {file.items.map(({ entry, status }) => (
              <div key={entry.key} className="send-dialog-comment">
                <div className="comment-display-header">
                  <span className="comment-title">Line {entry.anchor.line}</span>
                  {isDraft(entry) && <span className="comment-draft-badge">Draft</span>}
                  {status.kind === 'changed' && <span className="comment-draft-badge comment-source-badge">Source changed</span>}
                  {status.kind === 'missing' && <span className="comment-draft-badge comment-source-badge">Source missing</span>}
                </div>
                <CommentQuote quote={entry.quote} />
                <div className="comment-text">{entry.text}</div>
                <SourceControls
                  entry={entry}
                  status={status}
                  confirmed={confirmed.get(entry.key) === confirmationToken(status)}
                  onConfirm={(on) => confirm(entry, status, on)}
                  onUseCurrent={() =>
                    status.kind === 'changed' &&
                    status.currentText !== null &&
                    reanchor(entry.key, { line: entry.anchor.line, quote: status.currentText, etag: status.etag })
                  }
                  onOpenSource={() => onOpenSource({ root: entry.anchor.root, path: entry.anchor.path, line: entry.anchor.line })}
                  onDelete={() => discard(entry.key)}
                />
              </div>
            ))}
          </section>
        ))}
        {checked.some((c) => c.status.kind === 'error') && (
          <button type="button" className="btn" onClick={checkAgain}>
            Check again
          </button>
        )}
        <label className="send-dialog-field">
          <span>Message (optional)</span>
          <textarea aria-label="Message" value={message} placeholder="Add a note for the agent…" onChange={(e) => setMessage(e.target.value)} />
        </label>
        <div className="send-dialog-field">
          <span>Send to</span>
          <CommentRecipientSelect recipientId={chosenId} onChange={setChosenId} />
        </div>
        {/* Причину выбора адресата показывает сам селектор, пустой пакет — заголовок. */}
        {state.reason !== null && state.reason !== problem && targets.length > 0 && <p className="send-dialog-hint">{state.reason}</p>}
      </fieldset>
      <footer className="send-dialog-footer">
        <button type="button" className="btn btn-cancel" onClick={onClose}>
          Cancel
        </button>
        <button type="button" className="btn btn-send" disabled={!canSend} onClick={() => void submit()}>
          {sendLabel(targets.length, state.confirmCount)}
        </button>
      </footer>
    </>
  )
}

function sendLabel(count: number, confirmCount: number): string {
  const base = `Send ${plural(count, 'comment')}`
  return confirmCount > 0 ? `${base} (${confirmCount} need confirmation)` : base
}

// Группы — по (root, path): displayPath у разных корней с одной меткой совпадает и служит только подписью.
function groupByFile(checked: Checked[]): { key: string; displayPath: string; items: Checked[] }[] {
  const byFile = new Map<string, Checked[]>()
  for (const item of checked) {
    const key = fileKey(item.entry)
    byFile.set(key, [...(byFile.get(key) ?? []), item])
  }
  return Array.from(byFile, ([key, items]) => ({
    key,
    displayPath: items[0]?.entry.anchor.displayPath ?? '',
    items: items.sort((a, b) => a.entry.anchor.line - b.entry.anchor.line),
  })).sort((a, b) => a.displayPath.localeCompare(b.displayPath) || a.key.localeCompare(b.key))
}

type SourceControlsProps = {
  entry: FileEntry
  status: SourceStatus
  confirmed: boolean
  onConfirm: (on: boolean) => void
  onUseCurrent: () => void
  onOpenSource: () => void
  onDelete: () => void
}

function SourceControls({ entry, status, confirmed, onConfirm, onUseCurrent, onOpenSource, onDelete }: SourceControlsProps): ReactElement | null {
  const { line } = entry.anchor
  switch (status.kind) {
    case 'checking':
    case 'ok':
      return null
    case 'error':
      return <div className="comment-caption">Source unreadable — the comment text is kept</div>
    case 'changed':
      return (
        <div className="send-dialog-source">
          <div className="comment-caption">
            {status.currentText === null ? `Line ${line} no longer exists` : `Line ${line} now reads: "${status.currentText}"`}
          </div>
          <div className="comment-actions">
            {status.currentText !== null && (
              <button type="button" className="btn" onClick={onUseCurrent}>
                Use current line {line}
              </button>
            )}
            <button type="button" className="btn" onClick={onOpenSource}>
              Open source
            </button>
            <label className="send-dialog-confirm">
              <input type="checkbox" checked={confirmed} onChange={(e) => onConfirm(e.target.checked)} />
              Send with the old quote
            </label>
          </div>
        </div>
      )
    case 'missing':
      return (
        <div className="send-dialog-source">
          <div className="comment-caption">The file no longer exists</div>
          <div className="comment-actions">
            <button type="button" className="btn" onClick={onDelete}>
              Delete
            </button>
            <label className="send-dialog-confirm">
              <input type="checkbox" checked={confirmed} onChange={(e) => onConfirm(e.target.checked)} />
              Send without a current quote
            </label>
          </div>
        </div>
      )
  }
}

// SendOperation — уже заморожённый пакет: повтор отправляет ровно его же под тем же ID, новый ввод остаётся следующей редакцией.
function SendOperation({ onClose }: { onClose: () => void }): ReactElement | null {
  const { send } = useComments()
  const options = useCommentRecipients()
  const { operation } = send
  if (operation === null) return null
  const label = options.find((o) => o.id === operation.recipientId)?.label ?? 'the chosen agent'
  const count = plural(operation.revisions.length, 'comment')
  return (
    <>
      <div className="send-dialog-body">
        <p className="send-dialog-summary">{send.pending ? `Sending ${count} → ${label}…` : `Not sent yet: ${count} → ${label}`}</p>
        {send.error !== null && (
          <p className="comment-error" role="alert">
            {send.error}
          </p>
        )}
        <pre className="send-dialog-packet">{operation.body}</pre>
      </div>
      <footer className="send-dialog-footer">
        <button type="button" className="btn btn-cancel" onClick={onClose}>
          Close
        </button>
        <button type="button" className="btn btn-cancel" disabled={send.pending} onClick={send.cancel}>
          Discard attempt
        </button>
        <button type="button" className="btn btn-send" disabled={send.pending} onClick={send.retry}>
          Retry
        </button>
      </footer>
    </>
  )
}

// FileCommentSendStatus — состояние отправки вне диалога (в футере Files): идёт запрос, не ушло, принято.
export function FileCommentSendStatus(): ReactElement | null {
  const { send } = useComments()
  const options = useCommentRecipients()
  const { open: openAssistant } = useSideAgentContext()
  const { operation, accepted } = send
  const labelOf = (id: string): string => options.find((o) => o.id === id)?.label ?? 'the agent'
  if (operation !== null) {
    const count = plural(operation.revisions.length, 'comment')
    return (
      <div className="file-comment-send-status" role="status">
        {send.pending ? `Sending ${count} → ${labelOf(operation.recipientId)}…` : `Not sent: ${send.error ?? 'request failed'}`}
      </div>
    )
  }
  if (accepted === null) return null
  return (
    <div className="file-comment-send-status" role="status">
      <span>{`Accepted: ${plural(accepted.count, 'comment')} → ${labelOf(accepted.recipientId)} (not read yet)`}</span>
      {accepted.recipientId.startsWith('side:') && (
        <button type="button" className="btn" onClick={openAssistant}>
          Open conversation
        </button>
      )}
      <button type="button" className="icon-btn" aria-label="Dismiss status" onClick={send.dismissAccepted}>
        ✕
      </button>
    </div>
  )
}
