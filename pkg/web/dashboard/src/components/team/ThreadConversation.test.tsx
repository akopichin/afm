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
    expect(screen.getByText('QA reviewer')).toBeInTheDocument()
    expect(screen.getByText('No messages yet')).toBeInTheDocument()
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
})
