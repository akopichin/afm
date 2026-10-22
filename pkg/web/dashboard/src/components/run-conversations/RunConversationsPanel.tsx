import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type Dispatch, type KeyboardEvent as ReactKeyboardEvent, type ReactElement, type SetStateAction } from 'react'
import type { MeshParticipant, MeshThread, MeshThreadSummaries } from '../../api/mesh-client'
import { useModalDialog } from '../../lib/focus-trap'
import { orderInboxThreads } from '../../lib/team-inbox'
import { usePanelResize } from '../layout/use-panel-resize'
import { ThreadConversation, conversationTitle, targetKey } from '../team/ThreadConversation'
import { ThreadList } from '../team/ThreadList'

export const RUN_CONVERSATIONS_ID = 'run-conversations'
// RUN_CONVERSATIONS_TOGGLE — the opener button in the workspace tab strip: where the focus goes back to.
export const RUN_CONVERSATIONS_TOGGLE = '.run-conversations-toggle'

const HEADING = 'Run conversations'

export type RunConversationsPanelProps = {
  // open — on a wide screen the panel sits beside the workspace, on a narrow one it is a drawer over it. It is
  // always rendered: a closed panel is only slid out and inert, so its conversation and draft survive.
  open: boolean
  onClose: () => void
  // participants/threads/summaries — the SAME roster poll App.tsx owns (useMeshRoster), not a second one.
  participants: MeshParticipant[]
  threads: MeshThread[]
  summaries: MeshThreadSummaries
  // unreadThreadIds — conversations to mark as new in the list; opening focuses the first of them.
  unreadThreadIds?: ReadonlySet<string>
  // seenSeqs / onSeen — the stored seen cursor per thread (agent messages to the human) and how a conversation
  // moves it forward; App owns them because the opener in the tab strip shows the same unread state.
  seenSeqs?: Readonly<Record<string, number>>
  onSeen?: (threadId: string, seq: number) => void
}

// runConversationThreads — the conversations of the whole run with the human: no stage owns them, so they are
// not in any stage Feed. A thread qualifies when the human is in it, it was not opened from a stage, and none
// of its members belongs to a stage (a run-scoped helper has no stage_id, a stage agent or stage helper does).
// A member the roster does not know yet defers the whole thread to the next roster poll: showing it now would
// mean a blank name.
export function runConversationThreads(threads: MeshThread[], participants: MeshParticipant[]): MeshThread[] {
  const byId = new Map(participants.map((p) => [p.id, p]))
  return threads.filter((thread) => {
    if (!thread.members.includes('user') || (thread.origin_stage ?? '') !== '') return false
    return thread.members.every((id) => {
      const member = byId.get(id)
      return member !== undefined && (member.stage_id ?? '') === ''
    })
  })
}

const NO_UNREAD: ReadonlySet<string> = new Set()
const NO_SEEN: Readonly<Record<string, number>> = {}
const IGNORE_SEEN = (): void => {}

// RunConversationsPanel — the right-hand panel with the run's own conversations (see runConversationThreads),
// readable and answerable from every workspace view. List -> history -> Back; the selection and each draft live
// here, so a view switch (which does not unmount the panel) keeps them.
export function RunConversationsPanel({ open, onClose, participants, threads, summaries, unreadThreadIds = NO_UNREAD, seenSeqs = NO_SEEN, onSeen = IGNORE_SEEN }: RunConversationsPanelProps): ReactElement {
  const rootRef = useRef<HTMLElement | null>(null)
  const headingRef = useRef<HTMLHeadingElement | null>(null)
  const backRef = useRef<HTMLButtonElement | null>(null)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  // replyTargets — per thread: the message being answered, or null once the human cancelled it. Kept here, like the
  // drafts, so it outlives Back; absent means "not chosen yet" (the newest open question is preselected).
  const [replyTargets, setReplyTargets] = useState<Record<string, string | null>>({})
  const resize = usePanelResize({
    storageKey: 'afm.runConversationsWidth', defaultWidth: 340, container: 'parent', direction: -1,
    drawerQuery: '(max-width: 1180px)', toggleSelector: RUN_CONVERSATIONS_TOGGLE,
  })
  const drawerOpen = open && resize.isDrawer

  const visible = useMemo(() => orderInboxThreads(runConversationThreads(threads, participants), summaries), [threads, participants, summaries])
  const selected = visible.find((t) => t.id === selectedId) ?? null
  const selectedDraftId = selected?.id ?? ''
  const setSelectedDraft: Dispatch<SetStateAction<string>> = useCallback(
    (next) => setDrafts((prev) => ({ ...prev, [selectedDraftId]: typeof next === 'function' ? next(prev[selectedDraftId] ?? '') : next })),
    [selectedDraftId],
  )

  const setSelectedReplyTarget = useCallback((id: string | null | undefined) => {
    setReplyTargets((prev) => {
      const { [selectedDraftId]: _dropped, ...rest } = prev
      return id === undefined ? rest : { ...rest, [selectedDraftId]: id }
    })
  }, [selectedDraftId])

  useLayoutEffect(() => {
    const layout = resize.targetRef.current?.parentElement
    layout?.style.setProperty('--run-conversations-width', `${resize.effectiveWidth}px`)
    return () => { layout?.style.removeProperty('--run-conversations-width') }
  }, [resize.effectiveWidth, resize.targetRef])

  // Clear inert before useModalDialog's layout effect moves focus into an opening drawer.
  // A closed panel is slid out of view and its controls must not stay in the tab order.
  useLayoutEffect(() => {
    if (rootRef.current !== null) rootRef.current.inert = !open
  }, [open])

  const focusToggle = useCallback(() => document.querySelector<HTMLElement>(RUN_CONVERSATIONS_TOGGLE), [])
  // The drawer covers the workspace, so it keeps Tab inside, closes on Escape and counts as an open dialog (an
  // attention item then does not take the focus or switch the view).
  useModalDialog(rootRef, onClose, focusToggle, drawerOpen)

  // Opening focuses the first new row, else the first row, else the heading — never the separator; closing
  // hands the focus back to the opener unless it has already moved on.
  useEffect(() => {
    const root = rootRef.current
    if (!open || root === null) return
    const rows = [...root.querySelectorAll<HTMLElement>('.team-thread-item')]
    const target = rows.find((row) => row.querySelector('.team-thread-unread') !== null) ?? rows[0] ?? headingRef.current
    target?.focus()
    return () => {
      const active = document.activeElement
      if (active === null || active === document.body || root.contains(active)) focusToggle()?.focus()
    }
  }, [open, focusToggle])

  // Swapping the list for a conversation (and back) removes the control that was clicked: put the focus where the
  // human continues, or it falls to the page and the drawer's Tab trap no longer holds it. Into a conversation:
  // its Back button; back to the list: the row the human came from.
  const shownThreadRef = useRef(selected?.id ?? null)
  useEffect(() => {
    const before = shownThreadRef.current
    const now = selected?.id ?? null
    shownThreadRef.current = now
    const root = rootRef.current
    if (before === now || !open || root === null) return
    if (now !== null) {
      backRef.current?.focus()
      return
    }
    const rows = [...root.querySelectorAll<HTMLElement>('.team-thread-item')]
    const cameFrom = rows[visible.findIndex((t) => t.id === before)]
    ;(cameFrom ?? rows[0] ?? headingRef.current)?.focus()
  }, [selected?.id, open, visible])

  // Escape closes the desktop panel only while the focus is inside it (the drawer form listens on the document).
  function onKeyDown(event: ReactKeyboardEvent<HTMLElement>): void {
    if (event.key === 'Escape' && !event.defaultPrevented && !resize.isDrawer) onClose()
  }

  return (
    <>
      {drawerOpen && (
        <button type="button" className="run-conversations-scrim" aria-label="Close run conversations" tabIndex={-1} onClick={onClose} />
      )}
      <aside
        id={RUN_CONVERSATIONS_ID}
        ref={(node) => { rootRef.current = node; resize.targetRef.current = node as HTMLDivElement | null }}
        className={`run-conversations${open ? ' open' : ''}`}
        aria-label={HEADING}
        role={drawerOpen ? 'dialog' : undefined}
        aria-modal={drawerOpen ? true : undefined}
        onKeyDown={onKeyDown}
      >
        <div
          ref={resize.handleRef}
          className="panel-resizer run-conversations-resizer"
          role="separator"
          aria-orientation="vertical"
          aria-label="Resize run conversations"
          aria-valuemin={200}
          aria-valuemax={resize.maxWidth}
          aria-valuenow={resize.effectiveWidth}
          aria-hidden={resize.isDrawer ? true : undefined}
          tabIndex={resize.isDrawer ? -1 : 0}
          onMouseDown={resize.onMouseDown}
          onKeyDown={resize.onKeyDown}
          onDoubleClick={resize.onDoubleClick}
        ><span className="panel-resizer-grip" aria-hidden="true" /></div>
        <div className="run-conversations-body">
          <header className="run-conversations-header">
            {selected !== null && (
              <button ref={backRef} type="button" className="team-icon-btn" aria-label="Back to conversations" onClick={() => setSelectedId(null)}>
                <span aria-hidden="true">←</span>
              </button>
            )}
            <h2 ref={headingRef} tabIndex={-1} className="run-conversations-title">{HEADING}</h2>
            <button type="button" className="team-icon-btn" aria-label="Close" onClick={onClose}>
              <span aria-hidden="true">×</span>
            </button>
          </header>
          {selected === null ? (
            visible.length === 0 ? (
              <p className="run-conversations-empty empty-hint">
                No run conversations yet. When an agent that is not tied to a stage writes to you, the conversation shows up here.
              </p>
            ) : (
              <ThreadList
                threads={visible}
                participants={participants}
                summaries={summaries}
                unreadThreadIds={unreadThreadIds}
                onSelect={(thread) => setSelectedId(thread.id)}
              />
            )
          ) : (
            <>
              <p className="run-conversations-subtitle" title={conversationTitle({ kind: 'thread', thread: selected }, participants)}>
                {conversationTitle({ kind: 'thread', thread: selected }, participants)}
              </p>
              <ThreadConversation
                key={targetKey({ kind: 'thread', thread: selected })}
                target={{ kind: 'thread', thread: selected }}
                participants={participants}
                threads={visible}
                summaries={summaries}
                draft={drafts[selected.id] ?? ''}
                onDraftChange={setSelectedDraft}
                run={{
                  seenSeq: seenSeqs[selected.id] ?? 0,
                  onSeen,
                  tracking: open,
                  replyTargetId: replyTargets[selected.id],
                  onReplyTargetChange: setSelectedReplyTarget,
                }}
              />
            </>
          )}
        </div>
      </aside>
    </>
  )
}
