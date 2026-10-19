import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
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
    expect(log.nextElementSibling).toHaveClass('stage-discussion-route')
    expect(log.nextElementSibling?.nextElementSibling).toHaveClass('chat-composer-row')
  })

  test('posts a human message and clears the input', async () => {
    const post = vi.spyOn(runClient, 'postStageDiscussionMessage').mockResolvedValue({ messageId: 'm2' })
    render(<StageDiscussionPanel stageId="implement" />)
    await waitFor(() => expect(screen.getByText('You')).toBeInTheDocument())

    const input = screen.getByPlaceholderText(/add a message to this stage/i)
    fireEvent.change(input, { target: { value: 'please look at the backoff too' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('implement', expect.any(String), { body: 'please look at the backoff too', mentions: [], interrupt: false }),
    )
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

  test('Interrupt agent is offered only while the stage runs, and is sent with the post', async () => {
    const post = vi.spyOn(runClient, 'postStageDiscussionMessage').mockResolvedValue({ messageId: 'm2' })
    const { rerender } = render(<StageDiscussionPanel stageId="implement" stageStatus="awaiting_approval" />)
    expect(screen.queryByLabelText('Interrupt agent')).not.toBeInTheDocument()

    rerender(<StageDiscussionPanel stageId="implement" stageStatus="running" />)
    const toggle = screen.getByLabelText('Interrupt agent') as HTMLInputElement
    expect(toggle.checked).toBe(false)
    fireEvent.click(toggle)

    fireEvent.change(screen.getByPlaceholderText(/add a message to this stage/i), { target: { value: 'how is the plan going?' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('implement', expect.any(String), { body: 'how is the plan going?', mentions: [], interrupt: true }),
    )
  })

  test('@ inserts a unique handle, lists only other agents, and sends their ids as mentions', async () => {
    const post = vi.spyOn(runClient, 'postStageDiscussionMessage').mockResolvedValue({ messageId: 'm2' })
    const participants = [
      { id: 'user', kind: 'user', scope: 'run', alias: 'user', display_name: 'You', reachable: true },
      { id: 'stage:implement', kind: 'stage_agent', scope: 'stage', alias: 'implement', reachable: true },
      { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA', reachable: true },
      { id: 'run:t-2', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA 2', reachable: true },
      { id: 'run:t-3', kind: 'run_agent', scope: 'run', alias: 'gone', display_name: 'Gone', reachable: false },
    ]
    render(<StageDiscussionPanel stageId="implement" participants={participants} stageStatus="running" />)
    const input = screen.getByPlaceholderText(/add a message to this stage/i) as HTMLTextAreaElement
    expect(input).toHaveAttribute('aria-expanded', 'false')

    fireEvent.change(input, { target: { value: '@', selectionStart: 1 } })
    const menu = screen.getByRole('listbox', { name: /bring another agent/i })
    expect(menu).not.toHaveTextContent('You')
    expect(menu).not.toHaveTextContent('implement')
    expect(menu).not.toHaveTextContent('Gone') // a finished agent would never get the post
    // Combobox wiring: the input names its open listbox and the highlighted option.
    expect(input).toHaveAttribute('aria-expanded', 'true')
    expect(input).toHaveAttribute('aria-controls', menu.id)
    expect(document.getElementById(input.getAttribute('aria-activedescendant') ?? '')).toHaveAttribute('aria-selected', 'true')
    fireEvent.click(within(menu).getByRole('button', { name: /QA 2/ }))
    // Both run agents share the alias `qa`, so the pick is written as its full id — one mention, one agent.
    expect(input.value).toBe('@run:t-2 ')
    expect(screen.getByText(/and QA 2/)).toBeInTheDocument()

    fireEvent.change(input, { target: { value: '@run:t-2 please check the tests' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('implement', expect.any(String), {
        body: '@run:t-2 please check the tests',
        mentions: ['run:t-2'],
        interrupt: false,
      }),
    )
  })

  test('when the stage finishes the log is read again, so unanswered posts get marked', async () => {
    const list = vi.spyOn(runClient, 'listStageDiscussion')
    const { rerender } = render(<StageDiscussionPanel stageId="implement" stageStatus="running" />)
    await waitFor(() => expect(list).toHaveBeenCalledWith('implement', 0))
    list.mockResolvedValue({
      messages: [{ id: 'm1', author_kind: 'human', round: 0, body: 'any news?', unanswered: true, created_at: '2026-10-16T10:00:00Z' }],
      nextCursor: 1,
      hasMore: false,
    })
    rerender(<StageDiscussionPanel stageId="implement" stageStatus="done" />)
    await waitFor(() => expect(screen.getByText('Not answered')).toBeInTheDocument())
  })

  test('a reply arriving later clears the "Not answered" mark of the row already on screen', async () => {
    vi.useFakeTimers()
    try {
      vi.spyOn(runClient, 'listStageDiscussion')
        .mockResolvedValueOnce({
          messages: [{ id: 'm1', author_kind: 'human', round: 0, body: 'any news?', unanswered: true, created_at: '2026-10-16T10:00:00Z' }],
          nextCursor: 1,
          hasMore: false,
        })
        .mockResolvedValue({
          messages: [{ id: 'm2', author_kind: 'assistant', author_id: 'run:t-1', round: 0, parent_message_id: 'm1', body: 'yes', created_at: '2026-10-16T10:01:00Z' }],
          nextCursor: 2,
          hasMore: false,
        })
      render(<StageDiscussionPanel stageId="implement" stageStatus="done" />)
      await act(async () => vi.advanceTimersByTime(0))
      expect(screen.getByText('Not answered')).toBeInTheDocument()
      await act(async () => vi.advanceTimersByTime(3000))
      expect(screen.getByText('yes')).toBeInTheDocument()
      expect(screen.queryByText('Not answered')).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })
})
