import { useEffect, useState, type ReactElement, type ReactNode } from 'react'
import { approveStage, continueStage, retryHookStage, retryStage, reviseStage, skipHookStage } from '../../api/run-client'
import { useMessageTimeZone } from '../../lib/message-time-zone'
import { quotaTitle, type Stage } from '../../types'
import { Maximizable } from '../layout/Maximizable'
import { PanelFrame } from '../panel-frame/PanelFrame'
import { renderMarkdown } from './markdown'
import { LineCommentedDocument, type LineCommentDocumentApi } from './LineCommentedDocument'
import { QuotaCountdown, quotaPausedReasonText } from '../stages-list/QuotaCountdown'

// EMPTY_DOC — zero-state владельца для случая «review, но плана нет»: панель
// действий (Approve/Send revision) должна остаться доступной без комментариев.
const EMPTY_DOC: LineCommentDocumentApi = {
  body: null,
  comments: {},
  commentCount: 0,
  activeCommentLine: null,
  draft: '',
  hasOpenDraft: false,
  clearComments: () => {},
}

type PlanPanelProps = {
  stage: Stage | null
  attention?: boolean
  // banner — attention-шапка, рендерится ПЕРВЫМ ребёнком внутри скролл-области #plan-section, а не над
  // панелью.
  banner?: ReactNode
  // statusAvailable — свежесть /api/status из useStatus: пока он устарел, счётчик ожидания лимита замирает.
  statusAvailable?: boolean
}

// Панель плана стадии: загрузка markdown, рендер, действия Approve/Send revision/Retry.
export function PlanPanel({ stage, attention = false, banner, statusAvailable = true }: PlanPanelProps): ReactElement {
  const stageId = stage?.id ?? ''
  const hostZone = useMessageTimeZone()
  const [planMarkdown, setPlanMarkdown] = useState('')
  // planLoaded — попытка загрузки плана ЗАВЕРШИЛАСЬ, не «идёт».
  const [planLoaded, setPlanLoaded] = useState(false)
  const [busy, setBusy] = useState(false)
  const [clicked, setClicked] = useState<'approve' | 'revise' | 'retry' | 'continue' | null>(null)

  const isReview = stage?.status === 'awaiting_approval'
  // commentable — построчные комментарии + Approve/Send revision. Только когда
  // план реально ждёт ручного одобрения (не auto-approve).
  const commentable = isReview && stage !== null && !stage.autoApprove
  const showAutoApprovedBadge = stage !== null && stage.autoApprove && planMarkdown.trim() !== ''
  const showRetry = stage?.status === 'failed'
  const showHookFailed = stage?.status === 'hook_failed'
  const showPaused = stage?.status === 'paused'
  const showQuotaWait = stage?.status === 'awaiting_quota'
  const pausedFromQuota = showPaused && stage?.pausedFrom === 'awaiting_quota'
  const planEmpty = planMarkdown.trim() === ''

  function flashButton(which: 'approve' | 'revise' | 'retry' | 'continue') {
    setClicked(which)
    window.setTimeout(() => setClicked(null), 1200)
  }

  useEffect(() => {
    // stage === null: панель смонтирована для стабильности лейаута, но реальной стадии нет — не гоняем
    // пустой fetch GET /api/stages//plan.
    const current = stage
    if (current === null || current.isScript) return

    let cancelled = false

    setPlanMarkdown('')
    setPlanLoaded(false)

    async function loadPlan(stageId: string, stageDone: boolean) {
      let response: Response
      try {
        response = await fetch(`/api/stages/${encodeURIComponent(stageId)}/plan`)
      } catch {
        // Сетевая ошибка — попытка завершена (план пуст), но review-действия
        // должны остаться доступны (codex): помечаем loaded.
        if (!cancelled) setPlanLoaded(true)
        return
      }

      if (cancelled) return
      if (!response.ok) {
        setPlanLoaded(true)
        return
      }

      const text = await response.text()
      if (cancelled) return

      const finalText = stageDone ? text.replace(/- \[ \]/g, '- [x]') : text
      setPlanMarkdown(finalText)
      setPlanLoaded(true)
    }

    void loadPlan(current.id, current.status === 'done')

    return () => {
      cancelled = true
    }
  }, [stage?.id, stage?.status])

  async function approve() {
    if (stage === null) return
    flashButton('approve')
    setBusy(true)
    try {
      await approveStage(stage.id)
    } finally {
      setBusy(false)
    }
  }

  async function sendRevision(comments: Record<number, string>, clearComments: () => void) {
    if (stage === null) return
    flashButton('revise')
    const feedback = buildFeedback(comments)
    if (feedback === '') return

    setBusy(true)
    try {
      await reviseStage(stage.id, feedback)
      clearComments()
    } catch (err) {
      // Стадия могла уйти из awaiting_approval за время правки, либо сеть отвалилась — не теряем набранные
      // комментарии и логируем, чтобы отказ не ушёл в unhandled rejection.
      console.error('Failed to send plan revision:', err)
    } finally {
      setBusy(false)
    }
  }

  async function retry() {
    if (stage === null) return
    flashButton('retry')
    setBusy(true)
    try {
      await retryStage(stage.id)
    } finally {
      setBusy(false)
    }
  }

  async function doContinue() {
    if (stage === null) return
    flashButton('continue')
    setBusy(true)
    try {
      await continueStage(stage.id)
    } finally {
      setBusy(false)
    }
  }

  async function retryHook() {
    if (stage === null) return
    flashButton('retry')
    setBusy(true)
    try {
      await retryHookStage(stage.id)
    } finally {
      setBusy(false)
    }
  }

  async function skipHook() {
    if (stage === null) return
    flashButton('revise') // reuse the 'revise'-style flash slot; no dedicated 'skip' clicked-state needed
    setBusy(true)
    try {
      await skipHookStage(stage.id)
    } finally {
      setBusy(false)
    }
  }

  const emptyHint = <div id="plan-empty" className={`empty-hint${!planEmpty ? ' hidden' : ''}`}>No plan yet</div>

  function renderActions(doc: LineCommentDocumentApi): ReactNode {
    return (
      <div id="actions-section" className="section">
        <div className="actions-row">
          <button
            id="btn-approve"
            className={`btn btn-approve${clicked === 'approve' ? ' ok' : ''}`}
            type="button"
            disabled={busy || doc.commentCount > 0 || doc.activeCommentLine !== null}
            onClick={approve}
          >
            <span className="btn-ripple" aria-hidden="true" />
            <span className="btn-label">Approve</span>
            <span className="btn-done" aria-hidden="true">✓ Approved</span>
          </button>
          <button
            id="btn-revise"
            className={`btn btn-revise${clicked === 'revise' ? ' ok' : ''}`}
            type="button"
            disabled={busy || doc.commentCount === 0 || doc.activeCommentLine !== null}
            onClick={() => void sendRevision(doc.comments, doc.clearComments)}
          >
            <span className="btn-ripple" aria-hidden="true" />
            <span className="btn-label">{doc.commentCount > 0 ? `Send revision (${doc.commentCount})` : 'Send revision'}</span>
            <span className="btn-done" aria-hidden="true">✓ Sent</span>
          </button>
        </div>
        <div id="comment-hint" className="comment-hint">Click a plan line to comment</div>
      </div>
    )
  }

  return (
    <Maximizable id="plan">
      <PanelFrame title="Plan" maximizeId="plan" attention={attention}>
        {commentable && !planEmpty ? (
          <LineCommentedDocument
            key={`${stageId}:${planMarkdown}`}
            text={planMarkdown}
            specialSections
            stageId={stageId}
            bodyId="plan-content"
            bodyClassName="markdown-body line-commented"
          >
            {(doc) => (
              <>
                <div id="plan-section" className="section">
                  {banner}
                  {doc.body}
                  {emptyHint}
                </div>
                {renderActions(doc)}
              </>
            )}
          </LineCommentedDocument>
        ) : (
          <>
            <div id="plan-section" className="section">
              {banner}
              <div id="plan-content" className="markdown-body">
                {!planEmpty && <div className="md" dangerouslySetInnerHTML={{ __html: renderMarkdown(planMarkdown) }} />}
              </div>
              {emptyHint}
            </div>

            {/* Стадия в review, а план так и не загрузился (пустой/404/сетевая
                ошибка — planLoaded после оседания fetch): всё равно показываем
                Approve/Send revision — иначе стадию нельзя было бы одобрить из
                панели до перезагрузки (регресс, codex). Гейт на planLoaded, чтобы
                панель не мигала в окне загрузки НОРМАЛЬНОГО плана. Комментариев
                нет — zero-state doc: Approve активен, Send revision заблокирован. */}
            {commentable && planLoaded && renderActions(EMPTY_DOC)}

            {showAutoApprovedBadge && (
              <div id="auto-approved-section" className="section">
                <span className="auto-approved-badge">Auto-approved</span>
              </div>
            )}

            {showRetry && (
              <div id="retry-section" className="section">
                <div className="actions-row">
                  <button id="btn-retry" className={`btn btn-retry${clicked === 'retry' ? ' ok' : ''}`} type="button" disabled={busy} onClick={retry}>
                    <span className="btn-ripple" aria-hidden="true" />
                    <span className="btn-label">Retry</span>
                    <span className="btn-done" aria-hidden="true">✓</span>
                  </button>
                </div>
              </div>
            )}

            {showQuotaWait && (
              <div id="quota-section" className="section">
                {stage.quota !== undefined ? <QuotaCountdown quota={stage.quota} live={statusAvailable} /> : <p className="quota-title">Usage limit</p>}
                <p className="quota-hint">
                  The stage resumes by itself when the limit resets. To retry sooner, pause the stage from its menu in the
                  stage list, then press Continue.
                </p>
              </div>
            )}

            {showPaused && (
              <div id="paused-section" className="section">
                {pausedFromQuota && <p className="quota-title">{quotaTitle(stage.quota)}</p>}
                <p className="paused-reason">
                  {pausedFromQuota ? quotaPausedReasonText(stage.quota, hostZone, 'stage') : pausedReasonText(stage?.pausedFrom ?? '')}
                </p>
                {pausedFromQuota && (
                  <p className="quota-hint">Continue tries once, right now, with a fresh session; if the limit is still active, the stage waits again.</p>
                )}
                <div className="actions-row">
                  <button id="btn-continue" className={`btn btn-approve${clicked === 'continue' ? ' ok' : ''}`} type="button" disabled={busy} onClick={doContinue}>
                    <span className="btn-ripple" aria-hidden="true" />
                    <span className="btn-label">Continue</span>
                    <span className="btn-done" aria-hidden="true">✓</span>
                  </button>
                </div>
              </div>
            )}

            {showHookFailed && (
              <div id="hook-failed-section" className="section">
                <div className="actions-row">
                  <button id="btn-retry-hook" className="btn btn-retry" type="button" disabled={busy} onClick={retryHook}>
                    <span className="btn-ripple" aria-hidden="true" />
                    <span className="btn-label">Retry</span>
                    <span className="btn-done" aria-hidden="true">✓</span>
                  </button>
                  <button id="btn-skip-hook" className="btn btn-revise" type="button" disabled={busy} onClick={skipHook}>
                    <span className="btn-ripple" aria-hidden="true" />
                    <span className="btn-label">Skip</span>
                    <span className="btn-done" aria-hidden="true">✓</span>
                  </button>
                </div>
              </div>
            )}
          </>
        )}
      </PanelFrame>
    </Maximizable>
  )
}

function pausedReasonText(pausedFrom: Stage['pausedFrom']): string {
  switch (pausedFrom) {
    case 'pending':
      return 'This stage is paused before its first run (auto_run: false). Click Continue to start it.'
    case 'retrying':
      return 'This stage was paused while waiting to retry.'
    case 'planning':
      return 'This stage was manually paused while it was planning.'
    case 'revising':
      return 'This stage was manually paused while it was revising.'
    default:
      return 'This stage was manually paused while it was running.'
  }
}

function buildFeedback(comments: Record<number, string>): string {
  return Object.keys(comments)
    .map(Number)
    .sort((a, b) => a - b)
    .map((line) => `Line ${line}: ${comments[line]}`)
    .join('\n\n')
}
