// composer-recipients — who a stage Feed composer's message goes to (the "To" chips): people picked from the
// People panel or the conversation being answered, `@handles` typed in the text, and — when nobody is named —
// the stage's own agent. Pure functions, so FeedComposer only renders what they decide.

import type { MeshParticipant } from '../../api/mesh-client'
import { mentionHandles } from '../../lib/mesh-roster'

export type RecipientSource = 'picked' | 'typed' | 'default'

export type ComposerRecipient = {
  // key — the participant id, or `@handle` for a typed name nobody in the run has.
  key: string
  // handle — what the chip shows after `@`.
  handle: string
  participantId: string | null
  source: RecipientSource
  // problem — why the message cannot go to them; Send stays off while any recipient has one.
  problem: string | null
}

export type ComposerRecipients = {
  recipients: ComposerRecipient[]
  // defaultProblem — nobody is named and the stage agent cannot be written to either (finished, or waiting for
  // its helpers): why, so the composer asks for a recipient instead.
  defaultProblem: string | null
}

type StageStatusLike = { id: string; status: string }

// typedMentionHandles — the `@handle` tokens of a draft, in order: an `@` at the start or after whitespace (so an
// e-mail address is no mention), without trailing punctuation.
export function typedMentionHandles(draft: string): string[] {
  const out: string[] = []
  for (const m of draft.matchAll(/(^|\s)@([^\s@]+)/g)) {
    const handle = (m[2] ?? '').replace(/[.,;:!?)]+$/, '')
    if (handle !== '') out.push(handle)
  }
  return out
}

// recipientProblem — why p cannot get a message from the human right now, or null. A stage agent waiting for
// its helpers has finished its turn: the server would accept the message, but the agent reads it only when a
// Revise restarts it, so the composer says so instead of letting it sit unread.
function recipientProblem(p: MeshParticipant, handle: string, stages: StageStatusLike[]): string | null {
  if (p.kind === 'user') return `@${handle} is you — you can't write to yourself`
  if (!p.reachable) return `@${handle} can't receive messages: ${p.unreachable_reason !== undefined && p.unreachable_reason !== '' ? p.unreachable_reason : 'unavailable'}`
  if (p.kind === 'stage_agent' && stages.find((s) => s.id === p.stage_id)?.status === 'awaiting_assistants') {
    return `@${handle} has finished its turn and waits for its helpers — it reads a note only as a Revise`
  }
  return null
}

// resolveTyped — the participant a typed handle names: its roster-unique @handle, its id, or an alias only it
// has (case-insensitive). Several people sharing the alias is an error, never a silent pick.
function resolveTyped(handle: string, participants: MeshParticipant[], handles: Map<string, string>): MeshParticipant | 'ambiguous' | null {
  const h = handle.toLowerCase()
  const byHandle = participants.find((p) => (handles.get(p.id) ?? p.id).toLowerCase() === h || p.id.toLowerCase() === h)
  if (byHandle !== undefined) return byHandle
  const byAlias = participants.filter((p) => (p.alias ?? '').toLowerCase() === h)
  if (byAlias.length > 1) return 'ambiguous'
  return byAlias[0] ?? null
}

export type ComposerRecipientsInput = {
  participants: MeshParticipant[]
  stages: StageStatusLike[]
  // stageId — the feed's stage, whose agent is the default recipient.
  stageId: string
  // picked — participant ids added as chips (People panel, the sender of the message being answered).
  picked: string[]
  draft: string
}

// composerRecipients — the message's recipients: picked people first, then typed @handles, each person once;
// without either, the stage agent if it can read the message. Unknown, ambiguous and unreachable names stay in
// the list with their problem: an explicit recipient is never dropped or replaced by someone else.
export function composerRecipients({ participants, stages, stageId, picked, draft }: ComposerRecipientsInput): ComposerRecipients {
  const handles = mentionHandles(participants)
  const handleOf = (p: MeshParticipant): string => handles.get(p.id) ?? p.id
  const recipients: ComposerRecipient[] = []
  const seen = new Set<string>()
  const add = (r: ComposerRecipient): void => {
    if (seen.has(r.key)) return
    seen.add(r.key)
    recipients.push(r)
  }

  for (const id of picked) {
    const p = participants.find((x) => x.id === id)
    if (p === undefined) continue
    add({ key: p.id, handle: handleOf(p), participantId: p.id, source: 'picked', problem: recipientProblem(p, handleOf(p), stages) })
  }
  for (const handle of typedMentionHandles(draft)) {
    const p = resolveTyped(handle, participants, handles)
    if (p === 'ambiguous') {
      add({ key: `@${handle.toLowerCase()}`, handle, participantId: null, source: 'typed', problem: `@${handle} matches several people — pick one from the list` })
    } else if (p === null) {
      add({ key: `@${handle.toLowerCase()}`, handle, participantId: null, source: 'typed', problem: `No one is called @${handle} in this run` })
    } else {
      add({ key: p.id, handle: handleOf(p), participantId: p.id, source: 'typed', problem: recipientProblem(p, handleOf(p), stages) })
    }
  }
  if (recipients.length > 0) return { recipients, defaultProblem: null }

  const stageAgent = participants.find((p) => p.id === `stage:${stageId}`)
  if (stageAgent === undefined) return { recipients, defaultProblem: 'Pick someone to write to' }
  const problem = recipientProblem(stageAgent, handleOf(stageAgent), stages)
  if (problem !== null) return { recipients, defaultProblem: problem }
  return {
    recipients: [{ key: stageAgent.id, handle: handleOf(stageAgent), participantId: stageAgent.id, source: 'default', problem: null }],
    defaultProblem: null,
  }
}

// recipientFence — one message's identity for useRetryableSubmit: the recipient set (order-insensitive), the
// thread and the message it answers. Changing any of them makes it a different message with a new request id.
export function recipientFence(recipientIds: string[], threadId: string, replyTo: string): string {
  return JSON.stringify([[...recipientIds].sort(), threadId, replyTo])
}
