import { memo, useId, useMemo, useState, type ReactElement } from 'react'
import { ACTIVITY_TEXT_TOOL, type AssistantActivityEntry } from '../../types/assistant-activity'
import { renderPlainMarkdown } from '../plan-panel/markdown'
import { MessageTime } from '../message-time/MessageTime'
import type { ActivityRowState } from '../../hooks/use-assistant-activity'

// Past these sizes a row is clamped until the reader expands it: tool inputs can be multi-KB heredocs.
const TOOL_COLLAPSE_CHARS = 120
const TEXT_COLLAPSE_CHARS = 300
const TEXT_COLLAPSE_LINES = 6

function isLong(entry: AssistantActivityEntry): boolean {
  if (entry.tool === ACTIVITY_TEXT_TOOL) {
    return entry.detail.length > TEXT_COLLAPSE_CHARS || entry.detail.split('\n').length > TEXT_COLLAPSE_LINES
  }
  return entry.detail.length > TOOL_COLLAPSE_CHARS || entry.detail.includes('\n')
}

// formatDuration renders a tool duration in whole seconds, then minutes and seconds.
function formatDuration(ms: number): string {
  const seconds = Math.round(ms / 1000)
  if (seconds < 1) return '<1s'
  if (seconds < 60) return `${seconds}s`
  return `${Math.floor(seconds / 60)}m ${seconds % 60}s`
}

// stateLabel is the outcome text of a row; a tool row without an outcome never claims success.
function stateLabel(state: ActivityRowState, entry: AssistantActivityEntry, durationMs: number | null): string | null {
  const exit = entry.exitCode === undefined ? '' : ` · exit ${entry.exitCode}`
  switch (state) {
    case 'running': return 'Started — completion not received yet'
    case 'finished': return durationMs === null ? 'Finished' : `Finished · ${formatDuration(durationMs)}`
    case 'failed': return `Failed${exit}`
    case 'timed_out': return `Timed out${exit}`
    case 'usage_limit': return 'Usage limit reached'
    case 'unknown_outcome': return 'Outcome unknown'
    case 'not_received': return 'Completion not received'
    case 'interrupted': return 'Interrupted'
    default: return null
  }
}

type AssistantActivityRowProps = {
  // Stable id of the row across updates of its entry; the feed finds the row by it to keep the scroll position.
  rowKey: string
  entry: AssistantActivityEntry
  state: ActivityRowState
  durationMs: number | null
  // Already formatted by the caller, so a primitive prop keeps the memoized row from re-rendering.
  gap: string
  firstEvent: boolean
  // Called before a row expands: the feed must stop following the bottom, or the new text scrolls out of view.
  onBeforeExpand: () => void
}

// AssistantActivityRow — one saved action of a helper: its message (Markdown) or a compact tool line. Memoized
// because a feed holds up to 2000 rows while the status line ticks every second.
export const AssistantActivityRow = memo(function AssistantActivityRow({ rowKey, entry, state, durationMs, gap, firstEvent, onBeforeExpand }: AssistantActivityRowProps): ReactElement {
  const [expanded, setExpanded] = useState(false)
  const bodyId = useId()
  const isText = entry.tool === ACTIVITY_TEXT_TOOL
  const submitted = state === 'answer_submitted'
  const label = stateLabel(state, entry, durationMs)
  const long = !submitted && isLong(entry)
  const collapsed = long && !expanded
  // Never given to the renderer as HTML: it is the same html:false Markdown path the stage feed uses.
  const html = useMemo(() => (isText ? renderPlainMarkdown(entry.detail) : ''), [isText, entry.detail])

  function toggle(): void {
    if (!expanded) onBeforeExpand()
    setExpanded(!expanded)
  }

  return (
    <div
      data-event-id={rowKey}
      className={`assistant-activity-row ${isText ? 'assistant-activity-message' : 'assistant-activity-tool'}${collapsed ? ' is-collapsed' : ''}`}
    >
      {submitted ? (
        <div id={bodyId} className="assistant-activity-body">Answer submitted — see the result</div>
      ) : isText ? (
        <div id={bodyId} className="assistant-activity-body md" dangerouslySetInnerHTML={{ __html: html }} />
      ) : (
        <>
          <span className="assistant-activity-tool-name">{entry.tool}</span>
          <span id={bodyId} className="assistant-activity-body">{entry.detail}</span>
        </>
      )}
      <span className="assistant-activity-meta">
        {label !== null && (
          <span className={`assistant-activity-state is-${state}`}>
            {state === 'running' && <span className="assistant-task-view-spinner" aria-hidden="true" />}
            {label}
          </span>
        )}
        {long && (
          <button type="button" className="assistant-activity-toggle" aria-expanded={expanded} aria-controls={bodyId} onClick={toggle}>
            {expanded ? 'Show less' : 'Show more'}
          </button>
        )}
        {entry.truncated && <span className="assistant-activity-truncated" title="The saved text was cut to a size limit">Shortened by afm</span>}
        <MessageTime timestamp={entry.occurredAt} gap={gap} firstEvent={firstEvent} focusable />
      </span>
    </div>
  )
})
