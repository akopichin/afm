import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { ThreadConversation } from './ThreadConversation'
import * as meshClient from '../../api/mesh-client'
import type { MeshParticipant, MeshThread } from '../../api/mesh-client'

const qa: MeshParticipant = { id: 'run:qa', kind: 'run_agent', scope: 'run', display_name: 'QA reviewer', reachable: true }
const thread: MeshThread = { id: 'mt-1', title: 'Hand-off', scope: 'run', members: ['user', 'run:qa'], status: 'open' }

describe('ThreadConversation', () => {
  afterEach(() => {
    vi.restoreAllMocks()
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
    rerender(<ThreadConversation target={{ kind: 'direct', participant: qa }} threads={[thread]} onThreadOpened={onThreadOpened} />)
    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'hi' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(sendSpy).toHaveBeenCalledWith('mt-1', expect.objectContaining({ body: 'hi' })))
    expect(openSpy).not.toHaveBeenCalled()
    expect(onThreadOpened).toHaveBeenCalledWith(thread)
  })

  test('says so when the only other member has finished and cannot reply', () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const done = { ...qa, reachable: false, unreachable_reason: 'stage qa has reached a terminal status' }
    render(<ThreadConversation target={{ kind: 'direct', participant: done }} participants={[done]} />)
    expect(screen.getByRole('status')).toHaveTextContent("QA reviewer has finished and can't reply any more.")
  })

  test('a reachable member gets no finished notice', () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    render(<ThreadConversation target={{ kind: 'direct', participant: qa }} participants={[qa]} />)
    expect(screen.queryByRole('status')).toBeNull()
  })

  test('@ in a group conversation lists its members; picking one addresses only them', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })
    const api: MeshParticipant = { id: 'stage:api', kind: 'stage_agent', scope: 'stage', alias: 'api', display_name: 'API: due dates', stage_id: 'api', reachable: true }
    const group: MeshThread = { id: 'mt-g', title: 'Due dates', scope: 'run', members: ['user', 'run:qa', 'stage:api'], status: 'open' }
    render(<ThreadConversation target={{ kind: 'thread', thread: group }} participants={[qa, api]} />)
    expect(screen.getByText('Everyone in this conversation')).toBeInTheDocument()

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
    expect(screen.getByText('Everyone in this conversation')).toBeInTheDocument()
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
})
