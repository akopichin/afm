import type { ReactElement, ReactNode } from 'react'
import type { Stage } from '../../types'
import { STAGE_STATUS_LABELS } from '../../types'
import { QuotaCountdown } from '../stages-list/QuotaCountdown'

// AttentionNav — навигация prev/next между стадиями очереди attention (rule 6),
// показывается только когда таких стадий больше одной.
export type AttentionNav = {
  pos: number // 1-based позиция текущей стадии в очереди
  total: number
  onPrev: () => void
  onNext: () => void
}

type WorkspaceHeaderProps = {
  stage: Stage
  connected: boolean
  attentionNav?: AttentionNav
  // actions — the view's own buttons at the header's right end (the Feed's 👥 people toggle).
  actions?: ReactNode
  // statusAvailable — свежесть /api/status: пока он устарел, счётчик ожидания лимита замирает.
  statusAvailable?: boolean
}

// WorkspaceHeader — компактный заголовок воркспейса выбранной стадии: имя + статус-бейдж +
// «thinking»-индикатор во время running.
export function WorkspaceHeader({ stage, connected, attentionNav, actions, statusAvailable = true }: WorkspaceHeaderProps): ReactElement {
  return (
    <div className="workspace-header">
      <h2 id="detail-title" className="workspace-title">{stage.name !== '' ? stage.name : stage.id}</h2>
      <span className="status-badge-wrap">
        <span
          id="detail-status"
          className="status-badge"
          data-status={stage.status}
          data-blocked={stage.status === 'awaiting_assistants' && stage.awaitingAssistantsBlocked === true ? 'true' : undefined}
        >
          {STAGE_STATUS_LABELS[stage.status]}
        </span>
        {stage.status === 'awaiting_quota' && stage.quota !== undefined && <QuotaCountdown quota={stage.quota} live={statusAvailable} compact />}
        {stage.status === 'running' && connected && (
          <span className="thinking" aria-hidden="true">
            <span className="td" />
            <span className="td" />
            <span className="td" />
            thinking
          </span>
        )}
      </span>
      {attentionNav !== undefined && (
        <span className="attention-nav" role="group" aria-label="Navigate items needing attention">
          <button type="button" className="attention-nav-btn" aria-label="Previous item" onClick={attentionNav.onPrev}>‹</button>
          <span className="attention-nav-pos">{attentionNav.pos} / {attentionNav.total}</span>
          <button type="button" className="attention-nav-btn" aria-label="Next item" onClick={attentionNav.onNext}>›</button>
        </span>
      )}
      {actions !== undefined && <span className="workspace-header-actions">{actions}</span>}
    </div>
  )
}
