import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AfmEvent } from '../../types'
import { FeedWorkspace } from './FeedWorkspace'
import type { FeedComposerMeshProps } from './FeedComposer'
import * as meshClient from '../../api/mesh-client'
import * as runClient from '../../api/run-client'
import type { DiscussionMessageWire } from '../../api/run-client'
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

function meshProps(stages: FeedComposerMeshProps['stages'] = []): FeedComposerMeshProps {
  return { roster: { enabled: true, probed: true, participants, threads: [] }, profiles: [], stages, pick: null }
}

// stageLog — the stage's conversation log with its agent (GET /api/stages/{id}/discussion) the feed also reads.
function stageLog(messages: DiscussionMessageWire[]) {
  return vi.spyOn(runClient, 'listStageDiscussion').mockResolvedValue({ messages, nextCursor: messages.length, hasMore: false })
}

// renderFeed — the stage feed; withComposer — the stage takes messages, so the composer is there to answer with.
function renderFeed({ withComposer = false, reviseAvailable = true }: { withComposer?: boolean; reviseAvailable?: boolean } = {}) {
  vi.spyOn(meshClient, 'getStageMessages').mockResolvedValue({ messages, threads: [withUser, legacy], next_cursor: 6, has_more: false })
  render(
    <FeedWorkspace
      events={events}
      stageId="impl"
      mesh={meshProps()}
      noteTarget={withComposer ? 'impl' : null}
      onSendNote={withComposer ? vi.fn().mockResolvedValue(undefined) : undefined}
      reviseAvailable={reviseAvailable}
    />,
  )
}

const row = (id: string) => document.querySelector(`[data-message-id="${id}"]`) as HTMLElement
const feedText = () => screen.getByRole('region', { name: 'Feed' }).textContent ?? ''

describe('FeedWorkspace — the stage\'s conversations in its feed', () => {
  beforeEach(() => {
    stageLog([])
  })
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

  it('Reply in conversation is offered where the human is a member, and answers the message from the Message composer', async () => {
    const send = vi.spyOn(meshClient, 'sendMeshMessageTo').mockResolvedValue({ message_id: 'm7', seq: 7, thread_id: 'mt-1' })
    renderFeed({ withComposer: true })
    await waitFor(() => expect(row('m1')).not.toBeNull())
    // An older thread the agents opened among themselves is read-only.
    expect(within(row('m4')).queryByRole('button', { name: '↩ Reply in conversation' })).toBeNull()

    fireEvent.click(within(row('m1')).getByRole('button', { name: '↩ Reply in conversation' }))
    expect(row('m1')).toHaveClass('is-reply-target')
    expect(screen.getByRole('tab', { name: 'Message' })).toHaveAttribute('aria-selected', 'true')
    const recipients = screen.getByRole('group', { name: 'Recipients' })
    expect(within(recipients).getByText('@qa')).toBeInTheDocument()
    expect(screen.getByText('Found a bug', { selector: '.feed-reply-chip-src' })).toBeInTheDocument()

    fireEvent.change(screen.getByRole('combobox', { name: /message the mesh/i }), { target: { value: 'which one?' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
    await waitFor(() =>
      expect(send).toHaveBeenCalledWith(expect.objectContaining({ recipientIds: ['run:qa'], inReplyTo: 'm1', body: 'which one?', originStage: 'impl' })),
    )
    // Answered: the reply target goes.
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Cancel reply in conversation' })).toBeNull())
    expect(row('m1')).not.toHaveClass('is-reply-target')
  })

  it('one conversation reply at a time; a Revise reply to a thought is separate and looks different', async () => {
    renderFeed({ withComposer: true })
    await waitFor(() => expect(row('m1')).not.toBeNull())
    fireEvent.click(within(row('m1')).getByRole('button', { name: '↩ Reply in conversation' }))
    fireEvent.click(within(row('m3')).getByRole('button', { name: '↩ Reply in conversation' }))
    expect(row('m1')).not.toHaveClass('is-reply-target')
    expect(row('m3')).toHaveClass('is-reply-target')
    expect(document.querySelectorAll('.feed-reply-chip--conversation')).toHaveLength(1)

    // The thought's reply opens Revise with its own chip; the conversation reply waits in Message.
    fireEvent.click(screen.getAllByRole('button', { name: 'Reply to this thought' })[0] as HTMLElement)
    expect(screen.getByRole('tab', { name: 'Revise' })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('button', { name: 'Cancel reply to thought' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('tab', { name: 'Message' }))
    expect(screen.getByRole('button', { name: 'Cancel reply in conversation' })).toBeInTheDocument()
  })

  it('without Revise there is no Revise tab and no reply to a thought; Message stays', async () => {
    renderFeed({ withComposer: true, reviseAvailable: false })
    await waitFor(() => expect(row('m1')).not.toBeNull())
    expect(screen.queryByRole('tab', { name: 'Revise' })).toBeNull()
    expect(screen.getByRole('tab', { name: 'Message' })).toHaveAttribute('aria-selected', 'true')
    expect(screen.queryByRole('button', { name: 'Reply to this thought' })).toBeNull()
    expect(within(row('m1')).getByRole('button', { name: '↩ Reply in conversation' })).toBeInTheDocument()
  })

  it('Reply in conversation is not offered without a composer', async () => {
    renderFeed()
    await waitFor(() => expect(row('m1')).not.toBeNull())
    expect(within(row('m1')).queryByRole('button', { name: '↩ Reply in conversation' })).toBeNull()
  })

  it('an active Reply ends when its conversation closes', async () => {
    vi.useFakeTimers()
    try {
      vi.spyOn(meshClient, 'getStageMessages')
        .mockResolvedValueOnce({ messages, threads: [withUser, legacy], next_cursor: 6, has_more: false })
        .mockResolvedValue({ messages: [], threads: [{ ...withUser, status: 'closed' }], next_cursor: 6, has_more: false })
      render(<FeedWorkspace events={events} stageId="impl" mesh={meshProps()} noteTarget="impl" onSendNote={vi.fn()} />)
      await act(async () => vi.advanceTimersByTime(0))
      fireEvent.click(within(row('m1')).getByRole('button', { name: '↩ Reply in conversation' }))
      expect(screen.getByRole('button', { name: 'Cancel reply in conversation' })).toBeInTheDocument()

      await act(async () => vi.advanceTimersByTime(3000))
      expect(screen.queryByRole('button', { name: 'Cancel reply in conversation' })).toBeNull()
      expect(row('m1')).not.toHaveClass('is-reply-target')
      expect(within(screen.getByRole('group', { name: 'Recipients' })).queryByText('@qa')).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('Reply in conversation is not offered in a closed conversation', async () => {
    const closed: MeshThread = { id: 'mt-closed', scope: 'run', members: ['user', 'stage:impl', 'run:qa'], status: 'closed' }
    vi.spyOn(meshClient, 'getStageMessages').mockResolvedValue({
      messages: [msg('m9', 2, { thread_id: 'mt-closed', body: 'A closed one' })], threads: [closed], next_cursor: 2, has_more: false,
    })
    render(<FeedWorkspace events={events} stageId="impl" mesh={meshProps()} noteTarget="impl" onSendNote={vi.fn()} />)
    await waitFor(() => expect(row('m9')).not.toBeNull())
    expect(within(row('m9')).queryByRole('button', { name: '↩ Reply in conversation' })).toBeNull()
  })

  it('a post in the stage conversation shows once — as its bubble — and a post only the old log has as its own bubble', async () => {
    const posts = [
      ev('discussion_message', { message_id: 'm5', author_kind: 'human', author_id: 'user', round: 1 }, 7),
      ev('discussion_message', { message_id: 'd-only', author_kind: 'assistant', author_id: 'reviewer', round: 1 }, 8),
    ]
    vi.spyOn(meshClient, 'getStageMessages').mockResolvedValue({
      messages: [msg('m5', 7, { sender_id: 'user', recipient_ids: ['stage:impl'], body: 'Mirrored post' })], threads: [withUser], next_cursor: 7, has_more: false,
    })
    stageLog([
      { id: 'm5', author_kind: 'human', round: 1, body: 'Mirrored post', created_at: at(7) },
      { id: 'd-only', author_kind: 'assistant', author_id: 'reviewer', role: 'reviewer', round: 1, body: 'An old helper verdict', evidence: ['f-1'], created_at: at(8) },
    ])
    render(<FeedWorkspace events={[...events, ...posts]} stageId="impl" mesh={meshProps()} noteTarget="impl" onSendNote={vi.fn()} />)
    await waitFor(() => expect(row('d-only')).not.toBeNull())
    const text = feedText()
    expect(text).not.toContain('discussion_message')
    expect(text).not.toContain('posted in the stage conversation')
    expect(text.split('Mirrored post')).toHaveLength(2)
    // In journal order after the mirrored one, with its author, its text and the evidence it cited; read-only.
    expect(text.indexOf('Mirrored post')).toBeLessThan(text.indexOf('An old helper verdict'))
    expect(within(row('d-only')).getAllByText('reviewer')).toHaveLength(2) // the author and its role
    expect(within(within(row('d-only')).getByRole('list', { name: 'Evidence' })).getByText('f-1')).toBeInTheDocument()
    expect(within(row('d-only')).queryByRole('button', { name: '↩ Reply in conversation' })).toBeNull()
    // All counts what is shown: two events and the two bubbles.
    expect(screen.getByRole('button', { name: /^All/ })).toHaveTextContent('4')
  })

  it('before the log is read, a post without a bubble is a readable line — never the raw event type', async () => {
    vi.spyOn(runClient, 'listStageDiscussion').mockRejectedValue(new Error('offline'))
    vi.spyOn(meshClient, 'getStageMessages').mockResolvedValue({ messages: [msg('m1', 2)], threads: [withUser], next_cursor: 2, has_more: false })
    const post = ev('discussion_message', { message_id: 'd-only', author_kind: 'assistant', author_id: 'reviewer', round: 1 }, 8)
    render(<FeedWorkspace events={[...events, post]} stageId="impl" mesh={meshProps()} />)
    await waitFor(() => expect(row('m1')).not.toBeNull())
    expect(feedText()).toContain('reviewer posted in the stage conversation')
  })

  it('"Not answered" marks your post the finished stage never answered, until a reply arrives', async () => {
    vi.useFakeTimers()
    try {
      vi.spyOn(meshClient, 'getStageMessages').mockResolvedValue({
        messages: [msg('dm-q', 2, { sender_id: 'user', recipient_ids: ['stage:impl'], body: 'Any news?' })], threads: [withUser], next_cursor: 2, has_more: false,
      })
      vi.spyOn(runClient, 'listStageDiscussion')
        .mockResolvedValueOnce({ messages: [{ id: 'dm-q', author_kind: 'human', round: 0, body: 'Any news?', unanswered: true, created_at: at(2) }], nextCursor: 1, hasMore: false })
        .mockResolvedValue({
          messages: [{ id: 'dm-a', author_kind: 'assistant', author_id: 'run:qa', round: 0, parent_message_id: 'dm-q', body: 'Yes', created_at: at(3) }],
          nextCursor: 2,
          hasMore: false,
        })
      const done = [{ id: 'impl', name: 'Implement', status: 'done' }] as unknown as FeedComposerMeshProps['stages']
      render(<FeedWorkspace events={events} stageId="impl" mesh={meshProps(done)} />)
      await act(async () => vi.advanceTimersByTime(0))
      expect(within(row('dm-q')).getByText('Not answered')).toBeInTheDocument()
      await act(async () => vi.advanceTimersByTime(3000))
      expect(row('dm-a')).not.toBeNull()
      expect(screen.queryByText('Not answered')).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it("a log-only post renders like any bubble: the author by name, its recipients, markdown and a result's summary", async () => {
    vi.spyOn(meshClient, 'getStageMessages').mockResolvedValue({ messages: [], threads: [], next_cursor: 0, has_more: false })
    stageLog([
      { id: 'q', author_kind: 'human', round: 0, body: 'ask', mentions: ['run:qa', 'run:gone'], created_at: at(1) },
      { id: 'a', author_kind: 'main_agent', author_id: 'stage:impl', round: 0, body: '# Found an issue\n\nThe **retry loop** never backs off.', created_at: at(2) },
      { id: 'r', author_kind: 'assistant', author_id: 'run:qa', round: 0, body: '{"schema_version":1,"kind":"result","verdict":"pass","summary":"All checks green."}', created_at: at(3) },
    ])
    render(<FeedWorkspace events={[]} stageId="impl" mesh={meshProps()} />)
    await waitFor(() => expect(row('r')).not.toBeNull())
    expect(row('q')).toHaveClass('from-you')
    expect(within(row('q')).getByText('→ Implement, QA reviewer, gone')).toBeInTheDocument()
    expect(within(row('a')).getByText('Implement')).toBeInTheDocument()
    expect(row('a').querySelector('.md h1')).toHaveTextContent('Found an issue')
    expect(row('a').querySelector('.md strong')).toHaveTextContent('retry loop')
    expect(within(row('r')).getByText('All checks green.')).toBeInTheDocument()
    expect(within(row('r')).getByText('Pass')).toBeInTheDocument()
    // Only real messages: nobody who stayed silent gets a synthesized entry.
    expect(document.querySelectorAll('[data-message-id]')).toHaveLength(3)
  })

  it("drafts are per stage: following the run to another stage and back keeps the text, the picks and the Revise note", async () => {
    const stages = [{ id: 'impl', name: 'Implement', status: 'running' }, { id: 'verify', name: 'Verify', status: 'running' }] as unknown as FeedComposerMeshProps['stages']
    const mesh = (pick: FeedComposerMeshProps['pick']) => ({ ...meshProps(stages), pick })
    vi.spyOn(meshClient, 'getStageMessages').mockResolvedValue({ messages, threads: [withUser, legacy], next_cursor: 6, has_more: false })
    const props = { events, mesh: mesh(null), onSendNote: vi.fn().mockResolvedValue(undefined) }
    const { rerender } = render(<FeedWorkspace {...props} stageId="impl" noteTarget="impl" />)
    await waitFor(() => expect(row('m1')).not.toBeNull())
    rerender(<FeedWorkspace {...props} mesh={mesh({ participantId: 'run:qa', seq: 1 })} stageId="impl" noteTarget="impl" />)
    fireEvent.change(screen.getByRole('combobox', { name: /message the mesh/i }), { target: { value: 'half-written' } })
    fireEvent.click(screen.getByRole('tab', { name: 'Revise' }))
    fireEvent.change(screen.getByPlaceholderText('Note to agent…'), { target: { value: 'a note' } })

    rerender(<FeedWorkspace {...props} mesh={mesh({ participantId: 'run:qa', seq: 1 })} stageId="verify" noteTarget="verify" />)
    expect(screen.getByRole('tab', { name: 'Message' })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('combobox', { name: /message the mesh/i })).toHaveValue('')

    rerender(<FeedWorkspace {...props} mesh={mesh({ participantId: 'run:qa', seq: 1 })} stageId="impl" noteTarget="impl" />)
    expect(screen.getByRole('tab', { name: 'Revise' })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByPlaceholderText('Note to agent…')).toHaveValue('a note')
    fireEvent.click(screen.getByRole('tab', { name: 'Message' }))
    expect(screen.getByRole('combobox', { name: /message the mesh/i })).toHaveValue('half-written')
    expect(within(screen.getByRole('group', { name: 'Recipients' })).getByText('@qa')).toBeInTheDocument()
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
    const log = vi.spyOn(runClient, 'listStageDiscussion')
    render(<FeedWorkspace events={events} stageId="impl" />)
    expect(spy).not.toHaveBeenCalled()
    expect(log).not.toHaveBeenCalled()
    expect(screen.queryByRole('group', { name: 'Show in the feed' })).toBeNull()
  })
})
