import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { useState } from 'react'
import { conversationSubtitle, ThreadConversation, type RunConversationMode } from './ThreadConversation'
import * as meshClient from '../../api/mesh-client'
import type { MeshMessage, MeshParticipant, MeshSendReceipt, MeshThread } from '../../api/mesh-client'

const qa: MeshParticipant = { id: 'run:qa', kind: 'run_agent', scope: 'run', display_name: 'QA reviewer', reachable: true }
const thread: MeshThread = { id: 'mt-1', title: 'Hand-off', scope: 'run', members: ['user', 'run:qa'], status: 'open' }

describe('ThreadConversation', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('shows a linked side failure beside the original message and keeps its failure body visible', async () => {
    const side: MeshParticipant = { id: 'side:r1', kind: 'side_agent', scope: 'run', display_name: 'Side agent', reachable: true }
    const sideThread: MeshThread = { id: 'mt-side', scope: 'run', members: ['user', 'side:r1'], status: 'open' }
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [
      { id: 'm1', thread_id: 'mt-side', sender_id: 'user', recipient_ids: ['side:r1'], kind: 'message', body: 'help', seq: 1, created_at: '' },
      { id: 'm2', thread_id: 'mt-side', sender_id: 'side:r1', recipient_ids: ['user'], kind: 'result', auto_result: true, in_reply_to: 'm1', body: 'The agent failed to answer', seq: 2, created_at: '' },
    ], next_cursor: 2 })
    render(<ThreadConversation target={{ kind: 'thread', thread: sideThread }} participants={[side]} />)
    await waitFor(() => expect(screen.getByText('Could not answer')).toBeInTheDocument())
    expect(document.querySelector('[data-message-id="m1"]')).toHaveTextContent('Could not answer')
    expect(screen.getByText('The agent failed to answer')).toBeInTheDocument()
  })

  test('null target shows a neutral empty state', () => {
    render(<ThreadConversation target={null} />)
    expect(screen.getByText(/select a person or a conversation/i)).toBeInTheDocument()
  })

  test('a direct target with no thread yet shows the person label and no messages', () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    render(<ThreadConversation target={{ kind: 'direct', participant: qa }} />)
    expect(screen.getByPlaceholderText('Message QA reviewer…')).toBeInTheDocument()
    expect(screen.getByText('No messages yet')).toBeInTheDocument()
  })

  test('a titled conversation identifies its participants beneath the topic', () => {
    expect(conversationSubtitle({ kind: 'thread', thread }, [qa])).toContain('QA reviewer')
    const group: MeshThread = { ...thread, members: ['user', 'run:qa', 'stage:implement'] }
    expect(conversationSubtitle({ kind: 'thread', thread: group }, [qa])).toContain('QA reviewer')
  })

  test('the composer names members under a titled discussion', () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const api: MeshParticipant = { id: 'stage:api', kind: 'stage_agent', scope: 'stage', display_name: 'API reviewer', reachable: true }
    const group: MeshThread = { ...thread, members: ['user', qa.id, api.id] }
    render(<ThreadConversation target={{ kind: 'thread', thread: group }} participants={[qa, api]} />)
    expect(screen.getByText(/Everyone in this conversation · QA reviewer, API reviewer/)).toBeInTheDocument()
  })

  test('the empty state sits in the same messages area as messages do, so the composer never jumps', () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    render(<ThreadConversation target={{ kind: 'direct', participant: qa }} />)
    const log = screen.getByRole('log', { name: 'Messages' })
    expect(log).toHaveTextContent('No messages yet')
  })

  test('an untitled direct thread is named after the other member, never its mt- id', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const untitled: MeshThread = { id: 'mt-e95e00425b9b4640a0bc', title: '', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    render(<ThreadConversation target={{ kind: 'thread', thread: untitled }} participants={[qa]} />)
    expect(screen.getByPlaceholderText('Message QA reviewer…')).toBeInTheDocument()
    expect(screen.queryByText(/mt-e95e/)).toBeNull()
  })

  test('messages are signed with the sender name, not the participant id', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({
      messages: [
        { id: 'm0', thread_id: 'mt-1', sender_id: 'user', recipient_ids: ['run:qa'], kind: 'message', body: 'hi', seq: 1, created_at: '2026-10-18T00:00:00Z' },
        { id: 'm1', thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['user'], kind: 'reply', body: 'hello', seq: 2, created_at: '2026-10-18T00:00:01Z' },
      ],
      next_cursor: 2,
    })
    render(<ThreadConversation target={{ kind: 'thread', thread }} participants={[qa]} />)
    await waitFor(() => expect(screen.getByText('hello')).toBeInTheDocument())
    expect(screen.getByText('You')).toBeInTheDocument()
    expect(screen.getAllByText('QA reviewer').length).toBeGreaterThan(0)
    expect(screen.getByText('→ QA reviewer')).toBeInTheDocument()
    expect(screen.queryByText(/run:qa/)).toBeNull()
  })

  test('a pasted JSON result shows as its summary with a verdict chip, never raw JSON', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({
      messages: [
        {
          id: 'm1', thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['user'], kind: 'reply', seq: 1, created_at: '2026-10-18T00:00:01Z',
          body: '{"schema_version":1,"kind":"result","verdict":"inconclusive","summary":"Could not run the e2e suite."}',
        },
      ],
      next_cursor: 1,
    })
    render(<ThreadConversation target={{ kind: 'thread', thread }} participants={[qa]} />)
    await waitFor(() => expect(screen.getByText('Could not run the e2e suite.')).toBeInTheDocument())
    expect(screen.getByText('Inconclusive')).toBeInTheDocument()
    // The raw JSON is only inside the collapsed "Raw" details, never the visible text.
    expect(screen.getByText(/schema_version/).closest('details')).not.toBeNull()
  })

  test("an agent's turn result afm posted carries the turn result tag", async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({
      messages: [
        { id: 'm1', thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['user'], kind: 'reply', body: 'checked it', auto_result: true, seq: 1, created_at: '2026-10-18T00:00:01Z' },
      ],
      next_cursor: 1,
    })
    render(<ThreadConversation target={{ kind: 'thread', thread }} participants={[qa]} />)
    await waitFor(() => expect(screen.getByText('checked it')).toBeInTheDocument())
    expect(screen.getByText('turn result')).toBeInTheDocument()
  })

  test('a reply quotes the message it answers, inside its bubble', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({
      messages: [
        { id: 'm0', thread_id: 'mt-1', sender_id: 'user', recipient_ids: ['run:qa'], kind: 'message', body: 'Prepare checkout tests', seq: 1, created_at: '2026-10-18T00:00:00Z' },
        { id: 'm1', thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['user'], kind: 'reply', in_reply_to: 'm0', body: 'Got it', seq: 2, created_at: '2026-10-18T00:00:01Z' },
      ],
      next_cursor: 2,
    })
    render(<ThreadConversation target={{ kind: 'thread', thread }} participants={[qa]} />)
    await waitFor(() => expect(screen.getByText('Replying to You')).toBeInTheDocument())
    const quote = screen.getByText('Replying to You').closest('blockquote') as HTMLElement
    expect(quote).toHaveTextContent('Prepare checkout tests')
    expect(quote.closest('.team-conversation-message-body')).toHaveTextContent('Got it')
  })

  test('a conversation agents started between themselves is read-only for you', () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const agentsOnly: MeshThread = { id: 'mt-7', title: '', scope: 'run', members: ['run:qa', 'stage:implement'], status: 'open' }
    render(<ThreadConversation target={{ kind: 'thread', thread: agentsOnly }} participants={[qa]} />)
    expect(screen.getByText(/talking between themselves/)).toBeInTheDocument()
    expect(screen.queryByRole('combobox')).toBeNull()
  })

  test('a conversation opens at its latest message, not at the top', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({
      messages: [{ id: 'm0', thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['user'], kind: 'reply', body: 'latest', seq: 1, created_at: '2026-10-18T00:00:00Z' }],
      next_cursor: 1,
    })
    vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockReturnValue(900)
    render(<ThreadConversation target={{ kind: 'thread', thread }} participants={[qa]} />)
    await waitFor(() => expect(screen.getByText('latest')).toBeInTheDocument())
    await waitFor(() => expect(screen.getByRole('log', { name: 'Messages' }).scrollTop).toBe(900))
  })

  test('a direct target reuses a direct thread the roster learned about after the click instead of opening a second one', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const openSpy = vi.spyOn(meshClient, 'openMeshThread')
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })
    const onThreadOpened = vi.fn()
    const { rerender } = render(<ThreadConversation target={{ kind: 'direct', participant: qa }} onThreadOpened={onThreadOpened} />)
    // The roster poll catches up with a direct thread opened elsewhere (e.g. from the feed).
    const untitled = { ...thread, title: '' }
    rerender(<ThreadConversation target={{ kind: 'direct', participant: qa }} threads={[thread, untitled]} onThreadOpened={onThreadOpened} />)
    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'hi' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(sendSpy).toHaveBeenCalledWith('mt-1', expect.objectContaining({ body: 'hi' })))
    expect(openSpy).not.toHaveBeenCalled()
    expect(onThreadOpened).toHaveBeenCalledWith(untitled)
  })

  test('a direct send opens a separate untitled DM when only a titled task thread exists', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const unnamed: MeshThread = { ...thread, id: 'mt-dm', title: '' }
    const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue(unnamed)
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })
    render(<ThreadConversation target={{ kind: 'direct', participant: qa }} threads={[thread]} participants={[qa]} />)
    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'hi' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(sendSpy).toHaveBeenCalledWith('mt-dm', expect.objectContaining({ body: 'hi' })))
    expect(openSpy).toHaveBeenCalledWith(expect.objectContaining({ title: '', memberIds: ['run:qa'] }))
  })

  test('says so when the only other member has finished and cannot reply', () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const done = { ...qa, reachable: false }
    render(<ThreadConversation target={{ kind: 'direct', participant: done }} participants={[done]} />)
    expect(screen.getByRole('status')).toHaveTextContent("QA reviewer has finished and can't reply any more.")
  })

  test('says how the only other member ended when the server gives the reason', () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const failed = { ...qa, reachable: false, unreachable_reason: 'task t-1 failed: invalid result — invalid verdict value "approved"' }
    render(<ThreadConversation target={{ kind: 'direct', participant: failed }} participants={[failed]} />)
    expect(screen.getByRole('status')).toHaveTextContent(
      `QA reviewer can't reply any more: task t-1 failed: invalid result — invalid verdict value "approved".`,
    )
  })

  test('a reachable member gets no finished notice', () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    render(<ThreadConversation target={{ kind: 'direct', participant: qa }} participants={[qa]} />)
    expect(screen.queryByRole('status')).toBeNull()
  })

  test('a closed thread keeps Send disabled for mouse and keyboard', () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const send = vi.spyOn(meshClient, 'sendMeshMessage')
    const view = render(<ThreadConversation target={{ kind: 'thread', thread }} participants={[qa]} />)
    fireEvent.change(screen.getByPlaceholderText('Message Hand-off…'), { target: { value: 'late note' } })
    expect(screen.getByRole('button', { name: 'Send' })).toBeEnabled()
    view.rerender(<ThreadConversation target={{ kind: 'thread', thread: { ...thread, status: 'closed' } }} participants={[qa]} />)
    const button = screen.getByRole('button', { name: 'Send' })
    expect(button).toBeDisabled()
    expect(screen.getByRole('status')).toHaveTextContent('This conversation is closed')
    expect(screen.getByPlaceholderText('Message Hand-off…')).toBeDisabled()
    expect(screen.getByPlaceholderText('Message Hand-off…')).toHaveValue('late note')
    fireEvent.click(button)
    fireEvent.keyDown(screen.getByPlaceholderText('Message Hand-off…'), { key: 'Enter', ctrlKey: true })
    expect(send).not.toHaveBeenCalled()
  })

  test('reports only rendered incoming seq while the visible reader is at bottom', async () => {
    const onMessagesRead = vi.fn()
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [
      { id: 'm1', thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['user'], kind: 'reply', body: 'new', seq: 2, created_at: '' },
      { id: 'm2', thread_id: 'mt-1', sender_id: 'user', recipient_ids: ['run:qa'], kind: 'message', body: 'sent', seq: 3, created_at: '' },
    ], next_cursor: 3 })
    const view = render(<ThreadConversation target={{ kind: 'thread', thread }} participants={[qa]} readEnabled={false} onMessagesRead={onMessagesRead} />)
    await waitFor(() => expect(screen.getByText('new')).toBeInTheDocument())
    expect(onMessagesRead).not.toHaveBeenCalled()
    view.rerender(<ThreadConversation target={{ kind: 'thread', thread }} participants={[qa]} readEnabled onMessagesRead={onMessagesRead} />)
    await waitFor(() => expect(onMessagesRead).toHaveBeenCalledWith('mt-1', 2))
  })

  test('local read reporting leaves a server delivery failure receipt visible', async () => {
    const onMessagesRead = vi.fn()
    const side: MeshParticipant = { id: 'side:r1', kind: 'side_agent', scope: 'run', display_name: 'Side agent', reachable: true }
    const sideThread: MeshThread = { id: 'mt-read-receipt', scope: 'run', members: ['user', 'side:r1'], status: 'open' }
    const message: MeshMessage = { id: 'failed', thread_id: sideThread.id, sender_id: 'user', recipient_ids: ['side:r1'], kind: 'message', body: 'please check', side_agent_failed: true, seq: 2, created_at: '' }
    const incoming: MeshMessage = { id: 'reply', thread_id: sideThread.id, sender_id: 'side:r1', recipient_ids: ['user'], kind: 'reply', body: 'answer', seq: 3, created_at: '' }
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [message, incoming], next_cursor: 3 })
    render(<ThreadConversation target={{ kind: 'thread', thread: sideThread }} participants={[side]} readEnabled onMessagesRead={onMessagesRead} />)
    await waitFor(() => expect(onMessagesRead).toHaveBeenCalledWith(sideThread.id, 3))
    expect(screen.getByText('Could not answer')).toBeInTheDocument()
    expect(message.side_agent_failed).toBe(true)
  })

  test('a hidden document and a reader midway through the log do not report the message until visible at bottom', async () => {
    const onMessagesRead = vi.fn()
    const hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(true)
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [
      { id: 'm1', thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['user'], kind: 'reply', body: 'unread', seq: 4, created_at: '' },
    ], next_cursor: 4 })
    const view = render(<ThreadConversation target={{ kind: 'thread', thread }} readEnabled onMessagesRead={onMessagesRead} />)
    await waitFor(() => expect(screen.getByText('unread')).toBeInTheDocument())
    expect(onMessagesRead).not.toHaveBeenCalled()
    const log = screen.getByRole('log', { name: 'Messages' })
    vi.spyOn(log, 'scrollHeight', 'get').mockReturnValue(1000)
    vi.spyOn(log, 'clientHeight', 'get').mockReturnValue(100)
    log.scrollTop = 300
    fireEvent.scroll(log)
    hidden.mockReturnValue(false)
    fireEvent(document, new Event('visibilitychange'))
    expect(onMessagesRead).not.toHaveBeenCalled()
    // A rerender while the reader stays midway must not acknowledge it either.
    view.rerender(<ThreadConversation target={{ kind: 'thread', thread }} readEnabled onMessagesRead={onMessagesRead} />)
    expect(onMessagesRead).not.toHaveBeenCalled()
    log.scrollTop = 900
    fireEvent.scroll(log)
    await waitFor(() => expect(onMessagesRead).toHaveBeenCalledWith('mt-1', 4))
    hidden.mockRestore()
  })

  test('@ in a group conversation lists its members; picking one addresses only them', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })
    const api: MeshParticipant = { id: 'stage:api', kind: 'stage_agent', scope: 'stage', alias: 'api', display_name: 'API: due dates', stage_id: 'api', reachable: true }
    const group: MeshThread = { id: 'mt-g', title: 'Due dates', scope: 'run', members: ['user', 'run:qa', 'stage:api'], status: 'open' }
    render(<ThreadConversation target={{ kind: 'thread', thread: group }} participants={[qa, api]} />)
    expect(screen.getByText('Everyone in this conversation · QA reviewer, API: due dates')).toBeInTheDocument()

    const input = screen.getByPlaceholderText('Message Due dates…') as HTMLTextAreaElement
    fireEvent.change(input, { target: { value: '@', selectionStart: 1 } })
    const menu = screen.getByRole('listbox', { name: 'Mention someone in this conversation' })
    expect(menu).toHaveTextContent('@api')
    expect(menu).toHaveTextContent('QA reviewer')

    fireEvent.change(input, { target: { value: '@ap', selectionStart: 3 } })
    fireEvent.keyDown(input, { key: 'Enter' })
    await waitFor(() => expect(input.value).toBe('@api '))
    expect(screen.getByText('API: due dates')).toBeInTheDocument()

    fireEvent.change(input, { target: { value: '@api what does a bad dueDate return?' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(sendSpy).toHaveBeenCalledWith('mt-g', expect.objectContaining({ recipientIds: ['stage:api'] })))
  })

  test('deleting an @mention from the draft sends to everyone again', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })
    const api: MeshParticipant = { id: 'stage:api', kind: 'stage_agent', scope: 'stage', alias: 'api', display_name: 'API', stage_id: 'api', reachable: true }
    const group: MeshThread = { id: 'mt-g', title: 'Due dates', scope: 'run', members: ['user', 'run:qa', 'stage:api'], status: 'open' }
    render(<ThreadConversation target={{ kind: 'thread', thread: group }} participants={[qa, api]} />)
    const input = screen.getByPlaceholderText('Message Due dates…') as HTMLTextAreaElement
    fireEvent.change(input, { target: { value: '@api please check' } })
    expect(screen.getByText('API')).toBeInTheDocument()
    fireEvent.change(input, { target: { value: 'please check' } })
    expect(screen.getByText('Everyone in this conversation · QA reviewer, API')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(sendSpy).toHaveBeenCalledWith('mt-g', expect.objectContaining({ recipientIds: ['run:qa', 'stage:api'] })))
  })

  test('a retry after a lost open response re-uses the same open request id (no second thread)', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockRejectedValueOnce(new Error('network')).mockResolvedValueOnce(thread)
    vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })
    render(<ThreadConversation target={{ kind: 'direct', participant: qa }} />)
    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'hi' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('network'))
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(openSpy).toHaveBeenCalledTimes(2))
    expect(openSpy.mock.calls[1]?.[0].requestId).toBe(openSpy.mock.calls[0]?.[0].requestId)
  })

  test('sending the first message to a direct target opens a thread then sends into it', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue(thread)
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })
    const onThreadOpened = vi.fn()

    render(<ThreadConversation target={{ kind: 'direct', participant: qa }} onThreadOpened={onThreadOpened} />)

    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'please review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() => expect(sendSpy).toHaveBeenCalled())
    expect(openSpy).toHaveBeenCalledWith(expect.objectContaining({ scope: 'run', memberIds: ['run:qa'] }))
    expect(sendSpy).toHaveBeenCalledWith('mt-1', expect.objectContaining({ recipientIds: ['run:qa'], body: 'please review' }))
    expect(onThreadOpened).toHaveBeenCalledWith(thread)
    await waitFor(() => expect((screen.getByPlaceholderText('Message QA reviewer…') as HTMLTextAreaElement).value).toBe(''))
  })

  test('an existing thread target sends without opening a new one, addressing every other member', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({
      messages: [{ id: 'm0', thread_id: 'mt-1', sender_id: 'user', recipient_ids: ['run:qa'], kind: 'message', body: 'hi', seq: 1, created_at: '2026-10-18T00:00:00Z' }],
      next_cursor: 1,
    })
    const openSpy = vi.spyOn(meshClient, 'openMeshThread')
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 2 })

    render(<ThreadConversation target={{ kind: 'thread', thread }} />)
    await waitFor(() => expect(screen.getByText('hi')).toBeInTheDocument())

    fireEvent.change(screen.getByPlaceholderText('Message Hand-off…'), { target: { value: 'follow up' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() => expect(sendSpy).toHaveBeenCalledWith('mt-1', expect.objectContaining({ recipientIds: ['run:qa'], body: 'follow up' })))
    expect(openSpy).not.toHaveBeenCalled()
  })

  test('reports a durable send receipt even when selection changes, but never reports a failed send', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    let resolveSend!: (value: meshClient.MeshSendReceipt) => void
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage')
      .mockRejectedValueOnce(new Error('send failed'))
      .mockReturnValueOnce(new Promise((resolve) => { resolveSend = resolve }))
    const onMessageSent = vi.fn()
    const view = render(<ThreadConversation target={{ kind: 'thread', thread }} onMessageSent={onMessageSent} />)
    fireEvent.change(screen.getByPlaceholderText('Message Hand-off…'), { target: { value: 'first' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('send failed'))
    expect(onMessageSent).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(sendSpy).toHaveBeenCalledTimes(2))
    view.unmount()
    await act(async () => resolveSend({ message_id: 'm9', seq: 9 }))
    expect(onMessageSent).toHaveBeenCalledWith(thread, { message_id: 'm9', seq: 9 })
  })

  test('a failed send surfaces an error and keeps the draft', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    vi.spyOn(meshClient, 'openMeshThread').mockRejectedValue(new Error('boom'))

    render(<ThreadConversation target={{ kind: 'direct', participant: qa }} />)
    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'please review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('boom'))
    expect((screen.getByPlaceholderText('Message QA reviewer…') as HTMLTextAreaElement).value).toBe('please review')
  })

  test('a thread that opens but then fails to send keeps the draft, shows the error, and does not notify the parent yet', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue(thread)
    vi.spyOn(meshClient, 'sendMeshMessage').mockRejectedValue(new Error('boom'))
    const onThreadOpened = vi.fn()

    render(<ThreadConversation target={{ kind: 'direct', participant: qa }} onThreadOpened={onThreadOpened} />)
    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'please review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('boom'))
    expect((screen.getByPlaceholderText('Message QA reviewer…') as HTMLTextAreaElement).value).toBe('please review')
    expect(onThreadOpened).not.toHaveBeenCalled()
  })

  test('a direct conversation whose opened thread is closed meanwhile follows the roster and stops sending', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue(thread)
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockRejectedValue(new Error('boom'))
    const view = render(<ThreadConversation target={{ kind: 'direct', participant: qa }} threads={[]} />)
    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'please review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('boom'))

    view.rerender(<ThreadConversation target={{ kind: 'direct', participant: qa }} threads={[{ ...thread, status: 'closed' }]} />)
    expect(screen.getByRole('button', { name: /Send/ })).toBeDisabled()
    expect(sendSpy).toHaveBeenCalledTimes(1)
  })

  test('a successful retry after a failed send still notifies the parent of the opened thread', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue(thread)
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockRejectedValueOnce(new Error('boom')).mockResolvedValueOnce({ message_id: 'm1', seq: 1 })
    const onThreadOpened = vi.fn()

    render(<ThreadConversation target={{ kind: 'direct', participant: qa }} onThreadOpened={onThreadOpened} />)
    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'please review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('boom'))
    expect(onThreadOpened).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() => expect(onThreadOpened).toHaveBeenCalledWith(thread))
    expect(openSpy).toHaveBeenCalledTimes(1)
    expect(sendSpy).toHaveBeenCalledTimes(2)
  })

  test('a retry of the same message after a lost response reuses its request id; an edited message gets a new one', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const sendSpy = vi
      .spyOn(meshClient, 'sendMeshMessage')
      .mockRejectedValueOnce(new Error('network'))
      .mockRejectedValueOnce(new Error('network'))
      .mockResolvedValue({ message_id: 'm1', seq: 1 })
    render(<ThreadConversation target={{ kind: 'thread', thread }} participants={[qa]} />)
    const input = screen.getByPlaceholderText('Message Hand-off…')
    fireEvent.change(input, { target: { value: 'hi' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('network'))
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(sendSpy).toHaveBeenCalledTimes(2))
    expect(sendSpy.mock.calls[1]?.[1].requestId).toBe(sendSpy.mock.calls[0]?.[1].requestId)

    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('network'))
    fireEvent.change(input, { target: { value: 'hi, edited' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(sendSpy).toHaveBeenCalledTimes(3))
    expect(sendSpy.mock.calls[2]?.[1].requestId).not.toBe(sendSpy.mock.calls[0]?.[1].requestId)
  })

  test('text typed while a send is in flight survives its success', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    let resolveSend!: (v: { message_id: string; seq: number }) => void
    vi.spyOn(meshClient, 'sendMeshMessage').mockReturnValue(new Promise((res) => (resolveSend = res)))
    render(<ThreadConversation target={{ kind: 'thread', thread }} participants={[qa]} />)
    const input = screen.getByPlaceholderText('Message Hand-off…') as HTMLTextAreaElement
    fireEvent.change(input, { target: { value: 'first' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    fireEvent.change(input, { target: { value: 'first second' } })
    await act(async () => resolveSend({ message_id: 'm1', seq: 1 }))
    expect(input.value).toBe('second')
  })

  test('in a group with one finished member the default route addresses only the live one and says who is left out', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })
    const done: MeshParticipant = { id: 'stage:api', kind: 'stage_agent', scope: 'stage', alias: 'api', display_name: 'API', stage_id: 'api', reachable: false }
    const group: MeshThread = { id: 'mt-g', title: 'Due dates', scope: 'run', members: ['user', 'run:qa', 'stage:api'], status: 'open' }
    render(<ThreadConversation target={{ kind: 'thread', thread: group }} participants={[qa, done]} />)

    expect(screen.queryByRole('status')).toBeNull()
    expect(screen.getByText("API has finished and won't get this message")).toBeInTheDocument()
    fireEvent.change(screen.getByPlaceholderText('Message Due dates…'), { target: { value: 'status?' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(sendSpy).toHaveBeenCalledWith('mt-g', expect.objectContaining({ recipientIds: ['run:qa'] })))
  })

  test('when everyone in a group has finished, the notice stays and Send is off', () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage')
    const qaDone = { ...qa, reachable: false }
    const apiDone: MeshParticipant = { id: 'stage:api', kind: 'stage_agent', scope: 'stage', alias: 'api', display_name: 'API', stage_id: 'api', reachable: false }
    const group: MeshThread = { id: 'mt-g', title: 'Due dates', scope: 'run', members: ['user', 'run:qa', 'stage:api'], status: 'open' }
    render(<ThreadConversation target={{ kind: 'thread', thread: group }} participants={[qaDone, apiDone]} />)

    expect(screen.getByRole('status')).toHaveTextContent("Everyone in this conversation has finished and can't reply any more.")
    fireEvent.change(screen.getByPlaceholderText('Message Due dates…'), { target: { value: 'anyone?' } })
    expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled()
    expect(sendSpy).not.toHaveBeenCalled()
  })

  test('two members with the same alias: picking one inserts a unique handle and addresses only that one', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })
    const qa1: MeshParticipant = { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA one', reachable: true }
    const qa2: MeshParticipant = { id: 'run:t-2', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA two', reachable: true }
    const group: MeshThread = { id: 'mt-g', title: 'Review', scope: 'run', members: ['user', 'run:t-1', 'run:t-2'], status: 'open' }
    render(<ThreadConversation target={{ kind: 'thread', thread: group }} participants={[qa1, qa2]} />)

    const input = screen.getByPlaceholderText('Message Review…') as HTMLTextAreaElement
    fireEvent.change(input, { target: { value: '@QA', selectionStart: 3 } })
    const menu = screen.getByRole('listbox', { name: 'Mention someone in this conversation' })
    fireEvent.click(within(menu).getByText('QA two'))
    await waitFor(() => expect(input.value).toBe('@run:t-2 '))

    fireEvent.change(input, { target: { value: '@run:t-2 please re-run' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(sendSpy).toHaveBeenCalledWith('mt-g', expect.objectContaining({ recipientIds: ['run:t-2'] })))
  })

  test('typing the shared alias of two members addresses neither of them on its own', () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const qa1: MeshParticipant = { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA one', reachable: true }
    const qa2: MeshParticipant = { id: 'run:t-2', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA two', reachable: true }
    const group: MeshThread = { id: 'mt-g', title: 'Review', scope: 'run', members: ['user', 'run:t-1', 'run:t-2'], status: 'open' }
    render(<ThreadConversation target={{ kind: 'thread', thread: group }} participants={[qa1, qa2]} />)
    fireEvent.change(screen.getByPlaceholderText('Message Review…'), { target: { value: '@qa hi' } })
    expect(screen.getByText('Everyone in this conversation · QA one, QA two')).toBeInTheDocument()
  })
})

// Run conversations mode: the panel's conversation links an answer to the question it answers and reports which
// agent messages the human has actually seen.
describe('ThreadConversation in Run conversations mode', () => {
  const dev: MeshParticipant = { id: 'run:dev', kind: 'run_agent', scope: 'run', alias: 'dev', display_name: 'Dev', reachable: true }
  const group: MeshThread = { id: 'mt-1', title: 'Hand-off', scope: 'run', members: ['user', 'run:qa', 'run:dev'], status: 'open' }
  let server: MeshMessage[] = []

  const agentMsg = (id: string, seq: number, extra: Partial<MeshMessage> = {}): MeshMessage =>
    ({ id, thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['user'], kind: 'request', body: `Question ${id}`, seq, created_at: '2026-10-22T10:00:00Z', ...extra })
  const humanMsg = (id: string, seq: number, extra: Partial<MeshMessage> = {}): MeshMessage =>
    ({ id, thread_id: 'mt-1', sender_id: 'user', recipient_ids: ['run:qa'], kind: 'message', body: 'mine', seq, created_at: '2026-10-22T10:00:00Z', ...extra })

  type HarnessProps = { thread?: MeshThread; tracking?: boolean; seenSeq?: number; onSeen?: RunConversationMode['onSeen']; initialTarget?: string | null | undefined }
  function Harness({ thread: t = group, tracking = true, seenSeq = 0, onSeen = () => {}, initialTarget }: HarnessProps) {
    const [replyTargetId, setReplyTargetId] = useState<string | null | undefined>(initialTarget)
    const [draft, setDraft] = useState('')
    return (
      <ThreadConversation
        key={t.id}
        target={{ kind: 'thread', thread: t }}
        participants={[qa, dev]}
        threads={[t]}
        draft={draft}
        onDraftChange={setDraft}
        run={{ seenSeq, onSeen, tracking, replyTargetId, onReplyTargetChange: setReplyTargetId }}
      />
    )
  }

  const composer = () => screen.getByPlaceholderText('Message Hand-off…') as HTMLTextAreaElement
  const chip = () => document.querySelector('.feed-reply-chip--conversation')
  async function loaded(text: string) {
    await waitFor(() => expect(screen.getAllByText(text).length).toBeGreaterThan(0))
  }
  function type(text: string) {
    fireEvent.change(composer(), { target: { value: text } })
  }
  async function pollOnce() {
    await act(async () => { await vi.advanceTimersByTimeAsync(3100) })
  }

  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
    server = []
  })

  function useServer(messages: MeshMessage[]) {
    server = messages
    vi.spyOn(meshClient, 'getMeshThread').mockImplementation(async () => ({ messages: server, next_cursor: server.at(-1)?.seq ?? 0 }))
  }

  describe('linking an answer to the question', () => {
    test('answering the latest unanswered request without clicking Reply sends kind reply with in_reply_to', async () => {
      useServer([agentMsg('q1', 1)])
      const send = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'a1', seq: 2 })
      render(<Harness />)
      await loaded('Question q1')
      await waitFor(() => expect(chip()).not.toBeNull())
      expect(chip()).toHaveTextContent('Question q1')
      type('Use the blue one')
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
      await waitFor(() => expect(send).toHaveBeenCalledWith('mt-1', expect.objectContaining({ kind: 'reply', inReplyTo: 'q1', body: 'Use the blue one' })))
    })

    test('with no open question a typed message stays a plain message', async () => {
      useServer([agentMsg('n1', 1, { kind: 'message' })])
      const send = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'a1', seq: 2 })
      render(<Harness />)
      await loaded('Question n1')
      expect(chip()).toBeNull()
      type('hello')
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
      await waitFor(() => expect(send).toHaveBeenCalled())
      const sent = send.mock.calls[0]![1]
      expect(sent.kind).toBeUndefined()
      expect(sent.inReplyTo).toBeUndefined()
    })

    test('a question that already has an answer is not preselected', async () => {
      useServer([agentMsg('q1', 1), humanMsg('a1', 2, { kind: 'reply', in_reply_to: 'q1' })])
      render(<Harness />)
      await loaded('Question q1')
      expect(chip()).toBeNull()
    })

    test('outside Run mode there is no reply link, no chip and a request is answered as a plain message', async () => {
      useServer([agentMsg('q1', 1)])
      const send = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'a1', seq: 2 })
      render(<ThreadConversation target={{ kind: 'thread', thread: group }} participants={[qa, dev]} />)
      await loaded('Question q1')
      expect(chip()).toBeNull()
      expect(screen.queryByRole('button', { name: /Reply in conversation/ })).toBeNull()
      fireEvent.change(composer(), { target: { value: 'plain' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
      await waitFor(() => expect(send).toHaveBeenCalledWith('mt-1', expect.not.objectContaining({ inReplyTo: expect.anything() })))
    })

    test('a request B arriving while an answer to A is typed does not retarget', async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true })
      useServer([agentMsg('A', 1)])
      const send = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'a1', seq: 3 })
      render(<Harness />)
      await loaded('Question A')
      await waitFor(() => expect(chip()).toHaveTextContent('Question A'))
      type('answer to a')
      server = [agentMsg('A', 1), agentMsg('B', 2)]
      await pollOnce()
      await loaded('Question B')
      expect(chip()).toHaveTextContent('Question A')
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
      await waitFor(() => expect(send).toHaveBeenCalledWith('mt-1', expect.objectContaining({ kind: 'reply', inReplyTo: 'A' })))
    })

    test('an @mention of another member still includes the reply target sender', async () => {
      useServer([agentMsg('q1', 1)])
      const send = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'a1', seq: 2 })
      render(<Harness />)
      await loaded('Question q1')
      await waitFor(() => expect(chip()).not.toBeNull())
      type('@dev please look, answer is yes')
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
      await waitFor(() => expect(send).toHaveBeenCalled())
      expect(send.mock.calls[0]![1]).toMatchObject({ kind: 'reply', inReplyTo: 'q1', recipientIds: ['run:qa', 'run:dev'] })
    })

    test("clicking another message's Reply link changes the target explicitly", async () => {
      useServer([agentMsg('A', 1), agentMsg('B', 2)])
      const send = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'a1', seq: 3 })
      render(<Harness />)
      await loaded('Question B')
      await waitFor(() => expect(chip()).toHaveTextContent('Question B'))
      const links = screen.getAllByRole('button', { name: /Reply in conversation/ }).filter((b) => b.classList.contains('conversation-reply-link'))
      fireEvent.click(links[0]!)
      expect(chip()).toHaveTextContent('Question A')
      type('to a')
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
      await waitFor(() => expect(send).toHaveBeenCalledWith('mt-1', expect.objectContaining({ inReplyTo: 'A' })))
    })

    test('cancelling the chip sends a plain message and the target does not come back', async () => {
      useServer([agentMsg('q1', 1)])
      const send = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'a1', seq: 2 })
      render(<Harness />)
      await loaded('Question q1')
      await waitFor(() => expect(chip()).not.toBeNull())
      fireEvent.click(screen.getByRole('button', { name: 'Cancel reply in conversation' }))
      expect(chip()).toBeNull()
      type('just a note')
      expect(chip()).toBeNull()
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
      await waitFor(() => expect(send).toHaveBeenCalled())
      expect(send.mock.calls[0]![1].inReplyTo).toBeUndefined()
    })

    test('a plain message sent after cancelling hands the choice back: the still-open question is preselected again', async () => {
      useServer([agentMsg('A', 1)])
      vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'a1', seq: 2 })
      render(<Harness />)
      await loaded('Question A')
      await waitFor(() => expect(chip()).not.toBeNull())
      fireEvent.click(screen.getByRole('button', { name: 'Cancel reply in conversation' }))
      type('plain note')
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
      await waitFor(() => expect(composer().value).toBe(''))
      await waitFor(() => expect(chip()).toHaveTextContent('Question A'))
    })

    test('after a successful send the target is cleared and the answered question is not picked again before the next poll', async () => {
      useServer([agentMsg('q1', 1)])
      vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'a1', seq: 2 })
      render(<Harness />)
      await loaded('Question q1')
      await waitFor(() => expect(chip()).not.toBeNull())
      type('done')
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
      await waitFor(() => expect(composer().value).toBe(''))
      expect(chip()).toBeNull()
    })

    test('a send that resolves after the human picked another question keeps that choice and links the next send to it', async () => {
      // C is the newest, so it is what a cleared choice falls back to: B surviving proves it was kept, not re-derived.
      useServer([agentMsg('A', 1), agentMsg('B', 2), agentMsg('C', 3)])
      let finishA!: (receipt: MeshSendReceipt) => void
      const send = vi.spyOn(meshClient, 'sendMeshMessage')
        .mockImplementationOnce(() => new Promise<MeshSendReceipt>((resolve) => { finishA = resolve }))
        .mockResolvedValue({ message_id: 'b1', seq: 5 })
      render(<Harness />)
      await loaded('Question C')
      await waitFor(() => expect(chip()).toHaveTextContent('Question C'))
      const links = () => screen.getAllByRole('button', { name: /Reply in conversation/ }).filter((b) => b.classList.contains('conversation-reply-link'))
      fireEvent.click(links()[0]!)
      type('to a')
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
      await waitFor(() => expect(send).toHaveBeenCalledTimes(1))
      expect(send.mock.calls[0]![1]).toMatchObject({ inReplyTo: 'A' })
      // While A is still sending the human points at B.
      fireEvent.click(links()[1]!)
      expect(chip()).toHaveTextContent('Question B')
      await act(async () => { finishA({ message_id: 'a1', seq: 3 }) })
      await waitFor(() => expect(composer().value).toBe(''))
      expect(chip()).toHaveTextContent('Question B')
      type('to b')
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
      await waitFor(() => expect(send).toHaveBeenCalledTimes(2))
      expect(send.mock.calls[1]![1]).toMatchObject({ kind: 'reply', inReplyTo: 'B' })
    })

    test('a retry of a failed send reuses the request id, and a different target is a different request', async () => {
      useServer([agentMsg('A', 1), agentMsg('B', 2)])
      const send = vi.spyOn(meshClient, 'sendMeshMessage')
        .mockRejectedValueOnce(new Error('offline'))
        .mockRejectedValueOnce(new Error('offline'))
        .mockResolvedValue({ message_id: 'a1', seq: 3 })
      render(<Harness />)
      await loaded('Question B')
      await waitFor(() => expect(chip()).toHaveTextContent('Question B'))
      type('same words')
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
      await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('offline'))
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
      await waitFor(() => expect(send).toHaveBeenCalledTimes(2))
      expect(send.mock.calls[1]![1].requestId).toBe(send.mock.calls[0]![1].requestId)
      expect(send.mock.calls[1]![1].inReplyTo).toBe('B')
      // The same text aimed at A is another intent: it must not reuse B's key.
      const links = screen.getAllByRole('button', { name: /Reply in conversation/ }).filter((b) => b.classList.contains('conversation-reply-link'))
      fireEvent.click(links[0]!)
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
      await waitFor(() => expect(send).toHaveBeenCalledTimes(3))
      expect(send.mock.calls[2]![1].inReplyTo).toBe('A')
      expect(send.mock.calls[2]![1].requestId).not.toBe(send.mock.calls[0]![1].requestId)
    })

    test('a thread that closes drops the target', async () => {
      useServer([agentMsg('q1', 1)])
      const view = render(<Harness />)
      await loaded('Question q1')
      await waitFor(() => expect(chip()).not.toBeNull())
      view.rerender(<Harness thread={{ ...group, status: 'closed' }} />)
      expect(chip()).toBeNull()
      expect(screen.queryByRole('button', { name: /Reply in conversation/ })).toBeNull()
    })
  })

  describe('seen tracking', () => {
    type Observed = { root: Element | Document | null | undefined; thresholds: number[]; elements: Element[]; callback: IntersectionObserverCallback; disconnected: boolean }
    let observers: Observed[] = []

    function stubObserver() {
      observers = []
      vi.stubGlobal('IntersectionObserver', class {
        record: Observed
        constructor(callback: IntersectionObserverCallback, options?: IntersectionObserverInit) {
          const thresholds = options?.threshold === undefined ? [0] : [options.threshold].flat()
          this.record = { root: options?.root, thresholds, elements: [], callback, disconnected: false }
          observers.push(this.record)
        }
        observe(el: Element) { this.record.elements.push(el) }
        unobserve() {}
        disconnect() { this.record.disconnected = true }
        takeRecords() { return [] }
      })
    }
    const observedIds = () => [...new Set(observers.filter((o) => !o.disconnected).flatMap((o) => o.elements.map((e) => (e as HTMLElement).dataset.messageId)))].sort()
    // see — report `visible` px of a `height` px bubble inside a `rootHeight` px log.
    function see(id: string, visible: number, height = 100, rootHeight = 400) {
      const el = document.querySelector(`[data-message-id="${id}"]`) as HTMLElement
      const live = observers.filter((o) => !o.disconnected && o.elements.includes(el))
      const entry = {
        target: el, isIntersecting: visible > 0, intersectionRatio: visible / height,
        intersectionRect: { height: visible }, boundingClientRect: { height }, rootBounds: { height: rootHeight },
      } as unknown as IntersectionObserverEntry
      act(() => live.forEach((o) => o.callback([entry], {} as IntersectionObserver)))
    }

    function stubLogScroll(scrollTop: number) {
      const log = screen.getByRole('log', { name: 'Messages' })
      Object.defineProperty(log, 'clientHeight', { value: 300, configurable: true })
      Object.defineProperty(log, 'scrollHeight', { value: 1000, configurable: true })
      log.scrollTop = scrollTop
      fireEvent.scroll(log)
      return log
    }

    test('observes only agent messages addressed to the human beyond the cursor, with the message log as the root', async () => {
      stubObserver()
      useServer([
        agentMsg('seen', 1), agentMsg('new1', 2), humanMsg('mine', 3),
        agentMsg('chatter', 4, { recipient_ids: ['run:dev'] }), agentMsg('auto', 5, { auto_result: true, kind: 'reply' }), agentMsg('new2', 6, { kind: 'message' }),
      ])
      render(<Harness seenSeq={1} />)
      await loaded('Question new2')
      expect(observedIds()).toEqual(['new1', 'new2'])
      expect(observers.find((o) => !o.disconnected)?.root).toBe(screen.getByRole('log', { name: 'Messages' }))
    })

    test('observes nothing while the panel or history is not visible, and starts once it is', async () => {
      stubObserver()
      useServer([agentMsg('new1', 1)])
      const view = render(<Harness tracking={false} />)
      await loaded('Question new1')
      expect(observedIds()).toEqual([])
      view.rerender(<Harness tracking />)
      expect(observedIds()).toEqual(['new1'])
    })

    test('a bubble at least half visible is seen and advances the cursor to its seq', async () => {
      stubObserver()
      useServer([agentMsg('q1', 3)])
      const onSeen = vi.fn()
      render(<Harness onSeen={onSeen} />)
      await loaded('Question q1')
      see('q1', 40)
      expect(onSeen).not.toHaveBeenCalled()
      see('q1', 50)
      expect(onSeen).toHaveBeenCalledWith('mt-1', 3)
    })

    test('a bubble taller than the log counts as seen once half the log shows it', async () => {
      stubObserver()
      useServer([agentMsg('tall', 2)])
      const onSeen = vi.fn()
      render(<Harness onSeen={onSeen} />)
      await loaded('Question tall')
      see('tall', 149, 900, 300)
      expect(onSeen).not.toHaveBeenCalled()
      see('tall', 150, 900, 300)
      expect(onSeen).toHaveBeenCalledWith('mt-1', 2)
    })

    test('the cursor only advances through messages actually seen: a later bubble does not skip an earlier unseen one', async () => {
      stubObserver()
      useServer([agentMsg('q1', 2), agentMsg('q2', 4)])
      const onSeen = vi.fn()
      render(<Harness onSeen={onSeen} />)
      await loaded('Question q2')
      see('q2', 100)
      expect(onSeen).not.toHaveBeenCalled()
      see('q1', 100)
      expect(onSeen).toHaveBeenLastCalledWith('mt-1', 4)
    })

    test('nothing is marked seen while the page is hidden; looking again once the tab is shown counts', async () => {
      stubObserver()
      useServer([agentMsg('q1', 2)])
      const onSeen = vi.fn()
      const hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(true)
      render(<Harness onSeen={onSeen} />)
      await loaded('Question q1')
      see('q1', 100)
      expect(onSeen).not.toHaveBeenCalled()
      // A layout change while hidden must not stay remembered as "seen": returning to the tab observes again.
      hidden.mockReturnValue(false)
      act(() => { document.dispatchEvent(new Event('visibilitychange')) })
      expect(onSeen).not.toHaveBeenCalled()
      see('q1', 100)
      expect(onSeen).toHaveBeenCalledWith('mt-1', 2)
    })

    test('a bubble reported visible while hidden gets no accent either', async () => {
      stubObserver()
      vi.useFakeTimers({ shouldAdvanceTime: true })
      useServer([agentMsg('q1', 2)])
      const hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(true)
      render(<Harness />)
      await loaded('Question q1')
      see('q1', 100)
      expect(document.querySelectorAll('.is-new-agent-message')).toHaveLength(0)
      hidden.mockReturnValue(false)
      see('q1', 100)
      expect(document.querySelectorAll('.is-new-agent-message')).toHaveLength(1)
    })

    test('switching to another thread starts tracking afresh', async () => {
      stubObserver()
      useServer([agentMsg('q1', 2)])
      const onSeen = vi.fn()
      const view = render(<Harness onSeen={onSeen} />)
      await loaded('Question q1')
      view.rerender(<Harness thread={{ ...group, id: 'mt-2' }} onSeen={onSeen} />)
      await waitFor(() => expect(observers.some((o) => o.disconnected)).toBe(true))
      expect(onSeen).not.toHaveBeenCalled()
    })

    test('the unseen new message shows New message below when scrolled up, and it jumps to the bottom', async () => {
      stubObserver()
      useServer([agentMsg('q1', 2)])
      render(<Harness />)
      await loaded('Question q1')
      expect(screen.queryByRole('button', { name: 'New message below' })).toBeNull()
      const log = stubLogScroll(0)
      const jump = await screen.findByRole('button', { name: 'New message below' })
      fireEvent.click(jump)
      expect(log.scrollTop).toBe(1000)
    })

    test('no New message below once the message is behind the cursor', async () => {
      stubObserver()
      useServer([agentMsg('q1', 2)])
      render(<Harness seenSeq={2} />)
      await loaded('Question q1')
      stubLogScroll(0)
      expect(screen.queryByRole('button', { name: 'New message below' })).toBeNull()
    })

    describe('the brief accent', () => {
      const accented = () => [...document.querySelectorAll('.is-new-agent-message')].map((e) => (e as HTMLElement).dataset.messageId)

      test('marks a bubble once it is seen; only unseen agent-to-user bubbles can be, and offscreen ones are not bright yet', async () => {
        stubObserver()
        useServer([agentMsg('old', 1), agentMsg('new1', 2), humanMsg('mine', 3), agentMsg('chatter', 4, { recipient_ids: ['run:dev'] }), agentMsg('auto', 5, { auto_result: true, kind: 'reply' })])
        render(<Harness seenSeq={1} />)
        await loaded('Question new1')
        expect(observedIds()).toEqual(['new1'])
        expect(accented()).toEqual([])
        see('new1', 100)
        expect(accented()).toEqual(['new1'])
      })

      test('a bubble that arrived offscreen is bright only from when it scrolls into view, then clears after its fixed timer', async () => {
        stubObserver()
        vi.useFakeTimers({ shouldAdvanceTime: true })
        useServer([agentMsg('q1', 2)])
        render(<Harness />)
        await loaded('Question q1')
        // Longer than the accent: an offscreen bubble must not have used it up.
        await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
        see('q1', 10)
        expect(accented()).toEqual([])
        see('q1', 100)
        expect(accented()).toEqual(['q1'])
        await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
        expect(accented()).toEqual([])
      })

      test('stays on even when the cursor advances in the same render, then clears after its fixed timer without animationend', async () => {
        stubObserver()
        vi.useFakeTimers({ shouldAdvanceTime: true })
        useServer([agentMsg('q1', 2)])
        const view = render(<Harness seenSeq={0} />)
        await loaded('Question q1')
        see('q1', 100)
        expect(accented()).toEqual(['q1'])
        view.rerender(<Harness seenSeq={2} />)
        expect(accented()).toEqual(['q1'])
        await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
        expect(accented()).toEqual([])
      })

      test('does not accent while the panel is hidden, and accents once it opens and the bubble is seen', async () => {
        stubObserver()
        useServer([agentMsg('q1', 2)])
        const view = render(<Harness tracking={false} />)
        await loaded('Question q1')
        expect(accented()).toEqual([])
        view.rerender(<Harness tracking />)
        see('q1', 100)
        expect(accented()).toEqual(['q1'])
      })

      test('a message arriving later is accented once, not again after the poll repeats it', async () => {
        stubObserver()
        vi.useFakeTimers({ shouldAdvanceTime: true })
        useServer([agentMsg('q1', 1)])
        render(<Harness seenSeq={1} />)
        await loaded('Question q1')
        expect(accented()).toEqual([])
        server = [agentMsg('q1', 1), agentMsg('q2', 2)]
        await pollOnce()
        await waitFor(() => expect(observedIds()).toEqual(['q2']))
        see('q2', 100)
        expect(accented()).toEqual(['q2'])
        await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
        expect(accented()).toEqual([])
        await pollOnce()
        see('q2', 100)
        expect(accented()).toEqual([])
      })
    })
  })
})
