// composer-draft — the one draft Chat and Restart with note share, as pure transitions: every race of a late
// result is a plain unit test.

import type { MeshParticipant } from '../../api/mesh-client'
import { attachmentMarker, type ImagePasteDraft } from '../../hooks/use-image-paste'
import { removeSubmittedText } from '../../hooks/use-retryable-submit'
import { buildReplyBody } from '../../lib/quote'
import { composerRecipients, messageRoute, type ComposerRecipients, type MessageRoute } from './composer-recipients'
import { isLoopCommandDraft } from './loop-command'

export type ComposerMode = 'chat' | 'restart'

// MeshReplyTarget — a conversation message the human answers in its thread ("↩ Reply in conversation").
// members — the members of that message's conversation: the reply stays in it, so only they can get it.
export type MeshReplyTarget = { messageId: string; threadId: string; senderId: string; preview: string; members: string[] }

// ReplyContext — what the input answers: a thought of the stage agent (quoted on send) or a conversation message.
export type ReplyContext = { kind: 'thought'; key: string; text: string } | { kind: 'conversation'; target: MeshReplyTarget }

export type ComposerOp = 'chat' | 'restart' | 'loop' | 'attach'
export type InFlight = { op: ComposerOp; token: number }
// UnsettledChat — the last Chat request that has not succeeded: an unchanged retry reuses its request id, so a
// response lost after the server committed it does not duplicate the message.
export type UnsettledChat = { intentKey: string; requestId: string; error: string | null }
// Attaching — an "Attach to stage" profile picked with @: Send opens the attach preview instead of sending.
export type Attaching = { profileId: string; title: string }

export type ComposerDraft = {
  mode: ComposerMode
  // modeChosen — the human chose the mode (the switch, a reply, a People pick): a late mesh never overrides it.
  modeChosen: boolean
  text: string
  attachments: ImagePasteDraft
  // picked — Chat recipients added from the People panel; Restart keeps them for the way back to Chat.
  picked: string[]
  attaching: Attaching | null
  interrupt: boolean
  context: ReplyContext | null
  // contextGeneration — changes with every new, replaced or removed context, so a late result tells A → B → A apart.
  contextGeneration: number
  inFlight: InFlight | null
  unsettled: UnsettledChat | null
}

// EMPTY_COMPOSER_DRAFT — every stage's draft until it is edited; one object, so a store snapshot stays stable.
export const EMPTY_COMPOSER_DRAFT: ComposerDraft = Object.freeze({
  mode: 'chat',
  modeChosen: false,
  text: '',
  attachments: [],
  picked: [],
  attaching: null,
  interrupt: false,
  context: null,
  contextGeneration: 0,
  inFlight: null,
  unsettled: null,
})

// hasInput — the draft holds something the human wrote or attached (an upload in progress counts).
export function hasInput(d: ComposerDraft): boolean {
  return d.text.trim() !== '' || d.attachments.length > 0
}

export function defaultMode(hasMesh: boolean): ComposerMode {
  return hasMesh ? 'chat' : 'restart'
}

// withDefaultMode — the mode of a draft nobody started yet follows the mesh. A chosen mode, started input or an
// active Hold (the agent waits for this note) keep the mode they were started in.
export function withDefaultMode(d: ComposerDraft, env: { hasMesh: boolean; holdActive: boolean }): ComposerDraft {
  if (d.modeChosen || hasInput(d) || env.holdActive) return d
  const mode = defaultMode(env.hasMesh)
  return d.mode === mode ? d : { ...d, mode }
}

// patchDraft — the draft with these fields; the same object when nothing changes, so the store notifies no one.
export function patchDraft(d: ComposerDraft, patch: Partial<ComposerDraft>): ComposerDraft {
  const keys = Object.keys(patch) as (keyof ComposerDraft)[]
  return keys.every((key) => d[key] === patch[key]) ? d : { ...d, ...patch }
}

export function setMode(d: ComposerDraft, mode: ComposerMode): ComposerDraft {
  return d.mode === mode && d.modeChosen ? d : { ...d, mode, modeChosen: true }
}

// pickRecipient — a People pick writes to that person: Chat, with them added to the recipients.
export function pickRecipient(d: ComposerDraft, participantId: string): ComposerDraft {
  const picked = d.picked.includes(participantId) ? d.picked : [...d.picked, participantId]
  return picked === d.picked && d.mode === 'chat' && d.modeChosen ? d : { ...d, picked, mode: 'chat', modeChosen: true }
}

// withContext — a new reply target: the input stays, an attaching helper goes (it cannot take a reply).
function withContext(d: ComposerDraft, context: ReplyContext, mode: ComposerMode): ComposerDraft {
  return { ...d, context, contextGeneration: d.contextGeneration + 1, mode, modeChosen: true, attaching: null }
}

// selectThought — ↩ on a thought. The same thought again changes nothing: it only asks for the focus.
export function selectThought(d: ComposerDraft, thought: { key: string; text: string }, hasMesh: boolean): ComposerDraft {
  if (d.context?.kind === 'thought' && d.context.key === thought.key) return d
  return withContext(d, { kind: 'thought', key: thought.key, text: thought.text }, defaultMode(hasMesh))
}

// selectConversation — "Reply in conversation" is a Chat reply; the same message again changes nothing.
export function selectConversation(d: ComposerDraft, target: MeshReplyTarget): ComposerDraft {
  if (d.context?.kind === 'conversation' && d.context.target.messageId === target.messageId) return d
  return withContext(d, { kind: 'conversation', target }, 'chat')
}

export function cancelContext(d: ComposerDraft): ComposerDraft {
  return d.context === null ? d : { ...d, context: null, contextGeneration: d.contextGeneration + 1 }
}

let lastLockToken = 0

// nextLockToken — a token unique in this page, so only the operation that took the lock releases it.
export function nextLockToken(): number {
  lastLockToken += 1
  return lastLockToken
}

// takeLock — one operation on this input at a time; the draft is returned unchanged while another one runs.
export function takeLock(d: ComposerDraft, op: ComposerOp, token: number): ComposerDraft {
  return d.inFlight !== null ? d : { ...d, inFlight: { op, token } }
}

export function releaseLock(d: ComposerDraft, token: number): ComposerDraft {
  return d.inFlight?.token === token ? { ...d, inFlight: null } : d
}

// startChat — takes the lock for a Chat request; an unchanged retry keeps the unsettled request id.
export function startChat(d: ComposerDraft, intentKey: string, token: number, freshRequestId: string): ComposerDraft {
  if (d.inFlight !== null) return d
  const requestId = d.unsettled?.intentKey === intentKey ? d.unsettled.requestId : freshRequestId
  return { ...d, inFlight: { op: 'chat', token }, unsettled: { intentKey, requestId, error: null } }
}

// draftRecipients — the Chat recipients of this draft: the context adds its required recipient (the thought's
// stage agent, or the sender of the conversation message); the quote itself is never read for @mentions.
export function draftRecipients(d: ComposerDraft, roster: { participants: MeshParticipant[]; stages: { id: string; status: string }[]; stageId: string }): ComposerRecipients {
  const reply =
    d.context?.kind === 'thought'
      ? { replyTo: `stage:${roster.stageId}`, replyMembers: null }
      : d.context?.kind === 'conversation'
        ? { replyTo: d.context.target.senderId, replyMembers: d.context.target.members }
        : { replyTo: null, replyMembers: null }
  return composerRecipients({ ...roster, picked: d.picked, draft: d.text, ...reply })
}

export type SubmissionEnv = {
  stageId: string
  hasMesh: boolean
  // restartBlock — why the stage takes no Restart with note now, or null when it does.
  restartBlock: string | null
  recipients: ComposerRecipients
  conversationClosed: boolean
  stageRunning: boolean
}

// ChatIntent — exactly what a Chat request carries.
export type ChatIntent = { route: MessageRoute; recipientIds: string[]; inReplyTo: string | null; body: string; interrupt: boolean }

// Submission — what Send does with the draft now. A blocked reason of null is already explained on screen (an
// empty field, a recipient problem); switchTo offers the other mode, never chosen automatically.
export type Submission =
  | { kind: 'blocked'; reason: string | null; switchTo?: ComposerMode }
  | { kind: 'chat'; intent: ChatIntent }
  | { kind: 'restart'; body: string }
  | { kind: 'loop' }
  | { kind: 'attach'; profileId: string; instruction: string }

export const SLASH_IN_RESTART = 'Slash commands run in Chat'
const CHAT_OFF = "Chat isn't available now — collaboration is off. Your text is kept."
const SLASH_WITHOUT_CHAT = `${SLASH_IN_RESTART}. ${CHAT_OFF}`
const CLOSED_CONVERSATION = "Chat can't reply in a closed conversation. Your text is kept."
const ATTACH_ALONE = 'An attached helper gets the text alone — remove the other recipients or the helper'
const ATTACH_NO_REPLY = 'An attached helper gets the text alone — remove the reply or the helper'

export function buildSubmission(d: ComposerDraft, env: SubmissionEnv): Submission {
  const quote = d.context?.kind === 'thought' ? d.context.text : null
  if (d.mode === 'restart') {
    if (env.restartBlock !== null) {
      return { kind: 'blocked', reason: `Restart with note isn't available now — ${env.restartBlock}. Your text is kept.`, ...(env.hasMesh ? { switchTo: 'chat' as const } : {}) }
    }
    if (isLoopCommandDraft(d.text)) return env.hasMesh ? { kind: 'blocked', reason: SLASH_IN_RESTART, switchTo: 'chat' } : { kind: 'blocked', reason: SLASH_WITHOUT_CHAT }
    if (d.text.trim() === '') return { kind: 'blocked', reason: null }
    return { kind: 'restart', body: buildReplyBody(quote, d.text) }
  }

  if (!env.hasMesh) return { kind: 'blocked', reason: CHAT_OFF, ...(env.restartBlock === null ? { switchTo: 'restart' as const } : {}) }
  if (isLoopCommandDraft(d.text)) return { kind: 'loop' }
  if (d.text.trim() === '') return { kind: 'blocked', reason: null }
  const conversation = d.context?.kind === 'conversation' ? d.context.target : null
  if (conversation !== null && env.conversationClosed) return { kind: 'blocked', reason: CLOSED_CONVERSATION }
  const { recipients } = env.recipients
  if (d.attaching !== null) {
    if (d.context !== null) return { kind: 'blocked', reason: ATTACH_NO_REPLY }
    if (recipients.some((r) => r.source !== 'default')) return { kind: 'blocked', reason: ATTACH_ALONE }
    return { kind: 'attach', profileId: d.attaching.profileId, instruction: d.text.trim() }
  }
  const recipientIds = recipients.flatMap((r) => (r.participantId !== null ? [r.participantId] : [])).sort()
  if (recipientIds.length === 0 || recipients.some((r) => r.problem !== null)) return { kind: 'blocked', reason: null }
  const route = messageRoute(env.stageId, recipientIds, conversation !== null)
  return {
    kind: 'chat',
    intent: {
      route,
      recipientIds,
      inReplyTo: conversation?.messageId ?? null,
      body: buildReplyBody(quote, d.text),
      interrupt: d.interrupt && offersInterrupt(route, env.stageRunning),
    },
  }
}

// offersInterrupt — "Interrupt agent" applies to a new post to the stage agent while it runs.
export function offersInterrupt(route: MessageRoute, stageRunning: boolean): boolean {
  return route.kind === 'stage' && stageRunning
}

// chatIntentKey — a Chat request's identity for retries: its content only, never the mode or the context
// generation, so an identical request after A → B → A or a remount reuses the request id.
export function chatIntentKey(intent: ChatIntent): string {
  const addressees = intent.route.kind === 'stage' ? [...intent.route.mentions].sort() : [...intent.recipientIds].sort()
  return JSON.stringify([intent.route.kind, addressees, intent.inReplyTo, intent.body, intent.interrupt])
}

// SendSnapshot — the draft as it was sent (the raw text, not the body with a quote), so a late result tells
// what still belongs to it.
export type SendSnapshot = {
  op: ComposerOp
  token: number
  text: string
  sentAttachments: ImagePasteDraft
  contextGeneration: number
  // attaching — the helper pick an attach was sent for; a pick made since is a new one, even of the same profile.
  attaching: Attaching | null
  requestId: string | null
}

export function snapshotSend(d: ComposerDraft, op: ComposerOp, token: number, requestId: string | null = null): SendSnapshot {
  const sentAttachments = attachmentsIn(d.attachments, d.text)
  return { op, token, text: d.text, sentAttachments, contextGeneration: d.contextGeneration, attaching: d.attaching, requestId }
}

// attachmentsIn — the attachments a message with this text carries: those whose label is in it.
export function attachmentsIn(attachments: ImagePasteDraft, text: string): ImagePasteDraft {
  return attachments.filter((a) => {
    const marker = attachmentMarker(a)
    return marker !== null && text.includes(marker)
  })
}

// attachmentsToDiscard — the sent attachments the remaining text no longer names. One still named stays: the
// text could not be split from new edits, so it is kept with what it shows.
export function attachmentsToDiscard(sent: ImagePasteDraft, nextText: string): ImagePasteDraft {
  return sent.filter((a) => {
    const marker = attachmentMarker(a)
    return marker === null || !nextText.includes(marker)
  })
}

// completeSend — a success removes only what it sent: its own lock and helper pick, and its content while the
// context is unchanged (a context ends once nothing new remains). discard — the attachments to drop.
export function completeSend(d: ComposerDraft, snap: SendSnapshot): { draft: ComposerDraft; discard: ImagePasteDraft } {
  let next = releaseLock(d, snap.token)
  if (snap.op === 'chat') next = { ...next, interrupt: false, unsettled: next.unsettled?.requestId === snap.requestId ? null : next.unsettled }
  if (snap.op === 'attach' && next.attaching === snap.attaching) next = { ...next, attaching: null }
  if (d.contextGeneration !== snap.contextGeneration) return { draft: next, discard: [] }

  const text = removeSubmittedText(d.text, snap.text)
  const discard = attachmentsToDiscard(snap.sentAttachments, text)
  const gone = new Set(discard.map((a) => a.previewUrl))
  const attachments = gone.size === 0 ? d.attachments : d.attachments.filter((a) => !gone.has(a.previewUrl))
  const endsContext = snap.op !== 'loop' && d.context !== null && text.trim() === '' && attachments.length === 0
  return {
    draft: { ...next, text, attachments, ...(endsContext ? { context: null, contextGeneration: d.contextGeneration + 1 } : {}) },
    discard,
  }
}

// failSend — a failure keeps everything for the retry; a Chat failure records its reason with the retry id.
export function failSend(d: ComposerDraft, snap: SendSnapshot, error: string): ComposerDraft {
  const next = releaseLock(d, snap.token)
  if (snap.op !== 'chat' || next.unsettled === null || next.unsettled.requestId !== snap.requestId) return next
  return { ...next, unsettled: { ...next.unsettled, error } }
}
