import { describe, expect, test } from 'vitest'
import type { MeshMessage, MeshParticipant, MeshThread } from '../api/mesh-client'
import { canReplyInConversation, isAgentMessageToUser, latestUnansweredRequest } from './mesh-reply'

const qa: MeshParticipant = { id: 'run:qa', kind: 'run_agent', scope: 'run', display_name: 'QA', reachable: true }
const thread: MeshThread = { id: 'mt-1', scope: 'run', members: ['user', 'run:qa'], status: 'open' }

let seq = 0
const msg = (extra: Partial<MeshMessage>): MeshMessage => ({
  id: `m${++seq}`, thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['user'], kind: 'message', body: 'text', seq, created_at: '', ...extra,
})

describe('isAgentMessageToUser', () => {
  test('an explicit agent message addressed to the human counts', () => {
    expect(isAgentMessageToUser(msg({}))).toBe(true)
    expect(isAgentMessageToUser(msg({ recipient_ids: ['user', 'run:other'] }))).toBe(true)
  })

  test('the human own message, an agent-to-agent message and a passive auto result do not', () => {
    expect(isAgentMessageToUser(msg({ sender_id: 'user', recipient_ids: ['run:qa'] }))).toBe(false)
    expect(isAgentMessageToUser(msg({ recipient_ids: ['run:other'] }))).toBe(false)
    expect(isAgentMessageToUser(msg({ auto_result: true }))).toBe(false)
  })
})

describe('canReplyInConversation', () => {
  test('allows answering an open thread the human is in, written by a reachable author', () => {
    expect(canReplyInConversation(msg({}), thread, [qa])).toBe(true)
  })

  test.each([
    ['own message', msg({ sender_id: 'user' }), thread, [qa]],
    ['unknown thread', msg({}), undefined, [qa]],
    ['closed thread', msg({}), { ...thread, status: 'closed' as const }, [qa]],
    ['human not a member', msg({}), { ...thread, members: ['run:qa', 'run:x'] }, [qa]],
    ['finished author', msg({}), thread, [{ ...qa, reachable: false }]],
    ['author missing from roster', msg({}), thread, []],
  ])('refuses %s', (_name, message, t, people) => {
    expect(canReplyInConversation(message, t, people)).toBe(false)
  })
})

describe('latestUnansweredRequest', () => {
  const latest = (messages: MeshMessage[], t: MeshThread | undefined = thread, people = [qa]) => latestUnansweredRequest(messages, t, people)?.id

  test('picks the newest agent-to-user request', () => {
    const a = msg({ kind: 'request' })
    const b = msg({ kind: 'request' })
    expect(latest([a, b])).toBe(b.id)
  })

  test('ignores plain messages, auto results and requests addressed elsewhere', () => {
    expect(latest([msg({ kind: 'message' }), msg({ kind: 'request', auto_result: true }), msg({ kind: 'request', recipient_ids: ['run:other'] })])).toBeUndefined()
  })

  test('skips a request that already has a reply', () => {
    const a = msg({ kind: 'request' })
    const b = msg({ kind: 'request' })
    const answer = msg({ sender_id: 'user', recipient_ids: ['run:qa'], kind: 'reply', in_reply_to: b.id })
    expect(latest([a, b, answer])).toBe(a.id)
  })

  test('a reply from another thread member does not answer a request addressed to the human', () => {
    const reviewer: MeshParticipant = { id: 'run:rev', kind: 'run_agent', scope: 'run', display_name: 'Reviewer', reachable: true }
    const group: MeshThread = { ...thread, members: ['user', 'run:qa', 'run:rev'] }
    const a = msg({ kind: 'request' })
    const chatter = msg({ sender_id: 'run:rev', recipient_ids: ['run:qa'], kind: 'reply', in_reply_to: a.id })
    expect(latest([a, chatter], group, [qa, reviewer])).toBe(a.id)
  })

  test('a reply from a recipient other than the human answers a request that also addressed that recipient', () => {
    const a = msg({ kind: 'request', recipient_ids: ['user', 'run:rev'] })
    const answer = msg({ sender_id: 'run:rev', recipient_ids: ['run:qa'], kind: 'reply', in_reply_to: a.id })
    expect(latest([a, answer])).toBeUndefined()
  })

  test('skips a request the human has just answered even before the list shows the reply', () => {
    const a = msg({ kind: 'request' })
    const b = msg({ kind: 'request' })
    expect(latestUnansweredRequest([a, b], thread, [qa], new Set([b.id]))?.id).toBe(a.id)
  })

  test('is none when the author is unreachable or the thread closed', () => {
    const a = msg({ kind: 'request' })
    expect(latest([a], thread, [{ ...qa, reachable: false }])).toBeUndefined()
    expect(latest([a], { ...thread, status: 'closed' })).toBeUndefined()
  })
})
