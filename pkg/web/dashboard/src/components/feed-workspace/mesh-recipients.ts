// mesh-recipients — pure helpers for the feed composer's `@`-mention menu and thread resolution (Agent Mesh
// Phase 7, T7.4). Deliberately NOT a hook/component, same posture as mention-match.ts: easy to unit-test in
// isolation, FeedComposer just calls these.

import type { AssistantProfile } from '../../types/assistant-profile'
import type { MeshParticipant } from '../../api/mesh-client'
import { mentionHandles, participantLabel, participantMentionNote } from '../../lib/mesh-roster'
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

function toParticipantItem(p: MeshParticipant, handle: string): MeshMentionItem {
  return {
    id: p.id,
    handle,
    title: participantLabel(p),
    kind: p.kind,
    description: participantMentionNote(p),
    unavailable: !p.reachable,
    unavailableReason: p.unreachable_reason,
    target: { kind: 'participant', id: p.id, scope: p.scope === 'run' ? 'run' : 'stage' },
  }
}

// buildMeshMentionGroups — the feed composer's `@`-mention source, grouped per dev-plan.md T7.4: "This run"
// (standing run-scoped roles and the Side agent), "This stage" (THIS composer's own stage agent and any
// helper already attached to it), "Other stages" (the agents and helpers of every other stage) and "Attach to
// stage" (configured profiles — picking one and sending goes through attachAndSendMesh, never a plain Send
// into an existing thread). Query filtering is mention-match's
// own filterMentionGroups, not reimplemented here.
export function buildMeshMentionGroups(
  participants: MeshParticipant[],
  profiles: AssistantProfile[],
  stageId: string,
): MentionGroup<MeshMentionItem>[] {
  // One @handle per participant across the whole roster: two run agents sharing an alias get their ids.
  const handles = mentionHandles(participants)
  const item = (p: MeshParticipant): MeshMentionItem => toParticipantItem(p, handles.get(p.id) ?? p.id)
  const thisRun = participants.filter((p) => p.scope === 'run' && p.kind !== 'user').map(item)
  const thisStage = participants.filter((p) => p.scope === 'stage' && p.stage_id === stageId).map(item)
  const otherStages = participants.filter((p) => p.scope === 'stage' && p.stage_id !== stageId).map(item)
  const attach: MeshMentionItem[] = profiles
    .filter((p) => p.available)
    .map((p) => ({ id: p.id, title: p.title, description: 'Helper · Attach to stage', kind: 'helper', target: { kind: 'profile', profileId: p.id } }))

  return [
    { label: 'This run', items: thisRun },
    { label: 'This stage', items: thisStage },
    { label: 'Other stages', items: otherStages },
    { label: 'Attach to stage', items: attach },
  ]
}

// isLoopTarget — who a `/loop` may repeat a prompt to: a run agent or a helper of THIS stage. The stage's own
// agent, the Side agent, the human and other stages' helpers are no loop targets.
export function isLoopTarget(p: MeshParticipant, stageId: string): boolean {
  return p.kind === 'run_agent' || (p.kind === 'helper' && p.stage_id === stageId)
}

// buildLoopMentionGroups — the `@` menu while a loop command is typed: only reachable loop targets, in the usual
// "This run" / "This stage" groups. No attach profiles: a loop never creates a helper.
export function buildLoopMentionGroups(participants: MeshParticipant[], stageId: string): MentionGroup<MeshMentionItem>[] {
  const handles = mentionHandles(participants)
  const items = (kind: string): MeshMentionItem[] =>
    participants.filter((p) => p.kind === kind && p.reachable && isLoopTarget(p, stageId)).map((p) => toParticipantItem(p, handles.get(p.id) ?? p.id))
  return [
    { label: 'This run', items: items('run_agent') },
    { label: 'This stage', items: items('helper') },
  ]
}
