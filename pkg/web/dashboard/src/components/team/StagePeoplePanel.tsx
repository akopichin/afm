import { useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type ReactElement } from 'react'
import type { Stage } from '../../types'
import type { AssistantProfile } from '../../types/assistant-profile'
import type { MeshParticipant } from '../../api/mesh-client'
import { RunTeamList, teamStagesOf } from './RunTeamList'
import { AddRunAgentDialog } from './AddRunAgentDialog'
import { visibleFocusables } from '../../lib/focus-trap'

export const STAGE_PEOPLE_ID = 'stage-people'

export type StagePeoplePanelProps = {
  stageId: string
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
}

// StagePeoplePanel — the stage Feed's right side panel: who this stage can talk to (RunTeamList in focus
// mode). A click puts the person's @handle into the Feed composer; "+ Add agent" opens the same dialog as the
// Team tab. A wide screen shows it next to the feed, a narrow one as a drawer.
export function StagePeoplePanel({ stageId, participants, stages, profiles, open, onClose, onPick, onAgentAdded }: StagePeoplePanelProps): ReactElement {
  const [addOpen, setAddOpen] = useState(false)
  const drawerRef = useRef<HTMLDivElement | null>(null)
  const openerRef = useRef<HTMLElement | null>(null)

  // The open drawer covers the feed: it takes the focus (its first control), and on close hands it back to
  // whatever opened it (the 👥 button) — unless the focus has already moved on, e.g. to the composer a pick
  // filled in.
  useEffect(() => {
    const drawer = drawerRef.current
    if (!open || drawer === null) return
    openerRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    visibleFocusables(drawer)[0]?.focus()
    return () => {
      const active = document.activeElement
      if (active === null || active === document.body || drawer.contains(active)) openerRef.current?.focus()
      openerRef.current = null
    }
  }, [open])

  // Tab stays inside the open drawer: from its last control to its first, and back with Shift+Tab.
  function trapTab(e: ReactKeyboardEvent<HTMLDivElement>): void {
    if (!open || e.key !== 'Tab' || drawerRef.current === null) return
    const items = visibleFocusables(drawerRef.current)
    const first = items[0]
    const last = items[items.length - 1]
    if (first === undefined || last === undefined) return
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault()
      first.focus()
    }
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
      <div id={STAGE_PEOPLE_ID} ref={drawerRef} className={`stage-people${open ? ' open' : ''}`} onKeyDown={trapTab}>
        <RunTeamList
          id={`${STAGE_PEOPLE_ID}-roster`}
          drawer={false}
          focusStageId={stageId}
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
          pickHint={onPick === undefined ? 'You can write to them here while this stage is running.' : undefined}
          onAddRunAgent={() => {
            onClose()
            setAddOpen(true)
          }}
        />
      </div>
      {open && <button type="button" className="stage-people-scrim" aria-label="Close people" onClick={onClose} />}
      {addOpen && (
        <AddRunAgentDialog
          profiles={profiles}
          originStage={stageId}
          onAttached={(result, name) => {
            setAddOpen(false)
            onAgentAdded({ id: result.participant_id, kind: 'run_agent', scope: 'run', display_name: name, reachable: true })
          }}
          onCancel={() => setAddOpen(false)}
        />
      )}
    </>
  )
}
