import { useEffect, useState, type ReactElement, type ReactNode } from 'react'
import type { Stage } from '../../types'
import { Maximizable } from '../layout/Maximizable'
import { PanelFrame } from '../panel-frame/PanelFrame'
import { escapeHtml, formatLine, isHeading2, isSpecialSection, renderMarkdown, type SpecialSection } from './markdown'

type PlanPanelProps = {
  stage: Stage
  attention?: boolean
}

type LineItem = { kind: 'line'; line: number; html: string }
type CodeItem = { kind: 'code'; line: number; html: string }
type SectionItem = { kind: 'section'; section: SpecialSection; body: Array<LineItem | CodeItem> }
type ReviewItem = LineItem | CodeItem | SectionItem

// Панель плана стадии: загрузка markdown, рендер (обычный или review с номерами строк
// и комментариями), действия Approve/Send revision/Retry. Поведение перенесено из
// loadPlan / renderPlanReview / onPlanLineClick в текущем app.js.
export function PlanPanel({ stage, attention = false }: PlanPanelProps): ReactElement {
  const [planMarkdown, setPlanMarkdown] = useState('')
  const [comments, setComments] = useState<Record<number, string>>({})
  const [activeCommentLine, setActiveCommentLine] = useState<number | null>(null)
  const [draft, setDraft] = useState('')
  const [collapsedSections, setCollapsedSections] = useState<Set<number>>(new Set())
  const [busy, setBusy] = useState(false)

  const isReview = stage.status === 'awaiting_approval'
  const showActions = isReview
  const showRetry = stage.status === 'failed'
  const commentCount = Object.keys(comments).length

  useEffect(() => {
    // NO_STAGE sentinel: панель смонтирована для стабильности лейаута, но
    // реальной стадии нет — не гоняем пустой fetch GET /api/stages//plan.
    if (stage.id === '') return

    let cancelled = false

    setComments({})
    setActiveCommentLine(null)
    setPlanMarkdown('')

    async function loadPlan() {
      let response: Response
      try {
        response = await fetch(`/api/stages/${encodeURIComponent(stage.id)}/plan`)
      } catch {
        return
      }

      if (!response.ok || cancelled) return

      const text = await response.text()
      if (cancelled) return

      const finalText = stage.status === 'done' ? text.replace(/- \[ \]/g, '- [x]') : text
      setPlanMarkdown(finalText)
    }

    void loadPlan()

    return () => {
      cancelled = true
    }
  }, [stage.id, stage.status])

  function handleLineClick(line: number) {
    if (activeCommentLine === line) {
      setActiveCommentLine(null)
      return
    }

    setActiveCommentLine(line)
    setDraft(comments[line] ?? '')
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
    setBusy(true)
    try {
      await postJson(`/api/stages/${encodeURIComponent(stage.id)}/approve`, null)
    } finally {
      setBusy(false)
    }
  }

  async function sendRevision() {
    const feedback = buildFeedback(comments)
    if (feedback === '') return

    setBusy(true)
    try {
      await postJson(`/api/stages/${encodeURIComponent(stage.id)}/revise`, { feedback })
      setComments({})
      setActiveCommentLine(null)
    } finally {
      setBusy(false)
    }
  }

  async function retry() {
    setBusy(true)
    try {
      await postJson(`/api/stages/${encodeURIComponent(stage.id)}/retry`, null)
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
              <button id="btn-approve" className="btn btn-approve" type="button" disabled={busy} onClick={approve}>
                Approve
              </button>
              <button
                id="btn-revise"
                className="btn btn-revise"
                type="button"
                disabled={busy || commentCount === 0}
                onClick={sendRevision}
              >
                {commentCount > 0 ? `Send revision (${commentCount})` : 'Send revision'}
              </button>
            </div>
            <div id="comment-hint" className="comment-hint">Click a plan line to comment</div>
          </div>
        )}

        {showRetry && (
          <div id="retry-section" className="section">
            <div className="actions-row">
              <button id="btn-retry" className="btn btn-retry" type="button" disabled={busy} onClick={retry}>
                Retry
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

  function renderPlanLine(item: LineItem | CodeItem, key: string): ReactNode {
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
            <div className="comment-display-header">
              <span style={{ color: 'var(--c-awaiting)', fontSize: '12px' }}>{`Comment on line ${item.line}`}</span>
              <button
                type="button"
                className="comment-remove"
                aria-label={`Remove comment on line ${item.line}`}
                title="Remove comment"
                onClick={() => deleteComment(item.line)}
              >
                ✕
              </button>
            </div>
            <div style={{ color: 'var(--text)', whiteSpace: 'pre-wrap' }}>{comments[item.line]}</div>
          </div>
        )}

        {activeCommentLine === item.line && (
          <div className="line-comment-form" onClick={(event) => event.stopPropagation()}>
            <textarea placeholder={`Comment on line ${item.line}...`} value={draft} onChange={(event) => setDraft(event.target.value)} />
            <div className="comment-actions">
              <button className="btn btn-send" type="button" onClick={() => saveComment(item.line)}>
                {hasComment ? 'Update' : 'Add'}
              </button>
              {hasComment && (
                <button className="btn btn-cancel" type="button" onClick={() => deleteComment(item.line)}>
                  Delete
                </button>
              )}
              <button className="btn btn-cancel" type="button" onClick={() => setActiveCommentLine(null)}>
                Cancel
              </button>
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
  let inCode = false
  let codeStart = 0
  let codeLines: string[] = []

  const pushLeaf = (item: LineItem | CodeItem) => {
    if (currentSection !== null) {
      currentSection.body.push(item)
    } else {
      items.push(item)
    }
  }

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    if (line === undefined) continue
    const lineNum = i + 1

    if (line.trim().startsWith('```')) {
      if (!inCode) {
        inCode = true
        codeStart = i
        codeLines = [line]
        continue
      }

      inCode = false
      codeLines.push(line)
      pushLeaf({ kind: 'code', line: codeStart + 1, html: `<pre><code>${escapeHtml(codeLines.join('\n').trim())}</code></pre>` })
      continue
    }

    if (inCode) {
      codeLines.push(line)
      continue
    }

    const section = isSpecialSection(line)
    if (section !== null) {
      if (currentSection !== null) {
        items.push(currentSection)
        currentSection = null
      }

      currentSection = { kind: 'section', section, body: [] }
      continue
    }

    if (currentSection !== null && isHeading2(line)) {
      items.push(currentSection)
      currentSection = null
    }

    pushLeaf({ kind: 'line', line: lineNum, html: formatLine(line) })
  }

  if (currentSection !== null) {
    items.push(currentSection)
  }

  if (inCode && codeLines.length > 0) {
    items.push({ kind: 'code', line: codeStart + 1, html: `<pre><code>${escapeHtml(codeLines.join('\n'))}</code></pre>` })
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

async function postJson(url: string, body: unknown): Promise<void> {
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: body === null ? null : JSON.stringify(body),
  })

  if (!response.ok) {
    throw new Error(`POST ${url} -> ${response.status}`)
  }
}
