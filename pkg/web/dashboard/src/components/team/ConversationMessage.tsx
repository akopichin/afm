import type { ReactElement } from 'react'
import type { MeshMessage, MeshParticipant } from '../../api/mesh-client'
import { mentionHandles, participantRole, senderLabel } from '../../lib/mesh-roster'
import { messageText } from '../../lib/agent-result'
import { AgentMessageBody } from '../agent-message/AgentMessageBody'
import { formatEventGap } from '../feed-workspace/feed-view-model'
import { MessageTime } from '../message-time/MessageTime'
import { MeshAvatar } from './MeshAvatar'

export type ConversationMessageProps = {
  message: MeshMessage
  previousCreatedAt?: string
  participants: MeshParticipant[]
  // answered — the message this one replies to, quoted at the top of the bubble.
  answered?: MeshMessage
  // onJumpToQuote — the quote becomes a link to the quoted message (the stage feed scrolls to it).
  onJumpToQuote?: (messageId: string) => void
  // onReply — shows "↩ Reply in conversation" under the bubble (the stage feed's composer answers in the thread).
  onReply?: () => void
  // replyActive — this is the message the composer is answering now.
  replyActive?: boolean
  // notAnswered — your post to a stage that finished without anyone answering it.
  notAnswered?: boolean
  // markdown — render the text as Markdown (the stage feed, where the stage conversation always did).
  markdown?: boolean
  // role, evidence — a post from an older run's stage conversation log: its author's role and the ids it cited.
  role?: string
  evidence?: string[]
  sideOutcome?: 'answered' | 'could_not_answer' | null
  // isNew — an agent message to the human that has just arrived: the bubble is briefly bright.
  isNew?: boolean
  presentation?: 'feed'
  delivery?: string
  waitingForYou?: boolean
}

// roleLabel — a log post author's role in words; wire values (main_agent, assistant, a mesh kind) are not for people.
function roleLabel(role: string): string {
  switch (role) {
    case 'main_agent':
      return 'Stage agent'
    case 'assistant':
      return 'Helper'
    default:
      return participantRole(role)
  }
}

// ConversationMessage — the ONE bubble of a conversation message, in Team and in the stage feed: avatar,
// author, the agent's command, "→ recipients", time, then the text; a reply quotes the start of the message it
// answers ("Replying to …").
export function ConversationMessage({ message, previousCreatedAt, participants, answered, onJumpToQuote, onReply, replyActive = false, notAnswered = false, markdown = false, role, evidence, sideOutcome = null, isNew = false, presentation, delivery, waitingForYou = false }: ConversationMessageProps): ReactElement {
  const author = senderLabel(message.sender_id, participants)
  const sender = participants.find((p) => p.id === message.sender_id)
  const kind = sender?.kind ?? (message.sender_id === 'user' ? 'user' : '')
  const feed = presentation === 'feed'
  const handles = mentionHandles(participants)
  const recipients = message.recipient_ids.map((id) => feed && id !== 'user' && handles.has(id) ? `@${handles.get(id)}` : senderLabel(id, participants)).join(', ')
  const fromYou = message.sender_id === 'user'
  const authorRole = role ?? (feed && !fromYou ? (kind === 'stage_agent' ? 'main_agent' : kind) : undefined)
  const quote =
    answered === undefined ? null : (
      <>
        <span className="team-conversation-reply-quote-author">Replying to {senderLabel(answered.sender_id, participants)}</span>
        <span className="team-conversation-reply-quote-text">{messageText(answered.body, answered.sender_id !== 'user')}</span>
      </>
    )
  const content = (
    <>
        <div className="team-conversation-message-header">
          <span className="team-conversation-message-sender">{author}</span>
          {sender?.command !== undefined && sender.command !== '' && (
            <span className="conversation-command-chip" title={`Command: ${sender.command}`}>
              {sender.command}
            </span>
          )}
          {authorRole && <span className="conversation-role-chip">{roleLabel(authorRole)}</span>}
          {recipients !== '' && <span className="team-conversation-message-route">→ {recipients}</span>}
          <MessageTime
            timestamp={message.created_at}
            gap={formatEventGap(Date.parse(message.created_at), previousCreatedAt === undefined ? NaN : Date.parse(previousCreatedAt))}
            firstEvent={previousCreatedAt === undefined}
            focusable
          />
          {notAnswered && (
            <span className="conversation-not-answered" title="The stage finished without replying to this message">
              Not answered
            </span>
          )}
          {sideOutcome !== null && (
            <span className={sideOutcome === 'answered' ? 'conversation-role-chip' : 'conversation-not-answered'} role="status">
              {sideOutcome === 'answered' ? 'Answered' : 'Could not answer'}
            </span>
          )}
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
          <AgentMessageBody body={message.body} fromAgent={!fromYou} markdown={markdown} autoResult={message.auto_result === true} mentionHandles={feed ? [...handles.values()] : undefined} />
          {evidence !== undefined && evidence.length > 0 && (
            <ul className="conversation-evidence" aria-label="Evidence">
              {evidence.map((e) => (
                <li key={e}>{e}</li>
              ))}
            </ul>
          )}
        </div>
    </>
  )
  return (
    <li
      className={`team-conversation-message${feed ? ' feed-message' : ''}${fromYou ? ' from-you' : ''}${replyActive ? ' is-reply-target' : ''}${isNew ? ' is-new-agent-message' : ''}${feed && waitingForYou ? ' is-waiting-for-you' : ''}`}
      data-sender={message.sender_id}
      data-message-id={message.id}
    >
      <MeshAvatar name={author} kind={kind} />
      <div className="team-conversation-message-content">
        {feed ? <div className="feed-message-card">{content}</div> : content}
        {(onReply !== undefined || (feed && (delivery !== undefined || waitingForYou))) && (
          <div className="conversation-message-actions">
            {onReply !== undefined && (
              <button type="button" className="conversation-reply-link" aria-label="↩ Reply in conversation" aria-pressed={replyActive} onClick={onReply}>
                {feed ? '↩ Reply' : '↩ Reply in conversation'}
              </button>
            )}
            {feed && <span className="feed-message-delivery">{waitingForYou ? 'Waiting for you' : delivery}</span>}
          </div>
        )}
      </div>
    </li>
  )
}
