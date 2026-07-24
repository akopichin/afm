import type { ReactElement } from 'react'
import type { Stage } from '../../types'
import { ATTENTION_STATUSES } from '../../hooks/use-attention'

type StagesListProps = {
  stages: Stage[]
  selectedStageId: string | null
  onSelect: (stageId: string) => void
}

// Левая панель: список стадий с выбором активной. Разметка и классы (stage-item,
// active, status-dot, stage-label, stage-id, stage-name, dialog-badge) совпадают с
// renderStages в текущем app.js — селекторы тем работают без изменений.
// На стадиях, ожидающих действия пользователя, ставим data-attention='true' —
// CSS анимирует их (attention-пульс в --amber).
export function StagesList({ stages, selectedStageId, onSelect }: StagesListProps): ReactElement {
  return (
    <aside id="stages-panel">
      <h2>Stages</h2>
      <ul id="stages-list" className="stages-list">
        {stages.map((stage) => (
          <li
            key={stage.id}
            className={`stage-item${stage.id === selectedStageId ? ' active' : ''}`}
            data-stage-id={stage.id}
            data-status={stage.status}
            data-attention={ATTENTION_STATUSES.has(stage.status) ? 'true' : undefined}
            onClick={() => onSelect(stage.id)}
          >
            <span className="status-dot" data-status={stage.status} />
            <span className="stage-label">
              <span className="stage-id">{stage.id}</span>
              {stage.name !== '' && <span className="stage-name">{stage.name}</span>}
            </span>
            {stage.status === 'awaiting_user_input' && <span className="dialog-badge">💬</span>}
          </li>
        ))}
      </ul>
    </aside>
  )
}
