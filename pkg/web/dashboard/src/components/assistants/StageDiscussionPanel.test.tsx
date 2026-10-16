import { fireEvent, render, screen, waitFor } from '@testing-library/react'
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
})
