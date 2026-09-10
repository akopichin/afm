import type { ReactElement } from 'react'
import type { Stage } from '../../types'
import { STAGE_STATUS_LABELS } from '../../types'

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
}

// WorkspaceHeader — компактный заголовок воркспейса выбранной стадии: имя +
// статус-бейдж + «thinking»-индикатор во время running. Пришёл на смену
// detail-header из DashboardLayout; вращающийся ornament (novacorps-декор)
// намеренно убран (план §5.2/§17 — спокойная state-специфичная анимация вместо
// постоянного декоративного вращения). id detail-title/detail-status и класс
// status-badge сохранены для совместимости со скинами и проверками.
export function WorkspaceHeader({ stage, connected, attentionNav }: WorkspaceHeaderProps): ReactElement {
  return (
    <div className="workspace-header">
      <h2 id="detail-title" className="workspace-title">{stage.name !== '' ? stage.name : stage.id}</h2>
      <span className="status-badge-wrap">
        <span id="detail-status" className="status-badge" data-status={stage.status}>
          {STAGE_STATUS_LABELS[stage.status]}
        </span>
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
    </div>
  )
}
