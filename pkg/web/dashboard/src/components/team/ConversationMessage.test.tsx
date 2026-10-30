import { afterEach, describe, expect, test, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { ConversationMessage } from './ConversationMessage'
import { ThreadConversation } from './ThreadConversation'
import * as meshClient from '../../api/mesh-client'
import type { MeshMessage, MeshParticipant, MeshThread } from '../../api/mesh-client'
import { MessageTimeZoneProvider } from '../../lib/message-time-zone'

const participants: MeshParticipant[] = [
  { id: 'user', kind: 'user', scope: 'run', display_name: 'You', reachable: true },
  { id: 'run:qa', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA reviewer', reachable: true, command: 'codex' },
]

const question: MeshMessage = { id: 'm1', thread_id: 'mt-1', sender_id: 'user', recipient_ids: ['run:qa'], kind: 'message', body: 'Is it done?', seq: 1, created_at: '2026-10-19T10:00:00Z' }
const answer: MeshMessage = { id: 'm2', thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['user'], kind: 'reply', in_reply_to: 'm1', body: 'Yes', seq: 2, created_at: '2026-10-19T10:01:00Z' }

function renderMessage(props: Partial<Parameters<typeof ConversationMessage>[0]> = {}) {
  return render(
    <ul>
      <ConversationMessage message={answer} participants={participants} answered={question} {...props} />
    </ul>,
  )
}

describe('ConversationMessage', () => {
  afterEach(() => vi.restoreAllMocks())

  test('feed cards put role, handles and the quote inside the card, with reply and delivery below', () => {
    const onReply = vi.fn()
    renderMessage({ message: { ...answer, recipient_ids: ['stage:impl'] }, participants: [...participants,
      { id: 'stage:impl', kind: 'stage_agent', scope: 'stage', alias: 'impl', display_name: 'Executor', reachable: true },
    ], presentation: 'feed', delivery: 'Delivered', onReply })
    const card = document.querySelector('.feed-message-card')
    expect(card).toContainElement(screen.getByText('Run agent'))
    expect(card).toContainElement(screen.getByText('→ @impl'))
    expect(card).toContainElement(screen.getByText('Replying to You'))
    expect(card).not.toContainElement(screen.getByText('Delivered'))
    expect(card).not.toContainElement(screen.getByRole('button', { name: '↩ Reply in conversation' }))
    fireEvent.click(screen.getByRole('button', { name: '↩ Reply in conversation' }))
    expect(onReply).toHaveBeenCalledOnce()
  })

  test('feed cards only mark a request as waiting when the caller confirms it is unanswered', () => {
    const { rerender } = renderMessage({ presentation: 'feed', waitingForYou: true })
    expect(document.querySelector('.feed-message')).toHaveClass('is-waiting-for-you')
    expect(screen.getByText('Waiting for you')).toBeInTheDocument()
    rerender(<ul><ConversationMessage message={answer} participants={participants} presentation="feed" /></ul>)
    expect(screen.queryByText('Waiting for you')).toBeNull()
    expect(screen.queryByText('Delivered')).toBeNull()
  })

  test('shows the author, the command it runs, the recipients and the quoted message', () => {
    renderMessage()
    expect(screen.getByText('QA reviewer')).toBeInTheDocument()
    expect(screen.getByText('codex')).toHaveAttribute('title', 'Command: codex') // a long one is clipped; the tooltip keeps it whole
    expect(screen.getByText('→ You')).toBeInTheDocument()
    expect(screen.getByText('Replying to You')).toBeInTheDocument()
    expect(screen.queryByRole('button')).toBeNull() // Team: no reply link, the quote is not a link
  })

  test('the quote jumps to the quoted message and Reply answers in the conversation', () => {
    const onJumpToQuote = vi.fn()
    const onReply = vi.fn()
    renderMessage({ onJumpToQuote, onReply })
    fireEvent.click(screen.getByTitle('Show the quoted message'))
    expect(onJumpToQuote).toHaveBeenCalledWith('m1')
    fireEvent.click(screen.getByRole('button', { name: '↩ Reply in conversation' }))
    expect(onReply).toHaveBeenCalled()
  })

  test.each([
    ['direct', ['user', 'run:qa']],
    ['group', ['user', 'run:qa', 'stage:impl']],
  ])('Team %s messages show host time and gaps within the displayed thread', async (_kind, members) => {
    const thread: MeshThread = { id: 'mt-1', scope: 'run', members, status: 'open' }
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [question, answer], next_cursor: 2 })
    render(
      <MessageTimeZoneProvider timeZone="Asia/Tokyo" utcOffsetMinutes={540}>
        <ThreadConversation target={{ kind: 'thread', thread }} participants={participants} />
      </MessageTimeZoneProvider>,
    )
    await waitFor(() => expect(document.querySelector('[data-message-id="m2"]')).not.toBeNull())

    const first = document.querySelector('[data-message-id="m1"]') as HTMLElement
    const second = document.querySelector('[data-message-id="m2"]') as HTMLElement
    const firstTime = first.querySelector('time') as HTMLElement
    const secondTime = second.querySelector('time') as HTMLElement
    expect(firstTime).toHaveClass('message-time')
    expect(firstTime).toHaveAttribute('tabindex', '0')
    expect(firstTime).toHaveAttribute('title', expect.stringMatching(/Oct 19, 2026, 19:00:00 \(host time\).*first event \(\+0s\)/))
    expect(firstTime.querySelector('.message-time-clock')).toHaveTextContent('19:00')
    expect(firstTime.querySelector('.message-time-gap')).toHaveTextContent('+0s')
    expect(secondTime.querySelector('.message-time-clock')).toHaveTextContent('19:01')
    expect(secondTime.querySelector('.message-time-gap')).toHaveTextContent('+1m')
    expect(secondTime).toHaveAttribute('title', expect.stringMatching(/19:01:00 \(host time\).*\+1m after previous/))
    expect(first.querySelector('.team-conversation-message-header > time')).toBe(firstTime)
    expect(second.querySelector('.team-conversation-message-header > time')).toBe(secondTime)
  })
})
