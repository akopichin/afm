import type { ReactElement } from 'react'
import type { MeshMessage, MeshParticipant } from '../../api/mesh-client'
import { senderLabel } from '../../lib/mesh-roster'
import { messageText } from '../../lib/agent-result'
import { AgentMessageBody } from '../agent-message/AgentMessageBody'
import { MeshAvatar } from './MeshAvatar'

export type ConversationMessageProps = {
  message: MeshMessage
  participants: MeshParticipant[]
  // answered — the message this one replies to, quoted at the top of the bubble.
  answered?: MeshMessage
  // onJumpToQuote — the quote becomes a link to the quoted message (the stage feed scrolls to it).
  onJumpToQuote?: (messageId: string) => void
  // onReply — shows "↩ Reply in conversation" under the bubble (the stage feed's composer answers in the thread).
  onReply?: () => void
}

function messageTime(createdAt: string): string {
  const d = new Date(createdAt)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

// ConversationMessage — the ONE bubble of a conversation message, in Team and in the stage feed: avatar,
// author, the agent's command, "→ recipients", time, then the text; a reply quotes the start of the message it
// answers ("Replying to …").
export function ConversationMessage({ message, participants, answered, onJumpToQuote, onReply }: ConversationMessageProps): ReactElement {
  const author = senderLabel(message.sender_id, participants)
  const sender = participants.find((p) => p.id === message.sender_id)
  const kind = sender?.kind ?? (message.sender_id === 'user' ? 'user' : '')
  const recipients = message.recipient_ids.map((id) => senderLabel(id, participants)).join(', ')
  const fromYou = message.sender_id === 'user'
  const quote =
    answered === undefined ? null : (
      <>
        <span className="team-conversation-reply-quote-author">Replying to {senderLabel(answered.sender_id, participants)}</span>
        <span className="team-conversation-reply-quote-text">{messageText(answered.body, answered.sender_id !== 'user')}</span>
      </>
    )
  return (
    <li className={`team-conversation-message${fromYou ? ' from-you' : ''}`} data-sender={message.sender_id} data-message-id={message.id}>
      <MeshAvatar name={author} kind={kind} />
      <div className="team-conversation-message-content">
        <div className="team-conversation-message-header">
          <span className="team-conversation-message-sender">{author}</span>
          {sender?.command !== undefined && sender.command !== '' && (
            <span className="conversation-command-chip" title={`Command: ${sender.command}`}>
              {sender.command}
            </span>
          )}
          {recipients !== '' && <span className="team-conversation-message-route">→ {recipients}</span>}
          <time dateTime={message.created_at}>{messageTime(message.created_at)}</time>
        </div>
        <div className="team-conversation-message-body">
          {answered !== undefined && (
            <blockquote className="team-conversation-reply-quote">
              {onJumpToQuote !== undefined ? (
                <button type="button" className="conversation-quote-jump" title="Show the quoted message" onClick={() => onJumpToQuote(answered.id)}>
                  {quote}
                </button>
              ) : (
                quote
              )}
            </blockquote>
          )}
          <AgentMessageBody body={message.body} fromAgent={!fromYou} autoResult={message.auto_result === true} />
        </div>
        {onReply !== undefined && (
          <div className="conversation-message-actions">
            <button type="button" className="conversation-reply-link" onClick={onReply}>
              ↩ Reply in conversation
            </button>
          </div>
        )}
      </div>
    </li>
  )
}
