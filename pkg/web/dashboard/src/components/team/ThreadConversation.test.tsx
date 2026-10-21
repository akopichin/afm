import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { conversationSubtitle, ThreadConversation } from './ThreadConversation'
import * as meshClient from '../../api/mesh-client'
import type { MeshMessage, MeshParticipant, MeshThread } from '../../api/mesh-client'

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
