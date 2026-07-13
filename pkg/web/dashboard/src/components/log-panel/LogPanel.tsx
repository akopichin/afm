import type { ReactElement } from 'react'
import type { LogEntry } from '../../types'
import { PanelFrame } from '../panel-frame/PanelFrame'

type LogPanelProps = {
  entries: LogEntry[]
}

// Окно операционного лога выбранной стадии. Записи уже отфильтрованы хуком useStageLog
// (только text-строки); здесь они выводятся как есть, в хронологическом порядке.
// id log-content / log-empty сохранены для тем. Без maximize — лог компактный.
export function LogPanel({ entries }: LogPanelProps): ReactElement {
  const hasEntries = entries.length > 0

  return (
    <PanelFrame title="Log">
      <div className="section">
        <pre id="log-content" className={`log-content${hasEntries ? '' : ' hidden'}`}>
          {entries.map((entry) => entry.message).join('\n')}
        </pre>
        <div id="log-empty" className={`empty-hint${hasEntries ? ' hidden' : ''}`}>Log is empty</div>
      </div>
    </PanelFrame>
  )
}
