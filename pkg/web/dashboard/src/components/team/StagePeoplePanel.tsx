import { useEffect, useState, type ReactElement } from 'react'
import type { Stage } from '../../types'
import type { AssistantProfile } from '../../types/assistant-profile'
import type { MeshParticipant } from '../../api/mesh-client'
import { mergeById } from '../../lib/mesh-roster'
import { RunTeamList, teamStagesOf } from './RunTeamList'
import { AddRunAgentDialog } from './AddRunAgentDialog'

export const STAGE_PEOPLE_ID = 'stage-people'

export type StagePeoplePanelProps = {
  stageId: string
  // participants — the SAME roster poll App.tsx owns (useMeshRoster), not a second one.
  participants: MeshParticipant[]
  stages: Stage[]
  profiles: AssistantProfile[]
  // open — on a narrow screen the panel is a drawer (opened by the 👥 button in the workspace header).
  open: boolean
  onClose: () => void
  // onPick — address the person in the Feed composer. Undefined while this stage has no composer: the
  // rows are disabled and a hint says when one appears.
  onPick?: (participant: MeshParticipant) => void
}

// StagePeoplePanel — the stage Feed's right side panel: who this stage can talk to (RunTeamList in focus
// mode). A click puts the person's @handle into the Feed composer; "+ Add agent" opens the same dialog as the
// Team tab. A wide screen shows it next to the feed, a narrow one as a drawer.
export function StagePeoplePanel({ stageId, participants, stages, profiles, open, onClose, onPick }: StagePeoplePanelProps): ReactElement {
  const [addOpen, setAddOpen] = useState(false)
  // localParticipants — run agents added here that the next roster poll has not reported yet, shown by name
  // right away (the same merge as the Team tab).
  const [localParticipants, setLocalParticipants] = useState<MeshParticipant[]>([])

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
      <div id={STAGE_PEOPLE_ID} className={`stage-people${open ? ' open' : ''}`}>
        <RunTeamList
          id={`${STAGE_PEOPLE_ID}-roster`}
          drawer={false}
          focusStageId={stageId}
          participants={mergeById(participants, localParticipants)}
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
          onAttached={(result, name) => {
            setAddOpen(false)
            setLocalParticipants((prev) => [
              ...prev,
              { id: result.participant_id, kind: 'run_agent', scope: 'run', display_name: name, reachable: true },
            ])
          }}
          onCancel={() => setAddOpen(false)}
        />
      )}
    </>
  )
}
