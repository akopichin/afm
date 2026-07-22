import type { ReactElement } from 'react'
import type { LogEntry } from '../../types'
import { Maximizable } from '../layout/Maximizable'
import { PanelFrame } from '../panel-frame/PanelFrame'

type LogPanelProps = {
  entries: LogEntry[]
}

// Окно операционного лога выбранной стадии. Записи уже отфильтрованы хуком useStageLog
// (только text-строки); здесь они выводятся как есть, в хронологическом порядке.
// id log-content / log-empty сохранены для тем. Maximizable id="logs" + PanelFrame
// maximizeId="logs" (как у plan/dialog) дают кнопку «на весь экран»: высота
// #log-content в обычном режиме ограничена (см. .log-content в log-panel.css), а в
// maximized-режиме ограничение снимается CSS-модификатором `.maximize-overlay
// .log-content` — полные строки без дополнительной обрезки.
export function LogPanel({ entries }: LogPanelProps): ReactElement {
  const hasEntries = entries.length > 0

  return (
    <Maximizable id="logs">
      <PanelFrame title="Log" maximizeId="logs">
        <div className="section">
          <pre id="log-content" className={`log-content${hasEntries ? '' : ' hidden'}`}>
            {entries.map((entry) => entry.message).join('\n')}
          </pre>
          <div id="log-empty" className={`empty-hint${hasEntries ? ' hidden' : ''}`}>Log is empty</div>
        </div>
      </PanelFrame>
    </Maximizable>
  )
}
