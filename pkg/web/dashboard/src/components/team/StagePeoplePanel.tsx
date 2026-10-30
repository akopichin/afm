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

export type StagePeoplePanelProps = {
  stageId: string
  helperTasks?: readonly AssistantTask[]
  // participants — the SAME roster poll App.tsx owns (useMeshRoster), not a second one, merged with run
  // agents added here that the poll has not reported yet (the composer resolves picks against the same list).
  participants: MeshParticipant[]
  stages: Stage[]
  profiles: AssistantProfile[]
  // open — on a narrow screen the panel is a drawer (opened by the 👥 button in the workspace header).
  open: boolean
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
// narrow one as a drawer.
export function StagePeoplePanel({ stageId, participants, stages, profiles, open, onClose, onPick, onAgentAdded, onAttachHelper, helperTasks }: StagePeoplePanelProps): ReactElement {
  const [search, setSearch] = useState('')
  const [addOpen, setAddOpen] = useState(false)
  const drawerRef = useRef<HTMLDivElement | null>(null)
  const openerRef = useRef<HTMLElement | null>(null)
  const resize = usePanelResize({
    storageKey: 'afm.peopleWidth', defaultWidth: 280, container: 'parent', direction: -1,
    drawerQuery: '(max-width: 1180px)', toggleSelector: '.stage-people-toggle',
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

  // The open drawer covers the feed: it takes the focus (its first control), and on close hands it back to
  // whatever opened it (the 👥 button) — unless the focus has already moved on, e.g. to the composer a pick
  // filled in.
  useEffect(() => {
    const drawer = drawerRef.current
    if (!open || drawer === null) return
    openerRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    if (resize.isDrawer) visibleFocusables(drawer)[0]?.focus()
    return () => {
      const active = document.activeElement
      if (active === null || active === document.body || drawer.contains(active)) openerRef.current?.focus()
      openerRef.current = null
    }
  }, [open, resize.isDrawer])

  // Tab stays inside the open drawer: from its last control to its first, and back with Shift+Tab.
  function handleTab(e: ReactKeyboardEvent<HTMLDivElement>): void {
    if (open && resize.isDrawer && drawerRef.current !== null) trapTab(e, drawerRef.current)
  }

  // Escape closes the drawer, like the scrim.
  useEffect(() => {
    if (!open) return
    function onKeyDown(e: KeyboardEvent): void {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [open, onClose])

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
          onClose={resize.isDrawer ? onClose : undefined}
          participants={participants}
          stages={teamStagesOf(stages)}
          onPick={
            onPick === undefined
              ? undefined
              : (p) => {
                  onPick(p)
                  onClose()
                }
          }
          pickHint={onPick === undefined ? 'You can write to them here while the run is going.' : undefined}
          onAddRunAgent={() => {
            onClose()
            setAddOpen(true)
          }}
          onAttachHelper={
            onAttachHelper === undefined
              ? undefined
              : () => {
                  onClose()
                  onAttachHelper(stageId)
                }
          }
        />
      </div>
      {open && <button type="button" className="stage-people-scrim" aria-label="Close people" onClick={onClose} />}
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
