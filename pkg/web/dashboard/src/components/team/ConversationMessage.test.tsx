import { describe, expect, test, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { ConversationMessage } from './ConversationMessage'
import type { MeshMessage, MeshParticipant } from '../../api/mesh-client'

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
})
