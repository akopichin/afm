import { useEffect, useState, type ReactElement, type ReactNode } from 'react'
import { approveStage, retryHookStage, retryStage, reviseStage, skipHookStage } from '../../api/run-client'
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'
import type { Stage } from '../../types'
import { Maximizable } from '../layout/Maximizable'
import { PanelFrame } from '../panel-frame/PanelFrame'
import { isHeading2, isSpecialSection, nextLineBlock, renderMarkdown, type SpecialSection } from './markdown'

type PlanPanelProps = {
  stage: Stage | null
  attention?: boolean
}

type LineItem = { kind: 'line'; line: number; html: string }
type SectionItem = { kind: 'section'; section: SpecialSection; body: LineItem[] }
type ReviewItem = LineItem | SectionItem

// Панель плана стадии: загрузка markdown, рендер (обычный или review с номерами строк
// и комментариями), действия Approve/Send revision/Retry. Поведение перенесено из
// loadPlan / renderPlanReview / onPlanLineClick в текущем app.js.
export function PlanPanel({ stage, attention = false }: PlanPanelProps): ReactElement {
  const [planMarkdown, setPlanMarkdown] = useState('')
  const [comments, setComments] = useState<Record<number, string>>({})
  const [activeCommentLine, setActiveCommentLine] = useState<number | null>(null)
  const [draft, setDraft] = useState('')
  const commentTextareaRef = useAutoGrowTextarea(draft, 400)
  const [collapsedSections, setCollapsedSections] = useState<Set<number>>(new Set())
  const [busy, setBusy] = useState(false)
  const [clicked, setClicked] = useState<'approve' | 'revise' | 'retry' | null>(null)

  const isReview = stage?.status === 'awaiting_approval'
  const showActions = isReview && stage !== null && !stage.autoApprove
  const showAutoApprovedBadge = stage !== null && stage.autoApprove && planMarkdown.trim() !== ''
  const showRetry = stage?.status === 'failed'
  const showHookFailed = stage?.status === 'hook_failed'
  const commentCount = Object.keys(comments).length

  function flashButton(which: 'approve' | 'revise' | 'retry') {
    setClicked(which)
    window.setTimeout(() => setClicked(null), 1200)
  }

  useEffect(() => {
    // stage === null: панель смонтирована для стабильности лейаута, но
    // реальной стадии нет — не гоняем пустой fetch GET /api/stages//plan.
    const current = stage
    if (current === null) return

    let cancelled = false

    setComments({})
    setActiveCommentLine(null)
    setPlanMarkdown('')

    async function loadPlan(stageId: string, stageDone: boolean) {
      let response: Response
      try {
        response = await fetch(`/api/stages/${encodeURIComponent(stageId)}/plan`)
      } catch {
        return
      }

      if (!response.ok || cancelled) return

      const text = await response.text()
      if (cancelled) return

      const finalText = stageDone ? text.replace(/- \[ \]/g, '- [x]') : text
      setPlanMarkdown(finalText)
    }

    void loadPlan(current.id, current.status === 'done')

    return () => {
      cancelled = true
    }
  }, [stage?.id, stage?.status])

  function handleLineClick(line: number) {
    if (activeCommentLine !== null && draft.trim() !== '') return

    if (activeCommentLine === line) {
      setActiveCommentLine(null)
      return
    }

    setActiveCommentLine(line)
    setDraft(comments[line] ?? '')
  }

  function closeCommentForm() {
    setActiveCommentLine(null)
    setDraft('')
  }

  function saveComment(line: number) {
    const text = draft.trim()
    setComments((prev) => {
      const next = { ...prev }
      if (text === '') {
        delete next[line]
      } else {
        next[line] = text
      }
      return next
    })
    setActiveCommentLine(null)
  }

  function deleteComment(line: number) {
    setComments((prev) => {
      const next = { ...prev }
      delete next[line]
      return next
    })
    setActiveCommentLine(null)
  }

  function toggleSection(index: number) {
    setCollapsedSections((prev) => {
      const next = new Set(prev)
      if (next.has(index)) {
        next.delete(index)
      } else {
        next.add(index)
      }
      return next
    })
  }

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

  async function sendRevision() {
    if (stage === null) return
    flashButton('revise')
    const feedback = buildFeedback(comments)
    if (feedback === '') return

    setBusy(true)
    try {
      await reviseStage(stage.id, feedback)
      setComments({})
      setActiveCommentLine(null)
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

  return (
    <Maximizable id="plan">
      <PanelFrame title="Plan" maximizeId="plan" attention={attention}>
        <div id="plan-section" className="section">
          <div id="plan-content" className="markdown-body">
            {renderPlanBody()}
          </div>
          <div id="plan-empty" className={`empty-hint${planMarkdown.trim() !== '' ? ' hidden' : ''}`}>No plan yet</div>
        </div>

        {showActions && (
          <div id="actions-section" className="section">
            <div className="actions-row">
              <button
                id="btn-approve"
                className={`btn btn-approve${clicked === 'approve' ? ' ok' : ''}`}
                type="button"
                disabled={busy || commentCount > 0 || (activeCommentLine !== null && draft.trim() !== '')}
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
                disabled={busy || commentCount === 0}
                onClick={sendRevision}
              >
                <span className="btn-ripple" aria-hidden="true" />
                <span className="btn-label">{commentCount > 0 ? `Send revision (${commentCount})` : 'Send revision'}</span>
                <span className="btn-done" aria-hidden="true">✓ Sent</span>
              </button>
            </div>
            <div id="comment-hint" className="comment-hint">Click a plan line to comment</div>
          </div>
        )}

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
      </PanelFrame>
    </Maximizable>
  )

  function renderPlanBody(): ReactNode {
    if (planMarkdown.trim() === '') return null

    if (isReview) {
      return parseReviewPlan(planMarkdown).map((item, index) => renderReviewItem(item, index))
    }

    return <div className="md" dangerouslySetInnerHTML={{ __html: renderMarkdown(planMarkdown) }} />
  }

  function renderReviewItem(item: ReviewItem, sectionIndex: number): ReactNode {
    if (item.kind === 'section') {
      const collapsed = collapsedSections.has(sectionIndex)
      return (
        <div key={`section-${sectionIndex}`} className={`plan-section-wrapper ${item.section.css}${collapsed ? ' collapsed' : ''}`}>
          <h2 className="section-header" onClick={() => toggleSection(sectionIndex)}>
            {item.section.icon} {item.section.label} <span className="toggle">▾</span>
          </h2>
          <div className="plan-section-body">
            {item.body.map((child, childIndex) => renderPlanLine(child, `section-${sectionIndex}-${childIndex}`))}
          </div>
        </div>
      )
    }

    return renderPlanLine(item, `line-${sectionIndex}`)
  }

  function renderCommentHeader(label: string, ariaLabel: string, title: string, onClick: () => void): ReactNode {
    return (
      <div className="comment-display-header">
        <span style={{ color: 'var(--c-awaiting)', fontSize: '12px' }}>{label}</span>
        <button type="button" className="comment-remove" aria-label={ariaLabel} title={title} onClick={onClick}>
          ✕
        </button>
      </div>
    )
  }

  function renderPlanLine(item: LineItem, key: string): ReactNode {
    const hasComment = comments[item.line] !== undefined

    return (
      <div
        key={key}
        className={`plan-line${hasComment ? ' has-comment' : ''}`}
        data-line={item.line}
        data-line-end={item.line}
        onClick={() => handleLineClick(item.line)}
      >
        <span className="line-num">{item.line}</span>
        <span className="line-content" dangerouslySetInnerHTML={{ __html: item.html }} />
        <span className="line-comment-marker">●</span>

        {hasComment && (
          <div className="line-comment-form line-comment-display" onClick={(event) => event.stopPropagation()}>
            {renderCommentHeader(`Comment on line ${item.line}`, `Remove comment on line ${item.line}`, 'Remove comment', () => deleteComment(item.line))}
            <div style={{ color: 'var(--text)', whiteSpace: 'pre-wrap' }}>{comments[item.line]}</div>
          </div>
        )}

        {activeCommentLine === item.line && (
          <div className="line-comment-form" onClick={(event) => event.stopPropagation()}>
            {renderCommentHeader(`Comment on line ${item.line}`, `Close comment on line ${item.line}`, 'Close', closeCommentForm)}
            <textarea
              ref={commentTextareaRef}
              placeholder={`Comment on line ${item.line}...`}
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
            />
            <div className="comment-actions">
              <button className="btn btn-send" type="button" onClick={() => saveComment(item.line)}>
                {hasComment ? 'Update' : 'Add'}
              </button>
              {hasComment && (
                <button className="btn btn-cancel" type="button" onClick={() => deleteComment(item.line)}>
                  Delete
                </button>
              )}
            </div>
          </div>
        )}
      </div>
    )
  }
}

function parseReviewPlan(text: string): ReviewItem[] {
  const lines = text.split('\n')
  const items: ReviewItem[] = []
  let currentSection: SectionItem | null = null

  const pushLeaf = (item: LineItem) => {
    if (currentSection !== null) {
      currentSection.body.push(item)
    } else {
      items.push(item)
    }
  }

  for (let i = 0; i < lines.length; ) {
    const line = lines[i] ?? ''

    // Спецсекция (## Assumptions / ## Acceptance Criteria) — заголовок в одну
    // строку; открывает сворачиваемую обёртку.
    const section = isSpecialSection(line)
    if (section !== null) {
      if (currentSection !== null) {
        items.push(currentSection)
      }
      currentSection = { kind: 'section', section, body: [] }
      i++
      continue
    }

    // Любой другой заголовок ## закрывает открытую спецсекцию (сам заголовок
    // всё равно отрендерится ниже как обычная строка).
    if (currentSection !== null && isHeading2(line)) {
      items.push(currentSection)
      currentSection = null
    }

    // Обычная строка ИЛИ схлопнутый блок (fenced-код / таблица) — nextLineBlock
    // сам решает, сколько строк поглотить, и якорит блок на первой строке.
    const { block, next } = nextLineBlock(lines, i)
    pushLeaf({ kind: 'line', line: block.line, html: block.html })
    i = next
  }

  if (currentSection !== null) {
    items.push(currentSection)
  }

  return items
}

function buildFeedback(comments: Record<number, string>): string {
  return Object.keys(comments)
    .map(Number)
    .sort((a, b) => a - b)
    .map((line) => `Line ${line}: ${comments[line]}`)
    .join('\n\n')
}
