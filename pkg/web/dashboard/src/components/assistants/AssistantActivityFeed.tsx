import { useCallback, useLayoutEffect, useRef, type ReactElement, type ReactNode } from 'react'
import type { AssistantActivity } from '../../hooks/use-assistant-activity'
import { useStickToBottom } from '../../hooks/use-stick-to-bottom'
import { formatEventGap } from '../feed-workspace'
import { JumpToLatestButton } from '../jump-to-latest'
import { AssistantActivityRow } from './AssistantActivityRow'

type AssistantActivityFeedProps = {
  activity: AssistantActivity
  // Shown in place of the rows while there are none (loading, waiting, unavailable…).
  empty: ReactNode
  // Trailing items after the history: the result or failure card.
  children?: ReactNode
}

const CAPPED_NOTE = 'Showing the latest 2000 actions. Earlier actions are not shown.'

// AssistantActivityFeed — the scroll region of one helper's history. The caller keys it by (run, task), so a
// task switch starts a fresh scroll position.
export function AssistantActivityFeed({ activity, empty, children }: AssistantActivityFeedProps): ReactElement {
  const { entries, hasOlder, loadingOlder, olderCapped, loadOlder } = activity
  const feed = useStickToBottom<HTMLDivElement>()
  const scrollNode = useRef<HTMLDivElement | null>(null)
  const edgeNote = useRef<HTMLDivElement | null>(null)
  // The first row in view and where it sat, as of the last commit or scroll: whatever changes above or below
  // it, the commit puts it back at the same spot.
  const anchor = useRef<{ id: string; top: number } | null>(null)
  // Where focus was inside the region; a row that is gone with focus on it drops focus to the page.
  const focused = useRef<Element | null>(null)
  const focusEdgeNote = useRef(false)

  const attachScrollNode = useCallback(
    (node: HTMLDivElement | null) => {
      scrollNode.current = node
      feed.ref(node)
    },
    [feed.ref], // a stable state setter, so the callback ref is not re-attached on every render
  )

  const rememberAnchor = useCallback(() => {
    const node = scrollNode.current
    const row = node === null ? null : firstVisibleRow(node)
    anchor.current = node === null || row === null ? null : { id: row.dataset.eventId ?? '', top: row.offsetTop - node.scrollTop }
  }, [])

  const showOlderButton = entries.length > 0 && hasOlder && !olderCapped
  // The note that replaces the button: the start of the history, or the bound of what the panel keeps.
  const showEdgeNote = entries.length > 0 && !showOlderButton
  useLayoutEffect(() => {
    if (!showEdgeNote || !focusEdgeNote.current) return
    focusEdgeNote.current = false
    // The button that had focus is gone; move focus only if nothing else took it.
    if (document.activeElement === document.body) edgeNote.current?.focus({ preventScroll: true })
  }, [showEdgeNote])

  // Keeps the reader's rows still when rows appear or vanish above them (an older page, a trim from the top);
  // rows appended below do not move the anchor, so they are not counted. While the feed follows the end, the
  // end is the position: a stale anchor would pull the view back from it.
  useLayoutEffect(() => {
    const node = scrollNode.current
    if (node === null) return
    const previous = feed.stick ? null : anchor.current
    const row = previous === null ? null : findRow(node, previous.id)
    if (previous !== null && row !== null) node.scrollTop += row.offsetTop - node.scrollTop - previous.top
    const lostFocus = focused.current !== null && !focused.current.isConnected
    if (lostFocus && document.activeElement === document.body) node.focus({ preventScroll: true })
    if (lostFocus) focused.current = null
    rememberAnchor()
  })

  function onLoadOlder(): void {
    if (loadingOlder) return
    feed.release()
    focusEdgeNote.current = true
    loadOlder()
  }

  return (
    <div
      className="assistant-activity-scroll"
      role="region"
      aria-label="Helper activity"
      tabIndex={0}
      ref={attachScrollNode}
      onScroll={rememberAnchor}
      onFocus={(event) => { focused.current = event.target }}
      onBlur={(event) => { if (event.target.isConnected) focused.current = null }}
    >
      {showOlderButton && (
        <button
          type="button"
          className="assistant-activity-older"
          aria-busy={loadingOlder}
          aria-disabled={loadingOlder}
          onClick={onLoadOlder}
        >
          {loadingOlder ? 'Loading older activity…' : 'Load older activity'}
        </button>
      )}
      {showEdgeNote && (
        <div className="assistant-activity-older-note" tabIndex={-1} ref={edgeNote}>
          {olderCapped ? CAPPED_NOTE : 'Start of activity'}
        </div>
      )}
      {entries.length === 0 ? (
        empty
      ) : (
        entries.map((entry, index) => {
          const previous = entries[index - 1]
          return (
            <AssistantActivityRow
              key={entry.eventId}
              entry={entry}
              gap={formatEventGap(Date.parse(entry.occurredAt), previous === undefined ? NaN : Date.parse(previous.occurredAt))}
              firstEvent={previous === undefined && !hasOlder}
              onBeforeExpand={feed.release}
            />
          )
        })
      )}
      {children}
      {!feed.stick && <JumpToLatestButton onClick={feed.jumpToBottom} />}
    </div>
  )
}

// Rows are direct children of the scroll region, after the older button or note.
function findRow(scroll: HTMLElement, eventId: string): HTMLElement | null {
  return Array.from(scroll.children).find((child): child is HTMLElement => child instanceof HTMLElement && child.dataset.eventId === eventId) ?? null
}

// The first row whose bottom edge is below the top of the view.
function firstVisibleRow(scroll: HTMLElement): HTMLElement | null {
  const rows = Array.from(scroll.children).filter((child): child is HTMLElement => child instanceof HTMLElement && child.dataset.eventId !== undefined)
  return rows.find((row) => row.offsetTop + row.offsetHeight > scroll.scrollTop) ?? null
}
