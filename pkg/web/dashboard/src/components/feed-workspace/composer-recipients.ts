// composer-recipients — who a stage Feed composer's message goes to (the "To" chips): the sender of the
// conversation message being answered, people picked from the People panel, `@handles` typed in the text, and —
// when nobody is named — the stage's own agent. Pure functions, so FeedComposer only renders what they decide.

import type { MeshParticipant } from '../../api/mesh-client'
import { mentionHandles } from '../../lib/mesh-roster'
import { removeMention } from '../assistants/mention-match'

// RecipientSource — where a chip came from first: the message being answered, a pick, the text, or the default.
export type RecipientSource = 'reply' | 'picked' | 'typed' | 'default'

export type ComposerRecipient = {
  // key — the participant id, or `@handle` for a typed name nobody in the run has.
  key: string
  // handle — what the chip shows after `@`.
  handle: string
  participantId: string | null
  source: RecipientSource
  // picked — the person is also in the picked list; typedTokens — every `@token` of the draft naming them, as
  // typed (a full id shown by its alias too). Removing the chip removes all of them, so one × un-addresses them.
  picked: boolean
  typedTokens: string[]
  // problem — why the message cannot go to them; Send stays off while any recipient has one.
  problem: string | null
  // note — what sending to them means when it is not the usual delivery (the feed stage's finished agent).
  note: string | null
}

// FINISHED_STAGE_NOTE — the feed stage's finished agent, named explicitly: the post is recorded in the stage's
// conversation (and reaches the other agents named) but its agent never reads it.
export const FINISHED_STAGE_NOTE = 'finished — post recorded, not read'

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
export function recipientProblem(p: MeshParticipant, handle: string, stages: StageStatusLike[]): string | null {
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
  // picked — participant ids added as chips from the People panel.
  picked: string[]
  draft: string
  // replyTo — the sender of the conversation message being answered: a recipient for that reply only.
  replyTo?: string | null
  // replyMembers — the members of that message's conversation: a reply stays in it, so it can go to them only
  // (a conversation never gains members). null — not a reply.
  replyMembers?: string[] | null
}

// composerRecipients — the message's recipients: the sender of the message answered first, then picked
// people, then typed @handles, each person once; without any of them, the stage agent if it can read the
// message. Unknown, ambiguous, unreachable and departed names stay in the list with their problem: an explicit
// recipient is never dropped or replaced by someone else.
export function composerRecipients({ participants, stages, stageId, picked, draft, replyTo = null, replyMembers = null }: ComposerRecipientsInput): ComposerRecipients {
  const handles = mentionHandles(participants)
  const handleOf = (p: MeshParticipant): string => handles.get(p.id) ?? p.id
  const recipients: ComposerRecipient[] = []
  const byKey = new Map<string, ComposerRecipient>()
  const add = (key: string, from: RecipientSource, found: () => Pick<ComposerRecipient, 'handle' | 'participantId' | 'problem' | 'note'>, token?: string): void => {
    let r = byKey.get(key)
    if (r === undefined) {
      r = { key, ...found(), source: from, picked: false, typedTokens: [] }
      byKey.set(key, r)
      recipients.push(r)
    }
    if (from === 'picked') r.picked = true
    if (token !== undefined) r.typedTokens.push(token)
  }
  // The feed stage's own agent once the stage has finished can still be named on purpose in a NEW message: it is a
  // post in the stage's conversation (messageRoute 'stage'), recorded (as the Discussion always allowed) and
  // delivered to the others named. A reply goes into its own conversation instead, where it is just finished.
  const finishedHere = (p: MeshParticipant): boolean =>
    replyMembers === null && p.id === `stage:${stageId}` && ['done', 'failed'].includes(stages.find((s) => s.id === stageId)?.status ?? '')
  const outsideReply = (p: MeshParticipant): string | null =>
    replyMembers !== null && !replyMembers.includes(p.id) ? `@${handleOf(p)} is not in this conversation — a reply goes only to its members` : null
  const known = (p: MeshParticipant) => () =>
    finishedHere(p)
      ? { handle: handleOf(p), participantId: p.id, problem: null, note: FINISHED_STAGE_NOTE }
      : { handle: handleOf(p), participantId: p.id, problem: recipientProblem(p, handleOf(p), stages) ?? outsideReply(p), note: null }
  // A person named by id who left the roster: still a chip, with the reason, so Send cannot quietly go elsewhere.
  const departed = (id: string) => () => ({ handle: id, participantId: null, problem: `@${id} is no longer available in this run`, note: null })
  const byId = (id: string, from: RecipientSource): void => {
    const p = participants.find((x) => x.id === id)
    add(id, from, p !== undefined ? known(p) : departed(id))
  }

  if (replyTo !== null && replyTo !== 'user') byId(replyTo, 'reply')
  for (const id of picked) byId(id, 'picked')
  for (const handle of typedMentionHandles(draft)) {
    const p = resolveTyped(handle, participants, handles)
    if (p === 'ambiguous') {
      add(`@${handle.toLowerCase()}`, 'typed', () => ({ handle, participantId: null, problem: `@${handle} matches several people — pick one from the list`, note: null }), handle)
    } else if (p === null) {
      add(`@${handle.toLowerCase()}`, 'typed', () => ({ handle, participantId: null, problem: `No one is called @${handle} in this run`, note: null }), handle)
    } else {
      add(p.id, 'typed', known(p), handle)
    }
  }
  if (recipients.length > 0) return { recipients, defaultProblem: null }

  const stageAgent = participants.find((p) => p.id === `stage:${stageId}`)
  if (stageAgent === undefined) return { recipients, defaultProblem: 'Pick someone to write to' }
  const problem = recipientProblem(stageAgent, handleOf(stageAgent), stages)
  if (problem !== null) return { recipients, defaultProblem: problem }
  return {
    recipients: [{ key: stageAgent.id, handle: handleOf(stageAgent), participantId: stageAgent.id, source: 'default', picked: false, typedTokens: [], problem: null, note: null }],
    defaultProblem: null,
  }
}

// withoutRecipient — the picked list and draft after × on chip r: its pick goes and every token naming it is
// cut from the text. The message being answered keeps its sender (cancel the reply instead).
export function withoutRecipient(r: ComposerRecipient, current: { picked: string[]; draft: string }): { picked: string[]; draft: string } {
  return {
    picked: r.picked ? current.picked.filter((id) => id !== r.key) : current.picked,
    draft: r.typedTokens.reduce((d, token) => removeMention(d, token), current.draft),
  }
}

// recipientFence — one message's identity for useRetryableSubmit: the recipient set (order-insensitive), the
// thread and the message it answers. Changing any of them makes it a different message with a new request id.
export function recipientFence(recipientIds: string[], threadId: string, replyTo: string): string {
  return JSON.stringify([[...recipientIds].sort(), threadId, replyTo])
}

// MessageRoute — how a Message is delivered.
//   - 'stage': a new message that names the feed stage's own agent is a post in the stage's conversation with
//     its agent (POST /api/stages/{id}/discussion) — the path that can interrupt a running agent, marks a post the
//     stage finished without answering, and puts the question into the agent's next turn; everybody else named
//     (mentions) joins that same conversation, so the message is still ONE message to all of them.
//   - 'set': anything else — a message without the stage agent, or a reply, which stays in the conversation of
//     the message it answers — goes to exactly its recipients (POST /api/mesh/messages).
export type MessageRoute = { kind: 'stage'; mentions: string[] } | { kind: 'set' }

export function messageRoute(stageId: string, recipientIds: string[], replying: boolean): MessageRoute {
  const stageAgent = `stage:${stageId}`
  if (replying || !recipientIds.includes(stageAgent)) return { kind: 'set' }
  return { kind: 'stage', mentions: recipientIds.filter((id) => id !== stageAgent) }
}
