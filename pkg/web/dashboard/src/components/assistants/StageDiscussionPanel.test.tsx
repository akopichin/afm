import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { StageDiscussionPanel } from './StageDiscussionPanel'
import * as runClient from '../../api/run-client'

describe('StageDiscussionPanel', () => {
  beforeEach(() => {
    vi.spyOn(runClient, 'listStageDiscussion').mockResolvedValue({
      messages: [
        { id: 'm1', author_kind: 'human', round: 0, body: 'Can the reviewer re-check the retry path?', created_at: '2026-10-16T10:00:00Z' },
      ],
      nextCursor: 1,
      hasMore: false,
    })
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('renders the polled messages', async () => {
    render(<StageDiscussionPanel stageId="implement" />)
    await waitFor(() => expect(screen.getByText(/can the reviewer re-check/i)).toBeInTheDocument())
    expect(screen.getByText('You')).toBeInTheDocument()
  })

  test('messages (and the empty state) live in one scrolling area above the composer, so the composer never jumps', async () => {
    render(<StageDiscussionPanel stageId="implement" />)
    const log = screen.getByRole('log', { name: 'Discussion' })
    await waitFor(() => expect(log).toHaveTextContent(/can the reviewer re-check/i))
    expect(log.nextElementSibling).toHaveClass('chat-composer-row')
  })

  test('posts a human message and clears the input', async () => {
    const post = vi.spyOn(runClient, 'postStageDiscussionMessage').mockResolvedValue({ messageId: 'm2' })
    render(<StageDiscussionPanel stageId="implement" />)
    await waitFor(() => expect(screen.getByText('You')).toBeInTheDocument())

    const input = screen.getByPlaceholderText(/add a message to this stage/i)
    fireEvent.change(input, { target: { value: 'please look at the backoff too' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() => expect(post).toHaveBeenCalledWith('implement', expect.any(String), 'please look at the backoff too'))
    await waitFor(() => expect((input as HTMLTextAreaElement).value).toBe(''))
  })

  test('a retry after a lost response reuses the request id; text typed while sending survives', async () => {
    let resolvePost!: (v: { messageId: string }) => void
    const post = vi
      .spyOn(runClient, 'postStageDiscussionMessage')
      .mockRejectedValueOnce(new Error('network'))
      .mockReturnValueOnce(new Promise((res) => (resolvePost = res)))
    render(<StageDiscussionPanel stageId="implement" />)
    const input = screen.getByPlaceholderText(/add a message to this stage/i) as HTMLTextAreaElement
    fireEvent.change(input, { target: { value: 'check backoff' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('network'))

    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(post).toHaveBeenCalledTimes(2))
    expect(post.mock.calls[1]?.[1]).toBe(post.mock.calls[0]?.[1])
    fireEvent.change(input, { target: { value: 'check backoff and jitter' } })
    await act(async () => resolvePost({ messageId: 'm2' }))
    expect(input.value).toBe('and jitter')
  })

  test('overlapping polls never read the same cursor twice, so no message is appended twice', async () => {
    vi.useFakeTimers()
    try {
      let resolveFirst!: (v: Awaited<ReturnType<typeof runClient.listStageDiscussion>>) => void
      const list = vi
        .spyOn(runClient, 'listStageDiscussion')
        .mockReturnValueOnce(new Promise((res) => (resolveFirst = res)))
        .mockResolvedValue({ messages: [], nextCursor: 1, hasMore: false })
      render(<StageDiscussionPanel stageId="implement" />)
      await act(async () => vi.advanceTimersByTime(9000)) // three ticks while the first read hangs
      expect(list).toHaveBeenCalledTimes(1)

      await act(async () =>
        resolveFirst({ messages: [{ id: 'm1', author_kind: 'human', round: 0, body: 'only once', created_at: '2026-10-16T10:00:00Z' }], nextCursor: 1, hasMore: false }),
      )
      await act(async () => vi.advanceTimersByTime(0))
      expect(list).toHaveBeenLastCalledWith('implement', 1)
      expect(screen.getAllByText('only once')).toHaveLength(1)
    } finally {
      vi.useRealTimers()
    }
  })
})
