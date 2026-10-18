// mesh-recipients — pure helpers for the feed composer's `@`-mention menu and thread resolution (Agent Mesh
// Phase 7, T7.4). Deliberately NOT a hook/component, same posture as mention-match.ts: easy to unit-test in
// isolation, FeedComposer just calls these.

import type { AssistantProfile } from '../../types/assistant-profile'
import type { MeshParticipant, MeshThread } from '../../api/mesh-client'
import type { MentionGroup, MentionItem } from '../assistants/AssistantMentionMenu'

// MeshMentionTarget — what a selected feed-composer mention resolves to for Send: either an EXISTING mesh
// participant (Send addresses it directly into its own thread, no new task) or a configured profile with no
// participant yet ("Attach to stage" — Send must go through attachAndSendMesh, the T6.2 composite, same
// endpoint AddRunAgentDialog already calls). scope on the participant variant is the PARTICIPANT's own scope
// (run-scoped roles get a run-scoped thread even when mentioned from a stage's feed), not the composer's own
// stage.
export type MeshMentionTarget =
  | { kind: 'participant'; id: string; scope: 'stage' | 'run' }
  | { kind: 'profile'; profileId: string }

export type MeshMentionItem = MentionItem & { target: MeshMentionTarget }

// participantLabel — "display_name, else alias, else id" fallback for a mesh participant. The feed composer's
// own copy of the same fallback ThreadConversation/TeamView already use inline for their one-off labels.
export function participantLabel(p: MeshParticipant): string {
  if (p.display_name !== undefined && p.display_name !== '') return p.display_name
  if (p.alias !== undefined && p.alias !== '') return p.alias
  return p.id
}

function toParticipantItem(p: MeshParticipant): MeshMentionItem {
  return {
    id: p.id,
    title: participantLabel(p),
    unavailable: !p.reachable,
    unavailableReason: p.unreachable_reason,
    target: { kind: 'participant', id: p.id, scope: p.scope === 'run' ? 'run' : 'stage' },
  }
}

// buildMeshMentionGroups — the feed composer's `@`-mention source, grouped per dev-plan.md T7.4: "This run"
// (standing run-scoped roles and the Side agent), "This stage" (THIS composer's own stage agent and any
// helper already attached to it) and "Attach to stage" (configured profiles — picking one and sending goes
// through attachAndSendMesh, never a plain Send into an existing thread). Query filtering is mention-match's
// own filterMentionGroups, not reimplemented here.
export function buildMeshMentionGroups(
  participants: MeshParticipant[],
  profiles: AssistantProfile[],
  stageId: string,
): MentionGroup<MeshMentionItem>[] {
  const thisRun = participants.filter((p) => p.scope === 'run' && p.kind !== 'user').map(toParticipantItem)
  const thisStage = participants.filter((p) => p.scope === 'stage' && p.stage_id === stageId).map(toParticipantItem)
  const attach: MeshMentionItem[] = profiles
    .filter((p) => p.available)
    .map((p) => ({ id: p.id, title: p.title, target: { kind: 'profile', profileId: p.id } }))

  return [
    { label: 'This run', items: thisRun },
    { label: 'This stage', items: thisStage },
    { label: 'Attach to stage', items: attach },
  ]
}

// findExistingDirectThread — an already-open, exactly-two-member (human + participant) thread, if one
// exists. Re-addressing the same participant must land back in THAT conversation; a participant with no such
// thread yet gets a NEW one on send — a larger group thread that happens to include them is deliberately NOT
// matched here, so sending never widens an existing private conversation into a group one.
export function findExistingDirectThread(threads: MeshThread[], participantId: string): MeshThread | null {
  return (
    threads.find(
      (t) => t.status === 'open' && t.members.length === 2 && t.members.includes(participantId) && t.members.includes('user'),
    ) ?? null
  )
}
