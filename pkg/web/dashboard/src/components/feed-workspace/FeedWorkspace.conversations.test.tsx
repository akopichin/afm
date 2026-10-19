import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { AfmEvent } from '../../types'
import { FeedWorkspace } from './FeedWorkspace'
import type { FeedComposerMeshProps } from './FeedComposer'
import * as meshClient from '../../api/mesh-client'
import type { MeshMessage, MeshParticipant, MeshThread } from '../../api/mesh-client'

const at = (s: number) => `2026-10-19T10:00:${String(s).padStart(2, '0')}Z`
const ev = (type: string, payload: unknown, s: number): AfmEvent => ({ type, payload, stageId: 'impl', timestamp: at(s) }) as AfmEvent

const participants: MeshParticipant[] = [
  { id: 'user', kind: 'user', scope: 'run', display_name: 'You', reachable: true },
  { id: 'stage:impl', kind: 'stage_agent', scope: 'stage', alias: 'impl', display_name: 'Implement', stage_id: 'impl', reachable: true, command: 'claude' },
  { id: 'run:qa', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA reviewer', reachable: true, command: 'codex', activity: 'working' },
  { id: 'run:old', kind: 'run_agent', scope: 'run', alias: 'old', display_name: 'Old agent', reachable: true },
]
const withUser: MeshThread = { id: 'mt-1', scope: 'run', members: ['user', 'stage:impl', 'run:qa'], status: 'open' }
const legacy: MeshThread = { id: 'mt-old', scope: 'run', members: ['stage:impl', 'run:old'], status: 'open' }
const msg = (id: string, s: number, over: Partial<MeshMessage> = {}): MeshMessage => ({
  id, thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['stage:impl'], kind: 'message', body: `body ${id}`, seq: s, created_at: at(s), ...over,
})

const events = [ev('agent_action', { tool: 'text', detail: 'Started the work' }, 1), ev('agent_action', { tool: 'text', detail: 'Finished the work' }, 5)]
const messages = [
  msg('m1', 2, { body: 'Found a bug' }),
  msg('m2', 3, { sender_id: 'stage:impl', recipient_ids: ['run:qa'], in_reply_to: 'm1', body: 'Fixing it' }),
  msg('m3', 4, { recipient_ids: ['user'], in_reply_to: 'm1', body: 'Please decide' }),
  msg('m4', 6, { thread_id: 'mt-old', sender_id: 'run:old', body: 'An old conversation' }),
]

function meshProps(): FeedComposerMeshProps {
  return { roster: { enabled: true, probed: true, participants, threads: [] }, profiles: [], stages: [], pick: null }
}

function renderFeed(onReplyInConversation?: (m: MeshMessage) => void) {
  vi.spyOn(meshClient, 'getStageMessages').mockResolvedValue({ messages, threads: [withUser, legacy], next_cursor: 6, has_more: false })
  render(<FeedWorkspace events={events} stageId="impl" mesh={meshProps()} onReplyInConversation={onReplyInConversation} />)
}

const row = (id: string) => document.querySelector(`[data-message-id="${id}"]`) as HTMLElement
const feedText = () => screen.getByRole('region', { name: 'Feed' }).textContent ?? ''

describe('FeedWorkspace — the stage\'s conversations in its feed', () => {
  afterEach(() => vi.restoreAllMocks())

  it('interleaves conversation messages with the stage events in time order', async () => {
    renderFeed()
    await waitFor(() => expect(row('m1')).not.toBeNull())
    const text = feedText()
    const order = ['Started the work', 'Found a bug', 'Fixing it', 'Please decide', 'Finished the work', 'An old conversation'].map((t) => text.indexOf(t))
    expect(order).toEqual([...order].sort((a, b) => a - b))
    expect(order.every((i) => i >= 0)).toBe(true)
    // The shared bubble: author, the command it runs, recipients, the quote of the replied message.
    const reply = row('m2')
    expect(within(reply).getByText('claude')).toBeInTheDocument()
    expect(within(reply).getByText('Replying to QA reviewer')).toBeInTheDocument()
  })

  it('filters: Conversations hides the author\'s work, Author\'s work keeps only what is addressed to you', async () => {
    renderFeed()
    await waitFor(() => expect(row('m1')).not.toBeNull())
    expect(screen.getByRole('button', { name: /^All/ })).toHaveTextContent('6')
    expect(screen.getByRole('button', { name: /^Conversations/ })).toHaveTextContent('4')
    // Author's work: the two events and the message addressed to you — exactly what the filter shows.
    expect(screen.getByRole('button', { name: /^Author's work/ })).toHaveTextContent('3')

    fireEvent.click(screen.getByRole('button', { name: /^Conversations/ }))
    expect(screen.queryByText('Started the work')).toBeNull()
    expect(row('m1')).not.toBeNull()

    fireEvent.click(screen.getByRole('button', { name: /^Author's work/ }))
    expect(screen.getByText('Started the work')).toBeInTheDocument()
    expect(row('m1')).toBeNull()
    expect(screen.getByText('Please decide')).toBeInTheDocument() // addressed to you: always visible
  })

  it('Reply in conversation is offered where the human is a member, and hands the message to the composer', async () => {
    const onReply = vi.fn()
    renderFeed(onReply)
    await waitFor(() => expect(row('m1')).not.toBeNull())
    fireEvent.click(within(row('m1')).getByRole('button', { name: '↩ Reply in conversation' }))
    expect(onReply).toHaveBeenCalledWith(expect.objectContaining({ id: 'm1' }))
    // An older thread the agents opened among themselves is read-only.
    expect(within(row('m4')).queryByRole('button', { name: '↩ Reply in conversation' })).toBeNull()
  })

  it('Reply in conversation is not offered in a closed conversation', async () => {
    const closed: MeshThread = { id: 'mt-closed', scope: 'run', members: ['user', 'stage:impl', 'run:qa'], status: 'closed' }
    vi.spyOn(meshClient, 'getStageMessages').mockResolvedValue({
      messages: [msg('m9', 2, { thread_id: 'mt-closed', body: 'A closed one' })], threads: [closed], next_cursor: 2, has_more: false,
    })
    render(<FeedWorkspace events={events} stageId="impl" mesh={meshProps()} onReplyInConversation={vi.fn()} />)
    await waitFor(() => expect(row('m9')).not.toBeNull())
    expect(within(row('m9')).queryByRole('button', { name: '↩ Reply in conversation' })).toBeNull()
  })

  it('a Discussion post shows once — as its conversation bubble — and a post without one as a readable line', async () => {
    const posts = [
      ev('discussion_message', { message_id: 'm5', author_kind: 'human', author_id: 'user', round: 1 }, 7),
      ev('discussion_message', { message_id: 'd-only', author_kind: 'assistant', author_id: 'reviewer', round: 1 }, 8),
    ]
    vi.spyOn(meshClient, 'getStageMessages').mockResolvedValue({
      messages: [msg('m5', 7, { sender_id: 'user', recipient_ids: ['stage:impl'], body: 'Mirrored post' })], threads: [withUser], next_cursor: 7, has_more: false,
    })
    render(<FeedWorkspace events={[...events, ...posts]} stageId="impl" mesh={meshProps()} />)
    await waitFor(() => expect(row('m5')).not.toBeNull())
    const text = feedText()
    expect(text).not.toContain('discussion_message')
    expect(text.split('Mirrored post')).toHaveLength(2)
    expect(text).toContain('reviewer posted in the Discussion')
    expect(text).not.toContain('You posted in the Discussion')
    // All counts what is shown: two events, the bubble and the post without one.
    expect(screen.getByRole('button', { name: /^All/ })).toHaveTextContent('4')
  })

  it('a click on the quote jumps to the quoted message, even from a filter that hides it', async () => {
    renderFeed()
    await waitFor(() => expect(row('m1')).not.toBeNull())
    fireEvent.click(screen.getByRole('button', { name: /^Author's work/ }))
    expect(row('m1')).toBeNull()
    fireEvent.click(within(row('m3')).getByTitle('Show the quoted message'))
    await waitFor(() => expect(row('m1')).toHaveClass('is-jump-target'))
  })

  it('shows who is working right now', async () => {
    renderFeed()
    expect(await screen.findByText('@qa is working')).toBeInTheDocument()
  })

  it('without the mesh (the Full feed) there are no conversations and no requests', () => {
    const spy = vi.spyOn(meshClient, 'getStageMessages')
    render(<FeedWorkspace events={events} stageId="impl" />)
    expect(spy).not.toHaveBeenCalled()
    expect(screen.queryByRole('group', { name: 'Show in the feed' })).toBeNull()
  })
})
