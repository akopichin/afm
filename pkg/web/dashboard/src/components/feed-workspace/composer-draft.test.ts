import { describe, expect, it } from 'vitest'
import type { MeshParticipant } from '../../api/mesh-client'
import type { AttachmentRecord } from '../../hooks/use-image-paste'
import {
  EMPTY_COMPOSER_DRAFT,
  attachmentsToDiscard,
  buildSubmission,
  cancelContext,
  chatIntentKey,
  completeSend,
  draftRecipients,
  failSend,
  pickRecipient,
  releaseLock,
  selectConversation,
  selectThought,
  setMode,
  snapshotSend,
  startChat,
  takeLock,
  withDefaultMode,
  type ChatIntent,
  type ComposerDraft,
  type MeshReplyTarget,
  type SubmissionEnv,
} from './composer-draft'

const stageAgent: MeshParticipant = { id: 'stage:s1', kind: 'stage_agent', scope: 'stage', stage_id: 's1', alias: 's1', display_name: 'Backend agent', reachable: true }
const reviewer: MeshParticipant = { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'reviewer', display_name: 'Reviewer', reachable: true }
const docs: MeshParticipant = { id: 'run:t-2', kind: 'run_agent', scope: 'run', alias: 'docs', display_name: 'Docs', reachable: true }
const participants = [stageAgent, reviewer, docs]
const running = [{ id: 's1', status: 'running' }]

function attachment(id: string, path: string | null): AttachmentRecord {
  return {
    id, previewUrl: `blob:${id}`, uploading: path === null, failed: false, errorMsg: null,
    insertedText: path === null ? null : `[Screenshot: ${path}]\n`, file: new File([], `${id}.png`),
  }
}

function draft(patch: Partial<ComposerDraft> = {}): ComposerDraft {
  return { ...EMPTY_COMPOSER_DRAFT, ...patch }
}

const thoughtA = { key: 'k-a', text: 'Plan step: ask @reviewer to check the parser' }
const thoughtB = { key: 'k-b', text: 'Second thought' }
const conversation: MeshReplyTarget = { messageId: 'm1', threadId: 'mt-1', senderId: 'run:t-1', preview: 'Which parser?', members: ['user', 'run:t-1'] }

function env(d: ComposerDraft, patch: Partial<SubmissionEnv> = {}): SubmissionEnv {
  return {
    stageId: 's1',
    hasMesh: true,
    restartBlock: null,
    recipients: draftRecipients(d, { participants, stages: running, stageId: 's1' }),
    conversationClosed: false,
    stageRunning: true,
    ...patch,
  }
}

// sendFrom — what the composer does on submit: lock, then snapshot the locked draft.
function sendFrom(d: ComposerDraft, op: 'chat' | 'restart' | 'loop' | 'attach', token: number) {
  const locked = takeLock(d, op, token)
  return { locked, snap: snapshotSend(locked, op, token) }
}

describe('composer draft — selecting a reply context', () => {
  it('↩ on a thought keeps the text, attachments and picked people, selects Chat with the mesh and Restart without', () => {
    const shot = attachment('a', '/x/a.png')
    const before = draft({ text: 'pre-typed', attachments: [shot], picked: ['run:t-2'], mode: 'restart', modeChosen: true })
    const withMesh = selectThought(before, thoughtA, true)
    expect(withMesh).toMatchObject({ text: 'pre-typed', attachments: [shot], picked: ['run:t-2'], mode: 'chat', modeChosen: true })
    expect(withMesh.context).toEqual({ kind: 'thought', ...thoughtA })
    expect(withMesh.contextGeneration).toBe(before.contextGeneration + 1)

    expect(selectThought(draft({ text: 'note' }), thoughtA, false)).toMatchObject({ text: 'note', mode: 'restart' })
  })

  it('selecting the same thought again changes nothing (focus only); another thought replaces the quote', () => {
    const a = selectThought(draft({ text: 'x' }), thoughtA, true)
    expect(selectThought(a, thoughtA, true)).toBe(a)
    const b = selectThought(a, thoughtB, true)
    expect(b.context).toEqual({ kind: 'thought', ...thoughtB })
    expect(b.contextGeneration).toBe(a.contextGeneration + 1)
    expect(b.text).toBe('x')
  })

  it('a conversation reply selects Chat; the same message again changes nothing; it replaces a thought', () => {
    const t = selectThought(draft({ text: 'x', mode: 'restart', modeChosen: true }), thoughtA, true)
    const c = selectConversation(t, conversation)
    expect(c).toMatchObject({ text: 'x', mode: 'chat', context: { kind: 'conversation', target: conversation } })
    expect(c.contextGeneration).toBe(t.contextGeneration + 1)
    expect(selectConversation(c, { ...conversation })).toBe(c)
  })

  it('selecting a reply drops an attaching helper but keeps the text and attachments', () => {
    const shot = attachment('a', '/x/a.png')
    const d = draft({ text: 'review it', attachments: [shot], attaching: { profileId: 'p', title: 'Reviewer' } })
    expect(selectThought(d, thoughtA, true)).toMatchObject({ attaching: null, text: 'review it', attachments: [shot] })
    expect(selectConversation(d, conversation).attaching).toBeNull()
  })

  it('cancel removes the context only, bumping the generation; cancelling nothing changes nothing', () => {
    const shot = attachment('a', '/x/a.png')
    const c = selectConversation(draft({ text: 'keep', attachments: [shot], picked: ['run:t-2'] }), conversation)
    const cancelled = cancelContext(c)
    expect(cancelled).toMatchObject({ context: null, text: 'keep', attachments: [shot], picked: ['run:t-2'], contextGeneration: c.contextGeneration + 1 })
    expect(cancelContext(cancelled)).toBe(cancelled)
  })

  it('switching the mode keeps the context object and everything else', () => {
    const c = selectConversation(draft({ text: 'x' }), conversation)
    const r = setMode(c, 'restart')
    expect(r).toMatchObject({ mode: 'restart', modeChosen: true, text: 'x', contextGeneration: c.contextGeneration })
    expect(r.context).toBe(c.context)
    expect(setMode(r, 'restart')).toBe(r)
  })

  it('a People pick selects Chat and adds the person once, keeping the text', () => {
    const d = pickRecipient(draft({ text: 'x', mode: 'restart', modeChosen: true }), 'run:t-1')
    expect(d).toMatchObject({ mode: 'chat', picked: ['run:t-1'], text: 'x' })
    expect(pickRecipient(d, 'run:t-1')).toBe(d)
  })
})

describe('composer draft — default mode and a late mesh', () => {
  it('an untouched draft follows the mesh; a chosen mode, started input or an active Hold keep the mode', () => {
    const legacy = withDefaultMode(EMPTY_COMPOSER_DRAFT, { hasMesh: false, holdActive: false })
    expect(legacy.mode).toBe('restart')
    expect(withDefaultMode(legacy, { hasMesh: true, holdActive: false }).mode).toBe('chat')
    expect(withDefaultMode(legacy, { hasMesh: true, holdActive: true })).toBe(legacy)
    const typed = { ...legacy, text: 'started' }
    expect(withDefaultMode(typed, { hasMesh: true, holdActive: false })).toBe(typed)
    const imaged = { ...legacy, attachments: [attachment('a', null)] }
    expect(withDefaultMode(imaged, { hasMesh: true, holdActive: false })).toBe(imaged)
    const chosen = { ...legacy, modeChosen: true }
    expect(withDefaultMode(chosen, { hasMesh: true, holdActive: false })).toBe(chosen)
    expect(withDefaultMode(EMPTY_COMPOSER_DRAFT, { hasMesh: true, holdActive: false })).toBe(EMPTY_COMPOSER_DRAFT)
  })
})

describe('composer draft — recipients from the context', () => {
  it('the thought author is a required recipient; @mentions in the quote add no one, those in the text do', () => {
    const d = selectThought(draft({ text: 'please look @docs' }), thoughtA, true)
    const { recipients } = draftRecipients(d, { participants, stages: running, stageId: 's1' })
    expect(recipients.map((r) => [r.participantId, r.source])).toEqual([['stage:s1', 'reply'], ['run:t-2', 'typed']])
  })

  it('a conversation reply makes its sender required and keeps the conversation limit', () => {
    const d = selectConversation(draft({ text: 'yes @docs' }), conversation)
    const { recipients } = draftRecipients(d, { participants, stages: running, stageId: 's1' })
    expect(recipients[0]).toMatchObject({ participantId: 'run:t-1', source: 'reply', problem: null })
    expect(recipients[1]?.problem).toMatch(/not in this conversation/)
  })
})

describe('composer draft — what Send does', () => {
  it('Chat with a thought: a new post to the stage agent with the quote, the extra mention and Interrupt', () => {
    const d = { ...selectThought(draft({ text: 'agreed @reviewer' }), thoughtA, true), interrupt: true }
    const s = buildSubmission(d, env(d))
    expect(s).toEqual({
      kind: 'chat',
      intent: {
        route: { kind: 'stage', mentions: ['run:t-1'] },
        recipientIds: ['run:t-1', 'stage:s1'],
        inReplyTo: null,
        body: '> Plan step: ask @reviewer to check the parser\n\nagreed @reviewer',
        interrupt: true,
      },
    })
  })

  it('Chat in a conversation: the mesh route with in_reply_to, no quote; Interrupt is not offered there', () => {
    const d = { ...selectConversation(draft({ text: 'use the new one' }), conversation), interrupt: true }
    expect(buildSubmission(d, env(d))).toEqual({
      kind: 'chat',
      intent: { route: { kind: 'set' }, recipientIds: ['run:t-1'], inReplyTo: 'm1', body: 'use the new one', interrupt: false },
    })
  })

  it('Restart ignores picked people, the conversation, Interrupt and an attaching helper; a thought is quoted', () => {
    const base = draft({ text: '  fix it  ', picked: ['run:t-2'], interrupt: true, attaching: { profileId: 'p', title: 'P' }, mode: 'restart', modeChosen: true })
    expect(buildSubmission(base, env(base))).toEqual({ kind: 'restart', body: 'fix it' })
    const conv = setMode(selectConversation(base, conversation), 'restart')
    expect(buildSubmission(conv, env(conv))).toEqual({ kind: 'restart', body: 'fix it' })
    const thought = setMode(selectThought(base, thoughtA, true), 'restart')
    expect(buildSubmission(thought, env(thought))).toEqual({ kind: 'restart', body: '> Plan step: ask @reviewer to check the parser\n\nfix it' })
  })

  it('a slash command in Restart is blocked with a way to Chat; in Chat it is a /loop operation', () => {
    const d = draft({ text: '/loop 5m @reviewer ping', mode: 'restart', modeChosen: true })
    expect(buildSubmission(d, env(d))).toEqual({ kind: 'blocked', reason: 'Slash commands run in Chat', switchTo: 'chat' })
    const chat = setMode(selectThought(d, thoughtA, true), 'chat')
    expect(buildSubmission(chat, env(chat))).toEqual({ kind: 'loop' })
  })

  it('a slash command in Restart is blocked without the mesh too, only without the way to Chat', () => {
    const d = draft({ text: '/loop list', mode: 'restart', modeChosen: true })
    expect(buildSubmission(d, env(d, { hasMesh: false }))).toEqual({
      kind: 'blocked', reason: "Slash commands run in Chat. Chat isn't available now — collaboration is off. Your text is kept.",
    })
  })

  it('a closed conversation blocks Chat and keeps the input', () => {
    const d = selectConversation(draft({ text: 'late answer' }), conversation)
    expect(buildSubmission(d, env(d, { conversationClosed: true }))).toEqual({ kind: 'blocked', reason: "Chat can't reply in a closed conversation. Your text is kept." })
  })

  it('an unavailable selected mode is blocked with its reason and an explicit switch, never switched', () => {
    const restart = draft({ text: 'x', mode: 'restart', modeChosen: true })
    expect(buildSubmission(restart, env(restart, { restartBlock: 'the stage has finished' }))).toEqual({
      kind: 'blocked', reason: "Restart with note isn't available now — the stage has finished. Your text is kept.", switchTo: 'chat',
    })
    const chat = draft({ text: 'x', mode: 'chat', modeChosen: true })
    expect(buildSubmission(chat, env(chat, { hasMesh: false }))).toEqual({
      kind: 'blocked', reason: "Chat isn't available now — collaboration is off. Your text is kept.", switchTo: 'restart',
    })
  })

  it('an attaching helper takes the text alone: no reply, no other recipients', () => {
    const d = draft({ text: ' review the parser ', attaching: { profileId: 'p', title: 'Reviewer' } })
    expect(buildSubmission(d, env(d))).toEqual({ kind: 'attach', profileId: 'p', instruction: 'review the parser' })
    const named = { ...d, text: 'review @docs' }
    expect(buildSubmission(named, env(named))).toMatchObject({ kind: 'blocked', reason: expect.stringMatching(/remove the other recipients/) })
    const replying = { ...selectThought(d, thoughtA, true), attaching: d.attaching }
    expect(buildSubmission(replying, env(replying))).toMatchObject({ kind: 'blocked', reason: expect.stringMatching(/remove the reply/) })
  })

  it('empty text or a recipient problem blocks without a reason of its own', () => {
    expect(buildSubmission(draft({ text: '  ' }), env(draft()))).toEqual({ kind: 'blocked', reason: null })
    const d = draft({ text: 'hi @nobody' })
    expect(buildSubmission(d, env(d))).toEqual({ kind: 'blocked', reason: null })
  })

  it('the retry key is the request content only: not the mode, the generation or the recipient order', () => {
    const intent: ChatIntent = { route: { kind: 'stage', mentions: ['run:t-2', 'run:t-1'] }, recipientIds: ['run:t-1', 'run:t-2', 'stage:s1'], inReplyTo: null, body: 'b', interrupt: false }
    expect(chatIntentKey(intent)).toBe(chatIntentKey({ ...intent, route: { kind: 'stage', mentions: ['run:t-1', 'run:t-2'] } }))
    expect(chatIntentKey(intent)).not.toBe(chatIntentKey({ ...intent, body: 'c' }))
    expect(chatIntentKey(intent)).not.toBe(chatIntentKey({ ...intent, interrupt: true }))
    expect(chatIntentKey(intent)).not.toBe(chatIntentKey({ ...intent, inReplyTo: 'm2' }))
    const a = selectThought(draft({ text: 'same' }), thoughtA, true)
    const aba = selectThought(selectThought(a, thoughtB, true), thoughtA, true)
    const keyOf = (d: ComposerDraft) => { const s = buildSubmission(d, env(d)); return s.kind === 'chat' ? chatIntentKey(s.intent) : null }
    expect(keyOf(aba)).toBe(keyOf(a))
    expect(keyOf(setMode(setMode(a, 'restart'), 'chat'))).toBe(keyOf(a))
  })
})

describe('composer draft — the send lock and the retry id', () => {
  it('one operation at a time: a second lock is refused, a stale token releases nothing', () => {
    const locked = takeLock(draft({ text: 'x' }), 'chat', 1)
    expect(takeLock(locked, 'restart', 2)).toBe(locked)
    expect(releaseLock(locked, 2)).toBe(locked)
    expect(releaseLock(locked, 1).inFlight).toBeNull()
  })

  it('a stale settle does not release a newer lock', () => {
    const first = takeLock(draft({ text: 'x' }), 'chat', 1)
    const snap = snapshotSend(first, 'chat', 1)
    const second = takeLock(releaseLock(first, 1), 'restart', 2)
    expect(completeSend(second, snap).draft.inFlight).toEqual({ op: 'restart', token: 2 })
    expect(failSend(second, snap, 'boom').inFlight).toEqual({ op: 'restart', token: 2 })
  })

  it('an unchanged Chat retry reuses its request id; a changed one gets a new id; success forgets it', () => {
    const first = startChat(draft({ text: 'x' }), 'key-1', 1, 'req-1')
    expect(first.unsettled).toEqual({ intentKey: 'key-1', requestId: 'req-1', error: null })
    const failed = failSend(first, snapshotSend(first, 'chat', 1, 'req-1'), 'network down')
    expect(failed).toMatchObject({ inFlight: null, unsettled: { requestId: 'req-1', error: 'network down' } })
    const retry = startChat(failed, 'key-1', 2, 'req-2')
    expect(retry.unsettled).toEqual({ intentKey: 'key-1', requestId: 'req-1', error: null })
    const changed = startChat(failed, 'key-2', 2, 'req-2')
    expect(changed.unsettled?.requestId).toBe('req-2')
    expect(completeSend(retry, snapshotSend(retry, 'chat', 2, 'req-1')).draft.unsettled).toBeNull()
    expect(startChat(retry, 'key-1', 3, 'req-3')).toBe(retry)
  })

  it('a failed reply sent again to another message of the same sender is a new request', () => {
    const keyOf = (d: ComposerDraft) => { const s = buildSubmission(d, env(d)); return s.kind === 'chat' ? chatIntentKey(s.intent) : '' }
    const d = selectConversation(draft({ text: 'same answer' }), conversation)
    const first = startChat(d, keyOf(d), 1, 'req-1')
    const failed = failSend(first, snapshotSend(first, 'chat', 1, 'req-1'), 'network down')
    expect(startChat(failed, keyOf(failed), 2, 'req-2').unsettled?.requestId).toBe('req-1')
    const other = selectConversation(failed, { ...conversation, messageId: 'm2' })
    expect(startChat(other, keyOf(other), 2, 'req-2').unsettled?.requestId).toBe('req-2')
  })
})

describe('composer draft — late results (each race of the spec)', () => {
  it('success clears the sent text and attachments and ends the used context', () => {
    const shot = attachment('a', '/x/a.png')
    const d = selectThought(draft({ text: '[Screenshot: /x/a.png]\nlook', attachments: [shot] }), thoughtA, true)
    const { locked, snap } = sendFrom(d, 'chat', 1)
    const { draft: after, discard } = completeSend(locked, snap)
    expect(after).toMatchObject({ text: '', attachments: [], context: null, inFlight: null, mode: 'chat' })
    expect(after.contextGeneration).toBe(d.contextGeneration + 1)
    expect(discard).toEqual([shot])
  })

  it('failure keeps the text, attachments, context and Interrupt; only the lock goes', () => {
    const shot = attachment('a', '/x/a.png')
    const d = { ...selectThought(draft({ text: '[Screenshot: /x/a.png]\nlook', attachments: [shot] }), thoughtA, true), interrupt: true }
    const { locked, snap } = sendFrom(d, 'chat', 1)
    expect(failSend(locked, snap, 'boom')).toEqual({ ...d, inFlight: null })
  })

  it('race 1: text typed during the request stays, and so does the context', () => {
    const d = selectThought(draft({ text: 'first' }), thoughtA, true)
    const { locked, snap } = sendFrom(d, 'chat', 1)
    const { draft: after } = completeSend({ ...locked, text: 'first and more' }, snap)
    expect(after).toMatchObject({ text: 'and more', context: d.context, mode: 'chat' })
  })

  it('race 2a: a prefix edited during the request keeps the whole text and its attachments', () => {
    const shot = attachment('a', '/x/a.png')
    const d = draft({ text: 'fix the parser [Screenshot: /x/a.png]', attachments: [shot] })
    const { locked, snap } = sendFrom(d, 'chat', 1)
    const { draft: after, discard } = completeSend({ ...locked, text: 'Fix the parser [Screenshot: /x/a.png]' }, snap)
    expect(after).toMatchObject({ text: 'Fix the parser [Screenshot: /x/a.png]', attachments: [shot] })
    expect(discard).toEqual([])
  })

  it('race 2b: a marker an upload inserted at the front keeps the text and the attachments it still names', () => {
    const a = attachment('a', '/x/a.png')
    const b = attachment('b', '/x/b.png')
    const d = draft({ text: 'fix [Screenshot: /x/a.png]', attachments: [a] })
    const { locked, snap } = sendFrom(d, 'restart', 1)
    const edited = { ...locked, text: '[Screenshot: /x/b.png]\nfix [Screenshot: /x/a.png]', attachments: [a, b] }
    const { draft: after, discard } = completeSend(edited, snap)
    expect(after.text).toBe(edited.text)
    expect(after.attachments).toEqual([a, b])
    expect(discard).toEqual([])
  })

  it('a sent attachment whose marker the kept suffix no longer names is discarded; one still named is kept', () => {
    const a = attachment('a', '/x/a.png')
    const b = attachment('b', '/x/b.png')
    expect(attachmentsToDiscard([a, b], 'more [Screenshot: /x/b.png]')).toEqual([a])
  })

  const noReply = (d: ComposerDraft) => d
  const replyA = (d: ComposerDraft) => selectThought(d, thoughtA, true)
  it.each([
    ['no reply → thought', noReply, replyA],
    ['A → B', replyA, (d: ComposerDraft) => selectThought(d, thoughtB, true)],
    ['A → B → A', replyA, (d: ComposerDraft) => selectThought(selectThought(d, thoughtB, true), thoughtA, true)],
    ['cancel → reselect', replyA, (d: ComposerDraft) => selectThought(cancelContext(d), thoughtA, true)],
    ['thought → conversation', replyA, (d: ComposerDraft) => selectConversation(d, conversation)],
  ])('race 3: a context change during the request (%s) keeps the content; only the lock goes', (_name, before, change) => {
    const shot = attachment('a', '/x/a.png')
    const start = before(draft({ text: '[Screenshot: /x/a.png]\nsame', attachments: [shot] }))
    const { locked, snap } = sendFrom(start, 'chat', 1)
    const changed = change(locked)
    const { draft: after, discard } = completeSend(changed, snap)
    expect(after).toEqual({ ...changed, inFlight: null })
    expect(discard).toEqual([])
  })

  it('race 4: switching Chat → Restart during a Chat request keeps the lock; the result never changes the mode', () => {
    const d = draft({ text: 'hello' })
    const { locked, snap } = sendFrom(d, 'chat', 1)
    const switched = setMode(locked, 'restart')
    expect(takeLock(switched, 'restart', 2)).toBe(switched)
    expect(completeSend(switched, snap).draft).toMatchObject({ mode: 'restart', text: '', inFlight: null })
    expect(failSend(switched, snap, 'x')).toMatchObject({ mode: 'restart', text: 'hello', inFlight: null })
  })

  it('race 5: a /loop success removes its command only and never ends the context', () => {
    const d = selectThought(draft({ text: '/loop list' }), thoughtA, true)
    const { locked, snap } = sendFrom(d, 'loop', 1)
    const { draft: after } = completeSend(locked, snap)
    expect(after).toMatchObject({ text: '', context: d.context, contextGeneration: d.contextGeneration })
  })

  it('race 6: a Restart success with a conversation context ends it when nothing new remains', () => {
    const d = setMode(selectConversation(draft({ text: 'restart with this' }), conversation), 'restart')
    const { locked, snap } = sendFrom(d, 'restart', 1)
    expect(completeSend(locked, snap).draft).toMatchObject({ text: '', context: null, mode: 'restart' })
    const kept = completeSend({ ...locked, text: 'restart with this and that' }, snap).draft
    expect(kept).toMatchObject({ text: 'and that', context: d.context })
  })

  it('race 7: a Chat success after a mode switch and a context change still resets Interrupt, keeps the content', () => {
    const d = { ...draft({ text: 'x' }), interrupt: true }
    const { locked, snap } = sendFrom(d, 'chat', 1)
    const changed = setMode(selectThought(locked, thoughtA, true), 'restart')
    expect(completeSend(changed, snap).draft).toEqual({ ...changed, inFlight: null, interrupt: false })
  })

  it('race 8: the sent text is compared raw, not as the body with the quote', () => {
    const d = selectThought(draft({ text: '  spaced  ' }), thoughtA, true)
    const { locked, snap } = sendFrom(d, 'chat', 1)
    expect(snap.text).toBe('  spaced  ')
    expect(completeSend(locked, snap).draft).toMatchObject({ text: '', context: null })
  })

  it('race 9: an attachment pasted during the request keeps the context and the new attachment', () => {
    const b = attachment('b', null)
    const d = selectThought(draft({ text: 'look' }), thoughtA, true)
    const { locked, snap } = sendFrom(d, 'chat', 1)
    const { draft: after } = completeSend({ ...locked, attachments: [b] }, snap)
    expect(after).toMatchObject({ text: '', attachments: [b], context: d.context })
  })

  it('an attach success forgets the attaching helper', () => {
    const d = draft({ text: 'task', attaching: { profileId: 'p', title: 'P' } })
    const { locked, snap } = sendFrom(d, 'attach', 1)
    expect(completeSend(locked, snap).draft).toMatchObject({ text: '', attaching: null })
  })

  it('a late attach success keeps a helper picked since, even the same profile again, and the new text', () => {
    const d = draft({ text: 'task', attaching: { profileId: 'p', title: 'P' } })
    const { locked, snap } = sendFrom(d, 'attach', 1)
    const other = { ...locked, text: 'another task', attaching: { profileId: 'q', title: 'Q' } }
    expect(completeSend(other, snap).draft).toMatchObject({ text: 'another task', attaching: other.attaching, inFlight: null })
    const again = { ...locked, attaching: { profileId: 'p', title: 'P' } }
    expect(completeSend(again, snap).draft).toMatchObject({ text: '', attaching: again.attaching })
  })
})
