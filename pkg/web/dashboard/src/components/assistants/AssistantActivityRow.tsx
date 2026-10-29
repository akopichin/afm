import { memo, useId, useMemo, useState, type ReactElement } from 'react'
import { ACTIVITY_TEXT_TOOL, type AssistantActivityEntry } from '../../types/assistant-activity'
import { renderPlainMarkdown } from '../plan-panel/markdown'
import { MessageTime } from '../message-time/MessageTime'

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

type AssistantActivityRowProps = {
  entry: AssistantActivityEntry
  // Already formatted by the caller, so a primitive prop keeps the memoized row from re-rendering.
  gap: string
  firstEvent: boolean
  // Called before a row expands: the feed must stop following the bottom, or the new text scrolls out of view.
  onBeforeExpand: () => void
}

// AssistantActivityRow — one saved action of a helper: its message (Markdown) or a compact tool line. Memoized
// because a feed holds up to 2000 rows while the status line ticks every second.
export const AssistantActivityRow = memo(function AssistantActivityRow({ entry, gap, firstEvent, onBeforeExpand }: AssistantActivityRowProps): ReactElement {
  const [expanded, setExpanded] = useState(false)
  const bodyId = useId()
  const isText = entry.tool === ACTIVITY_TEXT_TOOL
  const long = isLong(entry)
  const collapsed = long && !expanded
  // Never given to the renderer as HTML: it is the same html:false Markdown path the stage feed uses.
  const html = useMemo(() => (isText ? renderPlainMarkdown(entry.detail) : ''), [isText, entry.detail])

  function toggle(): void {
    if (!expanded) onBeforeExpand()
    setExpanded(!expanded)
  }

  return (
    <div
      data-event-id={entry.eventId}
      className={`assistant-activity-row ${isText ? 'assistant-activity-message' : 'assistant-activity-tool'}${collapsed ? ' is-collapsed' : ''}`}
    >
      {isText ? (
        <div id={bodyId} className="assistant-activity-body md" dangerouslySetInnerHTML={{ __html: html }} />
      ) : (
        <>
          <span className="assistant-activity-tool-name">{entry.tool}</span>
          <span id={bodyId} className="assistant-activity-body">{entry.detail}</span>
        </>
      )}
      <span className="assistant-activity-meta">
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
