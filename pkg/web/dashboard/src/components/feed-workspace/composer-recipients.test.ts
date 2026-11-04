import { describe, expect, it } from 'vitest'
import { awaitingAgentNeedsRestart, composerRecipients, FINISHED_STAGE_NOTE, messageRoute, recipientProblem, resolveTyped, typedMentionHandles, withoutRecipient } from './composer-recipients'
import { mentionHandles } from '../../lib/mesh-roster'
import type { MeshParticipant } from '../../api/mesh-client'

const impl: MeshParticipant = { id: 'stage:impl', kind: 'stage_agent', scope: 'stage', stage_id: 'impl', alias: 'impl', display_name: 'Backend', reachable: true }
const docs: MeshParticipant = { id: 'stage:docs', kind: 'stage_agent', scope: 'stage', stage_id: 'docs', alias: 'docs', reachable: true }
const qa: MeshParticipant = { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA', reachable: true }
const done: MeshParticipant = {
  id: 'run:t-2', kind: 'run_agent', scope: 'run', alias: 'old', reachable: false, unreachable_reason: 'task t-2 has reached a terminal status',
}
const user: MeshParticipant = { id: 'user', kind: 'user', scope: 'run', alias: 'user', reachable: true }
const participants = [user, impl, docs, qa, done]
const running = [{ id: 'impl', status: 'running' }, { id: 'docs', status: 'running' }]

describe('typedMentionHandles', () => {
  it('finds whole @handle tokens, without trailing punctuation, and ignores e-mail addresses', () => {
    expect(typedMentionHandles('@qa, and @run:t-9. mail me at a@b.c (@docs)')).toEqual(['qa', 'run:t-9'])
    expect(typedMentionHandles('@qa\n@docs!')).toEqual(['qa', 'docs'])
  })
})

describe('resolveTyped / recipientProblem — shared with the /loop target', () => {
  it('resolves an alias or id case-insensitively and reports a duplicated alias as ambiguous', () => {
    const twin: MeshParticipant = { ...qa, id: 'run:t-7' }
    expect(resolveTyped('QA', participants, mentionHandles(participants))).toBe(qa)
    expect(resolveTyped('run:t-1', participants, mentionHandles(participants))).toBe(qa)
    expect(resolveTyped('nobody', participants, mentionHandles(participants))).toBeNull()
    expect(resolveTyped('qa', [qa, twin], mentionHandles([qa, twin]))).toBe('ambiguous')
  })
  it('explains why an unreachable person cannot be written to', () => {
    expect(recipientProblem(qa, 'qa', [])).toBeNull()
    expect(recipientProblem(done, 'old', [])).toBe("@old can't receive messages: task t-2 has reached a terminal status")
  })
})

describe('composerRecipients', () => {
  it('without anyone named, the message goes to the stage agent by default', () => {
    const r = composerRecipients({ participants, stages: running, stageId: 'impl', picked: [], draft: 'hello' })
    expect(r.recipients).toEqual([{ key: 'stage:impl', handle: 'impl', participantId: 'stage:impl', source: 'default', picked: false, typedTokens: [], problem: null, note: null }])
    expect(r.defaultProblem).toBeNull()
  })

  it('picked and typed recipients together, each person once, picked first', () => {
    const r = composerRecipients({ participants, stages: running, stageId: 'impl', picked: ['run:t-1'], draft: '@docs and @QA please' })
    expect(r.recipients.map((x) => [x.participantId, x.source])).toEqual([
      ['run:t-1', 'picked'],
      ['stage:docs', 'typed'],
    ])
  })

  it('an unknown or unreachable name is an error with its reason — never dropped or redirected', () => {
    const r = composerRecipients({ participants, stages: running, stageId: 'impl', picked: [], draft: '@nobody @old hi' })
    expect(r.recipients.map((x) => x.problem)).toEqual([
      'No one is called @nobody in this run',
      "@old can't receive messages: task t-2 has reached a terminal status",
    ])
  })

  it('a stage agent that waits for its helpers reads only a Revise; it is not the default then', () => {
    const waiting = [{ id: 'impl', status: 'awaiting_assistants' }]
    const typed = composerRecipients({ participants, stages: waiting, stageId: 'impl', picked: [], draft: '@impl hi' })
    expect(typed.recipients[0]?.problem).toMatch(/reads a note only through Restart with note/)
    const implicit = composerRecipients({ participants, stages: waiting, stageId: 'impl', picked: [], draft: 'hi' })
    expect(implicit.recipients).toEqual([])
    expect(implicit.defaultProblem).toMatch(/reads a note only through Restart with note/)
  })

  it('a finished stage agent is no default: a recipient is required', () => {
    const finished = { ...impl, reachable: false, unreachable_reason: 'stage impl has reached a terminal status' }
    const r = composerRecipients({ participants: [user, finished, qa], stages: [{ id: 'impl', status: 'done' }], stageId: 'impl', picked: [], draft: 'hi' })
    expect(r.recipients).toEqual([])
    expect(r.defaultProblem).toMatch(/terminal status/)
  })

  it('an alias two people share is ambiguous; @user is you', () => {
    const twin: MeshParticipant = { ...qa, id: 'run:t-3' }
    const r = composerRecipients({ participants: [...participants, twin], stages: running, stageId: 'impl', picked: [], draft: '@qa @user' })
    expect(r.recipients.map((x) => x.problem)).toEqual(['@qa matches several people — pick one from the list', "@user is you — you can't write to yourself"])
  })
})

describe('composerRecipients — chips that must not silently change who gets the message', () => {
  it('a picked person who left the roster stays as a problem chip; the stage agent does not take over', () => {
    const r = composerRecipients({ participants, stages: running, stageId: 'impl', picked: ['run:gone'], draft: 'hi' })
    expect(r.recipients).toEqual([
      { key: 'run:gone', handle: 'run:gone', participantId: null, source: 'picked', picked: true, typedTokens: [], problem: '@run:gone is no longer available in this run', note: null },
    ])
  })

  it('a chip remembers every token that named it, as typed — a full id shown by its alias included', () => {
    const r = composerRecipients({ participants, stages: running, stageId: 'impl', picked: ['stage:docs'], draft: '@stage:docs and @DOCS, again' })
    expect(r.recipients).toHaveLength(1)
    expect(r.recipients[0]).toMatchObject({ key: 'stage:docs', handle: 'docs', source: 'picked', picked: true, typedTokens: ['stage:docs', 'DOCS'] })
  })

  it('answering a message: its sender comes first, from the reply alone', () => {
    const r = composerRecipients({ participants, stages: running, stageId: 'impl', picked: [], draft: '@qa ok', replyTo: 'stage:docs' })
    expect(r.recipients.map((x) => [x.participantId, x.source])).toEqual([
      ['stage:docs', 'reply'],
      ['run:t-1', 'typed'],
    ])
  })
})

describe("composerRecipients — the feed stage's finished agent", () => {
  const finishedImpl = { ...impl, reachable: false, unreachable_reason: 'stage impl has reached a terminal status' }
  const doneStages = [{ id: 'impl', status: 'done' }, { id: 'docs', status: 'running' }]

  it('named explicitly it is a recorded post, not an error; other stages\' finished agents still are', () => {
    const r = composerRecipients({ participants: [user, finishedImpl, docs, qa], stages: doneStages, stageId: 'impl', picked: [], draft: '@impl @qa recap please' })
    expect(r.recipients.map((x) => [x.participantId, x.problem, x.note])).toEqual([
      ['stage:impl', null, 'finished — post recorded, not read'],
      ['run:t-1', null, null],
    ])
    const fromDocs = composerRecipients({ participants: [user, finishedImpl, docs], stages: doneStages, stageId: 'docs', picked: [], draft: '@impl hi' })
    expect(fromDocs.recipients[0]?.problem).toMatch(/terminal status/)
  })

  it('in a reply it is an ordinary finished recipient, and anyone outside the reply thread is an error', () => {
    const r = composerRecipients({
      participants: [user, finishedImpl, docs, qa], stages: doneStages, stageId: 'impl', picked: [], draft: '@impl @qa ok',
      replyTo: 'stage:docs', replyMembers: ['user', 'stage:docs', 'stage:impl'],
    })
    expect(r.recipients.map((x) => [x.participantId, x.problem, x.note])).toEqual([
      ['stage:docs', null, null],
      ['stage:impl', "@impl can't receive messages: stage impl has reached a terminal status", null],
      ['run:t-1', '@qa is not in this conversation — a reply goes only to its members', null],
    ])
  })

  it('is never the default: with nobody named a recipient is still required', () => {
    const r = composerRecipients({ participants: [user, finishedImpl, qa], stages: doneStages, stageId: 'impl', picked: [], draft: 'hi' })
    expect(r.recipients).toEqual([])
    expect(r.defaultProblem).toMatch(/terminal status/)
  })
})

describe('withoutRecipient', () => {
  it('removes every source of a chip: the pick and each typed token naming it', () => {
    const r = composerRecipients({ participants, stages: running, stageId: 'impl', picked: ['stage:docs'], draft: '@stage:docs and @docs please' })
    const chip = r.recipients[0]
    if (chip === undefined) throw new Error('no chip')
    expect(withoutRecipient(chip, { picked: ['stage:docs', 'run:t-1'], draft: '@stage:docs and @docs please' })).toEqual({ picked: ['run:t-1'], draft: 'and please' })
  })
})

describe('messageRoute', () => {
  it("a new message naming the stage's own agent goes to the stage conversation, the others joining it", () => {
    expect(messageRoute('impl', ['run:t-1', 'stage:impl', 'stage:docs'], false)).toEqual({ kind: 'stage', mentions: ['run:t-1', 'stage:docs'] })
    expect(messageRoute('impl', ['stage:impl'], false)).toEqual({ kind: 'stage', mentions: [] })
  })

  it('a message without the stage agent, or any reply, goes to exactly its recipients', () => {
    expect(messageRoute('impl', ['run:t-1', 'stage:docs'], false)).toEqual({ kind: 'set' })
    expect(messageRoute('impl', ['stage:impl'], true)).toEqual({ kind: 'set' })
  })
})

describe("the stage agent as a required recipient of a thought reply", () => {
  const awaiting = [{ id: 'impl', status: 'awaiting_assistants' }, { id: 'docs', status: 'running' }]
  const thoughtReply = { replyTo: 'stage:impl', replyMembers: null }
  const doneStages = [{ id: 'impl', status: 'done' }, { id: 'docs', status: 'running' }]

  it('is a chip from the reply, not a default', () => {
    const r = composerRecipients({ participants, stages: running, stageId: 'impl', picked: [], draft: 'why?', ...thoughtReply })
    expect(r.recipients.map((x) => [x.participantId, x.source, x.problem, x.note])).toEqual([['stage:impl', 'reply', null, null]])
    expect(r.defaultProblem).toBeNull()
  })

  it('carries the awaiting_assistants problem while the agent waits for its helpers', () => {
    const r = composerRecipients({ participants, stages: awaiting, stageId: 'impl', picked: [], draft: 'why?', ...thoughtReply })
    expect(r.recipients[0]?.problem).toMatch(/waits for its helpers/)
  })

  it('is marked finished, not an error, once the stage is done', () => {
    const r = composerRecipients({ participants: [user, { ...impl, reachable: false }, qa], stages: doneStages, stageId: 'impl', picked: [], draft: 'why?', ...thoughtReply })
    expect(r.recipients.map((x) => [x.source, x.problem, x.note])).toEqual([['reply', null, FINISHED_STAGE_NOTE]])
  })

  it('routes to the stage conversation with the other recipients as mentions', () => {
    const r = composerRecipients({ participants, stages: running, stageId: 'impl', picked: [], draft: '@qa why?', ...thoughtReply })
    const ids = r.recipients.map((x) => x.participantId ?? x.key)
    expect(ids).toEqual(['stage:impl', 'run:t-1'])
    expect(messageRoute('impl', ids, false)).toEqual({ kind: 'stage', mentions: ['run:t-1'] })
  })
})

describe('awaitingAgentNeedsRestart', () => {
  const awaiting = [{ id: 'impl', status: 'awaiting_assistants' }, { id: 'docs', status: 'running' }]
  const doneStages = [{ id: 'impl', status: 'done' }, { id: 'docs', status: 'running' }]
  const needs = (draft: string, stages: { id: string; status: string }[], extra: Partial<Parameters<typeof composerRecipients>[0]> = {}): boolean => {
    const { recipients, defaultProblem } = composerRecipients({ participants, stages, stageId: 'impl', picked: [], draft, ...extra })
    return awaitingAgentNeedsRestart({ recipients, defaultProblem, stageId: 'impl', stages })
  }

  it('is true when the waiting agent would be the default recipient', () => {
    expect(needs('hi', awaiting)).toBe(true)
  })

  it('stays true when the agent is a recipient of a thought reply', () => {
    expect(needs('hi', awaiting, { replyTo: 'stage:impl', replyMembers: null })).toBe(true)
  })

  it('stays true when the agent is named in the text or picked', () => {
    expect(needs('@impl hi', awaiting)).toBe(true)
    expect(needs('hi', awaiting, { picked: ['stage:impl'] })).toBe(true)
  })

  it('is false when only other people are addressed', () => {
    expect(needs('@qa hi', awaiting)).toBe(false)
    expect(needs('hi', awaiting, { picked: ['stage:docs'] })).toBe(false)
  })

  it('is false for any other stage status', () => {
    expect(needs('hi', running)).toBe(false)
    expect(needs('hi', doneStages)).toBe(false)
    expect(needs('@impl hi', [{ id: 'impl', status: 'paused' }])).toBe(false)
  })
})
