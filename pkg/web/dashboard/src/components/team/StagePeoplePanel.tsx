import { useContext, useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type ReactElement } from 'react'
import type { AssistantTask } from '../../types/assistant-task'
import type { Stage } from '../../types'
import type { AssistantProfile } from '../../types/assistant-profile'
import type { MeshParticipant } from '../../api/mesh-client'
import { RunTeamList, teamStagesOf } from './RunTeamList'
import { AddRunAgentDialog } from './AddRunAgentDialog'
import { visibleFocusables, trapTab, DialogOpenContext } from '../../lib/focus-trap'
import { usePanelResize } from '../layout/use-panel-resize'

export const STAGE_PEOPLE_ID = 'stage-people'
// At this width and below the panel is a drawer; keep in sync with the media query in skins/base/mesh.css.
export const STAGE_PEOPLE_DRAWER_QUERY = '(max-width: 1180px)'

function peopleToggle(): HTMLElement | null {
  return document.querySelector<HTMLElement>('.stage-people-toggle')
}

export type StagePeoplePanelProps = {
  stageId: string
  helperTasks?: readonly AssistantTask[]
  // participants — the SAME roster poll App.tsx owns (useMeshRoster), not a second one, merged with run
  // agents added here that the poll has not reported yet (the composer resolves picks against the same list).
  participants: MeshParticipant[]
  stages: Stage[]
  profiles: AssistantProfile[]
  // open — whether the panel is shown: beside the feed on a wide screen, as a drawer on a narrow one. Both are
  // toggled by the People button in the workspace header.
  open: boolean
  // drawer — the layout `open` was resolved for. The same flag decides both, so the panel never sees a wide
  // `open` with the drawer layout (or the reverse) while the viewport crosses the breakpoint.
  drawer: boolean
  onClose: () => void
  // onPick — address the person in the Feed composer. Undefined while this stage has no composer: the
  // rows are disabled and a hint says when one appears.
  onPick?: (participant: MeshParticipant) => void
  // onAgentAdded — a run agent was just added: the owner shows it (and lets it be picked) before the next
  // roster poll.
  onAgentAdded: (participant: MeshParticipant) => void
  // onAttachHelper — open the stage's "Attach helper" dialog (a helper of this stage, not a run agent).
  // Undefined while the stage cannot take one: no button.
  onAttachHelper?: (stageId: string) => void
}

// StagePeoplePanel — the stage Feed's right side panel: who this stage can talk to (RunTeamList in focus
// mode). A click adds the person to the Feed composer's recipients; "Add run agent…" opens the same dialog as
// the Team tab, "Attach helper to stage…" the stage's helper dialog. A wide screen shows it next to the feed, a
// narrow one as a drawer. Only the drawer closes itself after an action; the wide panel stays until hidden.
export function StagePeoplePanel({ stageId, participants, stages, profiles, open, drawer, onClose, onPick, onAgentAdded, onAttachHelper, helperTasks }: StagePeoplePanelProps): ReactElement {
  const [search, setSearch] = useState('')
  const [addOpen, setAddOpen] = useState(false)
  const drawerRef = useRef<HTMLDivElement | null>(null)
  const openerRef = useRef<HTMLElement | null>(null)
  const resize = usePanelResize({
    storageKey: 'afm.peopleWidth', defaultWidth: 280, container: 'parent', direction: -1,
    drawerQuery: STAGE_PEOPLE_DRAWER_QUERY, drawer, toggleSelector: '.stage-people-toggle',
  })

  const reportOpen = useContext(DialogOpenContext)
  useLayoutEffect(() => {
    if (!open || !resize.isDrawer) return
    reportOpen(true)
    return () => reportOpen(false)
  }, [open, resize.isDrawer, reportOpen])

  useLayoutEffect(() => {
    const layout = resize.targetRef.current?.parentElement
    layout?.style.setProperty('--people-width', `${resize.effectiveWidth}px`)
    return () => { layout?.style.removeProperty('--people-width') }
  }, [resize.effectiveWidth, resize.targetRef])

  // An open drawer takes the focus and remembers who had it; the wide panel has no opener.
  useEffect(() => {
    const drawer = drawerRef.current
    if (!resize.isDrawer) openerRef.current = null
    if (!open || !resize.isDrawer || drawer === null) return
    const opener = document.activeElement
    openerRef.current = opener instanceof HTMLElement && opener !== document.body ? opener : null
    visibleFocusables(drawer)[0]?.focus()
  }, [open, resize.isDrawer])

  // A panel that gets closed hands the focus to its opener (else the People button) unless it already moved on,
  // e.g. to the composer a pick filled in. Only closing the layout in effect counts: a resize between the wide
  // panel and the drawer, or an unmount, never moves the focus.
  const layout = resize.isDrawer ? 'drawer' : 'wide'
  const shown = open ? layout : null
  const previouslyShown = useRef(shown)
  useEffect(() => {
    const wasShown = previouslyShown.current
    previouslyShown.current = shown
    if (wasShown !== layout || shown !== null) return
    const active = document.activeElement
    if (active === null || active === document.body || drawerRef.current?.contains(active)) {
      const returnTo = openerRef.current ?? peopleToggle()
      returnTo?.focus()
    }
    openerRef.current = null
  }, [shown, layout])

  // A dialog opened from the drawer records the focused element as its opener. Hand the focus to the People
  // toggle first, so Cancel/Escape return there and not to a control of the drawer that is being hidden.
  function closeDrawerForDialog(): void {
    if (!resize.isDrawer) return
    peopleToggle()?.focus()
    onClose()
  }

  // Tab stays inside the open drawer: from its last control to its first, and back with Shift+Tab.
  function handleTab(e: ReactKeyboardEvent<HTMLDivElement>): void {
    if (open && resize.isDrawer && drawerRef.current !== null) trapTab(e, drawerRef.current)
  }

  // Escape closes the drawer, like the scrim; the wide panel ignores it.
  useEffect(() => {
    if (!open || !resize.isDrawer) return
    function onKeyDown(e: KeyboardEvent): void {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [open, resize.isDrawer, onClose])

  return (
    <>
      {/* Not a landmark of its own: the roster inside is the "People" navigation. */}
      <div id={STAGE_PEOPLE_ID} ref={(node) => { drawerRef.current = node; resize.targetRef.current = node }} className={`stage-people${open ? ' open' : ''}`} onKeyDown={handleTab} tabIndex={-1} role={open && resize.isDrawer ? 'dialog' : undefined} aria-label={open && resize.isDrawer ? 'Stage people' : undefined} aria-modal={open && resize.isDrawer ? true : undefined}>
        <div
          ref={resize.handleRef}
          className="panel-resizer stage-people-resizer"
          role="separator"
          aria-orientation="vertical"
          aria-label="Resize people panel"
          aria-valuemin={200}
          aria-valuemax={resize.maxWidth}
          aria-valuenow={resize.effectiveWidth}
          aria-hidden={resize.isDrawer ? true : undefined}
          tabIndex={resize.isDrawer ? -1 : 0}
          onMouseDown={resize.onMouseDown}
          onKeyDown={resize.onKeyDown}
          onDoubleClick={resize.onDoubleClick}
        ><span className="panel-resizer-grip" aria-hidden="true" /></div>
        <RunTeamList
          id={`${STAGE_PEOPLE_ID}-roster`}
          drawer={false}
          focusStageId={stageId}
          helperTasks={helperTasks}
          search={search}
          onSearchChange={setSearch}
          onClose={onClose}
          participants={participants}
          stages={teamStagesOf(stages)}
          onPick={
            onPick === undefined
              ? undefined
              : (p) => {
                  onPick(p)
                  if (resize.isDrawer) onClose()
                }
          }
          pickHint={onPick === undefined ? 'You can write to them here while the run is going.' : undefined}
          onAddRunAgent={() => {
            closeDrawerForDialog()
            setAddOpen(true)
          }}
          onAttachHelper={
            onAttachHelper === undefined
              ? undefined
              : () => {
                  closeDrawerForDialog()
                  onAttachHelper(stageId)
                }
          }
        />
      </div>
      {open && resize.isDrawer && <button type="button" className="stage-people-scrim" aria-label="Close people" onClick={onClose} />}
      {addOpen && (
        <AddRunAgentDialog
          profiles={profiles}
          originStage={stageId}
          onAttached={(_, participant) => {
            setAddOpen(false)
            onAgentAdded(participant)
          }}
          onCancel={() => setAddOpen(false)}
        />
      )}
    </>
  )
}
