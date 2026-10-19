import { describe, expect, it } from 'vitest'
import { withStageDiscussion } from './stage-discussion-feed'
import type { MeshMessage } from '../../api/mesh-client'
import type { DiscussionMessage } from '../../hooks/use-stage-discussion'

const at = (s: number) => `2026-10-19T10:00:${String(s).padStart(2, '0')}Z`
const mesh = (id: string, s: number, over: Partial<MeshMessage> = {}): MeshMessage => ({
  id, thread_id: 'mt-1', sender_id: 'stage:impl', recipient_ids: ['user'], kind: 'message', body: `mesh ${id}`, seq: s, created_at: at(s), ...over,
})
const post = (id: string, s: number, over: Partial<DiscussionMessage> = {}): DiscussionMessage => ({
  id, authorKind: 'human', round: 0, body: `post ${id}`, createdAt: at(s), ...over,
})

describe('withStageDiscussion — the stage conversation log in the feed', () => {
  it('a post mirrored into the mesh is one message; a post only in the old log becomes its own bubble, in time order', () => {
    const { messages } = withStageDiscussion(
      [mesh('m1', 1), mesh('dm-2', 3, { sender_id: 'user', recipient_ids: ['stage:impl'] }), mesh('m4', 5)],
      [post('dm-2', 3), post('dm-legacy', 4, { authorKind: 'assistant', authorId: 'qaEngineer', body: 'the retry loop is unbounded', evidence: ['f-1', 'f-2'] })],
      'impl',
    )
    expect(messages.map((m) => m.id)).toEqual(['m1', 'dm-2', 'dm-legacy', 'm4'])
    expect(messages[2]).toMatchObject({ sender_id: 'qaEngineer', recipient_ids: [], body: 'the retry loop is unbounded' })
  })

  it("a legacy helper post keeps its role and evidence as data beside the bubble, not inside its text", () => {
    const { messages, legacy } = withStageDiscussion([], [post('h', 1, { authorKind: 'assistant', authorId: 'qa', role: 'reviewer', evidence: ['f-1', 'f-2'], body: 'unbounded' })], 'impl')
    expect(messages[0]?.body).toBe('unbounded')
    expect(legacy.get('h')).toEqual({ role: 'reviewer', evidence: ['f-1', 'f-2'] })
  })

  it('the journal seq orders the two readers when both have it — not the clock', () => {
    // The legacy post was journaled between m1 and m2, although its timestamp says otherwise.
    const { messages } = withStageDiscussion(
      [mesh('m1', 1, { seq: 10 }), mesh('m2', 2, { seq: 30 })],
      [post('dm-x', 9, { seq: 20 })],
      'impl',
    )
    expect(messages.map((m) => m.id)).toEqual(['m1', 'dm-x', 'm2'])
  })

  it("a legacy agent reply to your post is addressed to you, so Author's work still shows it", () => {
    const { messages } = withStageDiscussion(
      [mesh('dm-q', 1, { sender_id: 'user', recipient_ids: ['stage:impl'] })],
      [post('dm-q', 1), post('a1', 2, { authorKind: 'main_agent', parentMessageId: 'dm-q' }), post('a2', 3, { authorKind: 'assistant', authorId: 'qa', parentMessageId: 'a1' })],
      'impl',
    )
    expect(messages.find((m) => m.id === 'a1')?.recipient_ids).toEqual(['user'])
    expect(messages.find((m) => m.id === 'a2')?.recipient_ids).toEqual([])
  })

  it('a legacy post names its real author and recipients; it is in no thread you can reply to', () => {
    const { messages } = withStageDiscussion(
      [],
      [
        post('q', 1, { mentions: ['run:t-1'] }),
        post('a', 2, { authorKind: 'main_agent', parentMessageId: 'q', autoResult: true }),
        post('s', 3, { authorKind: 'afm' }),
      ],
      'impl',
    )
    expect(messages.map((m) => [m.sender_id, m.recipient_ids, m.in_reply_to, m.auto_result])).toEqual([
      ['user', ['stage:impl', 'run:t-1'], undefined, undefined],
      ['stage:impl', ['user'], 'q', true],
      ['afm', [], undefined, undefined],
    ])
    expect(new Set(messages.map((m) => m.thread_id))).toEqual(new Set(['discussion:impl']))
  })

  it('"Not answered": the posts the server marked, unless a reply has arrived since — in the log or in the mesh', () => {
    const { unanswered } = withStageDiscussion(
      [mesh('r', 5, { in_reply_to: 'dm-2' }), mesh('mine', 6, { sender_id: 'user', in_reply_to: 'dm-3' })],
      [post('dm-1', 1, { unanswered: true }), post('dm-2', 2, { unanswered: true }), post('dm-3', 3, { unanswered: true })],
      'impl',
    )
    expect([...unanswered].sort()).toEqual(['dm-1', 'dm-3'])
  })

  it('no log — the conversation messages as they are', () => {
    const messages = [mesh('m1', 1)]
    expect(withStageDiscussion(messages, [], 'impl').messages).toBe(messages)
  })
})
