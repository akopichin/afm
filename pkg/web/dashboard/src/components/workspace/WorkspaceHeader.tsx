import { type ReactElement, type ReactNode } from 'react'
import type { Stage } from '../../types'
import { STAGE_STATUS_LABELS } from '../../types'
import { QuotaCountdown } from '../stages-list/QuotaCountdown'
import { useStageAction } from '../../hooks/use-stage-retry'
import { continueStage, pauseStage, retryHookStage, retryStage } from '../../api/run-client'

const PAUSABLE_STATUSES: ReadonlySet<Stage['status']> = new Set(['running', 'planning', 'revising', 'retrying', 'awaiting_peer', 'awaiting_quota'])

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
  replyNeeded?: number
  controlsEnabled?: boolean
  showControls?: boolean
  onContinue?: () => void
}

// Иконки шапки — те же линейные SVG на currentColor, что в прототипе.
function icon(children: ReactElement): ReactElement {
  return (
    <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {children}
    </svg>
  )
}
const REPLY_ICON = icon(<path d="m9 5-6 6 6 6M3 11h11a6 6 0 0 1 6 6v3" />)
const REFRESH_ICON = icon(<path d="M20 11a8 8 0 1 0-2.3 5.7M20 4v7h-7" />)
const PAUSE_ICON = icon(<path d="M8 5v14M16 5v14" />)
const PLAY_ICON = icon(<path d="m8 4 12 8-12 8Z" />)

// WorkspaceHeader — компактный заголовок воркспейса выбранной стадии: имя + чип статуса (точка, слово,
// «thinking» во время running) и справа действия: ответ ждут, People, Retry, Pause/Continue иконкой.
export function WorkspaceHeader({ stage, connected, attentionNav, actions, statusAvailable = true, replyNeeded = 0, controlsEnabled = true, showControls = true, onContinue }: WorkspaceHeaderProps): ReactElement {
  const action = useStageAction(stage)
  const paused = stage.status === 'paused'
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
          {stage.status === 'running' && connected && (
            <span className="thinking" aria-hidden="true">
              <span className="td" />
              <span className="td" />
              <span className="td" />
              thinking
            </span>
          )}
        </span>
        {stage.status === 'awaiting_quota' && stage.quota !== undefined && <QuotaCountdown quota={stage.quota} live={statusAvailable} compact />}
      </span>
      {attentionNav !== undefined && (
        <span className="attention-nav" role="group" aria-label="Navigate items needing attention">
          <button type="button" className="attention-nav-btn" aria-label="Previous item" onClick={attentionNav.onPrev}>‹</button>
          <span className="attention-nav-pos">{attentionNav.pos} / {attentionNav.total}</span>
          <button type="button" className="attention-nav-btn" aria-label="Next item" onClick={attentionNav.onNext}>›</button>
        </span>
      )}
      <span className="workspace-header-actions">
        {replyNeeded > 0 && <span className="workspace-reply-needed" role="status">{REPLY_ICON}{replyNeeded} {replyNeeded === 1 ? 'reply' : 'replies'} needed</span>}
        {actions}
        {showControls && (stage.status === 'failed' || stage.status === 'hook_failed') && (
          <button type="button" className="workspace-action workspace-action-retry" disabled={action.locked || !statusAvailable || !controlsEnabled} onClick={() => void action.send(stage.status === 'hook_failed' ? retryHookStage : retryStage)}>{REFRESH_ICON}Retry</button>
        )}
        {showControls && (paused || (PAUSABLE_STATUSES.has(stage.status) && !(stage.isScript && stage.status === 'running'))) && (
          <button
            type="button"
            className="workspace-action workspace-action-icon"
            aria-label={paused ? 'Continue' : 'Pause'}
            title={paused ? 'Continue stage' : 'Pause stage'}
            disabled={action.locked || !statusAvailable || !controlsEnabled}
            onClick={() => paused && onContinue !== undefined ? onContinue() : void action.send(paused ? continueStage : pauseStage)}
          >
            {paused ? PLAY_ICON : PAUSE_ICON}
          </button>
        )}
      </span>
      {showControls && action.error !== null && <span className="workspace-action-error" role="alert">{action.error}</span>}
    </div>
  )
}
