import { type ReactElement, type ReactNode } from 'react'
import type { Stage } from '../../types'
import { STAGE_STATUS_LABELS } from '../../types'
import { QuotaCountdown } from '../stages-list/QuotaCountdown'
import { useStageAction } from '../../hooks/use-stage-retry'
import { continueStage, pauseStage, retryHookStage, retryStage, triggerStageButton } from '../../api/run-client'
import type { AttentionKind } from '../../hooks/use-workspace-view'

const PAUSABLE_STATUSES: ReadonlySet<Stage['status']> = new Set(['running', 'planning', 'revising', 'retrying', 'awaiting_peer', 'awaiting_quota'])

// Кнопки из flow.yaml доставляются через Revise, поэтому нужен живой агент: running/awaiting_approval, не скрипт.
const BUTTON_STATUSES: ReadonlySet<Stage['status']> = new Set(['running', 'awaiting_approval'])

// AttentionNav — навигация prev/next между стадиями очереди attention (rule 6),
// показывается только когда таких стадий больше одной.
export type AttentionNav = {
  pos: number // 1-based позиция текущей стадии в очереди
  total: number
  onPrev: () => void
  onNext: () => void
}

// FeedFocusControl — the Focus / Exit focus button; passed only where focus mode is available.
export type FeedFocusControl = {
  active: boolean
  onToggle: () => void
  // target — what the mode is on: the Feed, or the Plan / Question view.
  target?: 'feed' | 'view'
  // onShowFeed — focus mode hides the tab strip, so a view other than the Feed brings the way back to the Feed.
  onShowFeed?: () => void
}

const FOCUS_NAMES = {
  feed: { enter: 'Focus on Feed', exit: 'Exit Feed focus' },
  view: { enter: 'Focus on this view', exit: 'Exit focus' },
}

// PendingAttention — the waiting item the beacon tab points at. Focus mode hides the tab strip, so the toolbar
// shows it as a button with the beacon's label, count and tone.
export type PendingAttention = {
  label: string
  kind: AttentionKind
  count: number
  onOpen: () => void
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
  onReplyJump?: () => void
  controlsEnabled?: boolean
  showControls?: boolean
  // showStageButtons — false, когда в воркспейсе выбран helper, а не сама стадия: кнопки принадлежат стадии.
  showStageButtons?: boolean
  onContinue?: () => void
  feedFocus?: FeedFocusControl
  pendingAttention?: PendingAttention
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
const EXPAND_ICON = icon(<path d="M8 3H3v5m13-5h5v5M3 16v5h5m13-5v5h-5" />)
const COLLAPSE_ICON = icon(<path d="M3 8h5V3m13 5h-5V3M8 21v-5H3m13 5v-5h5" />)

// WorkspaceHeader — компактный заголовок воркспейса выбранной стадии: имя + чип статуса (точка, слово,
// «thinking» во время running) и справа действия: ответ ждут, People, ожидание (в фокусе), Retry,
// Pause/Continue иконкой, Focus.
export function WorkspaceHeader({ stage, connected, attentionNav, actions, statusAvailable = true, replyNeeded = 0, onReplyJump, controlsEnabled = true, showControls = true, showStageButtons = true, onContinue, feedFocus, pendingAttention }: WorkspaceHeaderProps): ReactElement {
  const action = useStageAction(stage)
  const paused = stage.status === 'paused'
  const buttonsShown = showStageButtons && !stage.isScript && BUTTON_STATUSES.has(stage.status) && stage.buttons.length > 0
  return (
    <div className="workspace-header">
      <h2 id="detail-title" className="workspace-title" title={stage.name !== '' ? stage.name : undefined}>{stage.id}</h2>
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
      {buttonsShown && (
        <span className="stage-buttons" role="group" aria-label="Stage buttons">
          {stage.buttons.map((label) => (
            <button
              key={label}
              type="button"
              className="stage-button"
              disabled={action.locked || !statusAvailable || !controlsEnabled}
              onClick={() => void action.send((stageId) => triggerStageButton(stageId, label))}
            >
              {label}
            </button>
          ))}
        </span>
      )}
      {attentionNav !== undefined && (
        <span className="attention-nav" role="group" aria-label="Navigate items needing attention">
          <button type="button" className="attention-nav-btn" aria-label="Previous item" onClick={attentionNav.onPrev}>‹</button>
          <span className="attention-nav-pos">{attentionNav.pos} / {attentionNav.total}</span>
          <button type="button" className="attention-nav-btn" aria-label="Next item" onClick={attentionNav.onNext}>›</button>
        </span>
      )}
      <span className="workspace-header-actions">
        {replyNeeded > 0 && <span className="workspace-reply-needed" role="status">
          <button type="button" className="workspace-reply-needed-btn" aria-label="Go to the question awaiting your reply" onClick={onReplyJump}>{REPLY_ICON}{replyNeeded} {replyNeeded === 1 ? 'reply' : 'replies'} needed</button>
        </span>}
        {actions}
        {feedFocus?.active === true && pendingAttention !== undefined && (
          <button type="button" className="workspace-action feed-focus-wait" data-kind={pendingAttention.kind} onClick={pendingAttention.onOpen}>
            <span className="feed-focus-wait-label">{pendingAttention.label}</span>
            {pendingAttention.count > 0 && <span className="feed-focus-wait-count">{pendingAttention.count}</span>}
          </button>
        )}
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
        {feedFocus?.active === true && feedFocus.onShowFeed !== undefined && (
          <button type="button" className="workspace-action feed-focus-feed" onClick={feedFocus.onShowFeed}>Feed</button>
        )}
        {feedFocus !== undefined && (
          <button
            type="button"
            className="workspace-action feed-focus-toggle"
            aria-pressed={feedFocus.active}
            aria-label={feedFocus.active ? FOCUS_NAMES[feedFocus.target ?? 'feed'].exit : FOCUS_NAMES[feedFocus.target ?? 'feed'].enter}
            aria-keyshortcuts={feedFocus.active ? 'F Escape' : 'F'}
            title={`${feedFocus.active ? FOCUS_NAMES[feedFocus.target ?? 'feed'].exit : FOCUS_NAMES[feedFocus.target ?? 'feed'].enter} · ${feedFocus.active ? 'F or Esc' : 'F'}`}
            onClick={feedFocus.onToggle}
          >
            {feedFocus.active ? COLLAPSE_ICON : EXPAND_ICON}
            <span className="feed-focus-toggle-label">{feedFocus.active ? 'Exit focus' : 'Focus'}</span>
            {feedFocus.active && <kbd>Esc</kbd>}
          </button>
        )}
      </span>
      {(showControls || buttonsShown) && action.error !== null && <span className="workspace-action-error" role="alert">{action.error}</span>}
    </div>
  )
}
