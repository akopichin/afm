import type { ReactElement } from 'react'
import type { Stage } from '../../types'
import { STAGE_STATUS_LABELS } from '../../types'

type WorkspaceHeaderProps = {
  stage: Stage
  connected: boolean
}

// WorkspaceHeader — компактный заголовок воркспейса выбранной стадии: имя +
// статус-бейдж + «thinking»-индикатор во время running. Пришёл на смену
// detail-header из DashboardLayout; вращающийся ornament (novacorps-декор)
// намеренно убран (план §5.2/§17 — спокойная state-специфичная анимация вместо
// постоянного декоративного вращения). id detail-title/detail-status и класс
// status-badge сохранены для совместимости со скинами и проверками.
export function WorkspaceHeader({ stage, connected }: WorkspaceHeaderProps): ReactElement {
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
    </div>
  )
}
