import type { ReactElement } from 'react'
import type { MeshParticipant } from '../../api/mesh-client'
import { participantRole, senderLabel } from '../../lib/mesh-roster'
import { AgentMessageBody } from '../agent-message/AgentMessageBody'

// DiscussionMessage mirrors the server's DiscussionMessageView (pkg/server/discussion.go) one field at a
// time — see GET /api/stages/{id}/discussion.
export type DiscussionMessage = {
  id: string
  authorKind: string // 'human' | 'main_agent' | 'assistant' | 'afm'
  authorId?: string
  role?: string
  executor?: string
  round: number
  parentMessageId?: string
  contextVersion?: string
  evidence?: string[]
  body: string
  // mentions — mesh participant ids the human also addressed with `@`.
  mentions?: string[]
  createdAt: string
}

// authorLabel renders a stable, honest attribution for one message — never defaults an unnamed author to
// looking like "the user".
function authorLabel(m: DiscussionMessage, participants: MeshParticipant[]): string {
  if (m.authorId) return senderLabel(m.authorId, participants)
  switch (m.authorKind) {
    case 'human':
      return 'You'
    case 'main_agent':
      return 'Main agent'
    case 'afm':
      return 'AFM'
    default:
      return m.authorKind
  }
}

// roleLabel — the author's role in words; the wire values (main_agent, assistant, a mentioned participant's
// mesh kind such as run_agent) are not for people.
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

type StageDiscussionProps = {
  // messages — already in commit/append order (pkg/assistants.RestoreOrder's contract); this component never
  // re-sorts by createdAt (TestRound_OrderRestoredFromIdsNotTimestamps's UI-facing half).
  messages: DiscussionMessage[]
  // participants — the run's Agent Mesh roster, to name an author by its display name instead of its id.
  participants?: MeshParticipant[]
}

// StageDiscussion renders one stage's discussion log: every message carries its author, role and evidence
// explicitly (dev-plan.md T6.1's "у сообщения есть автор ... роль ... ссылки на evidence") — a participant
// who never replied simply has no entry here, never a synthesized "agreed" placeholder
// (TestRound_SilentParticipantNotShownAsAgreeing). Message bodies render through the SAME safe markdown
// renderer the plan panel/helper task view use — no second dangerouslySetInnerHTML surface
// (TestDiscussion_MarkdownViaSafeRenderer).
export function StageDiscussion({ messages, participants = [] }: StageDiscussionProps): ReactElement {
  if (messages.length === 0) {
    return <div className="stage-discussion-empty empty-hint">No discussion yet</div>
  }
  return (
    <ul className="stage-discussion">
      {messages.map((m) => (
        <li key={m.id} className="stage-discussion-message" data-author-kind={m.authorKind} data-round={m.round}>
          <div className="stage-discussion-message-header">
            <span className="stage-discussion-message-author">{authorLabel(m, participants)}</span>
            {m.role && <span className="stage-discussion-message-role">{roleLabel(m.role)}</span>}
            {m.mentions && m.mentions.length > 0 && (
              <span className="stage-discussion-message-mentions">→ {m.mentions.map((id) => senderLabel(id, participants)).join(', ')}</span>
            )}
          </div>
          <AgentMessageBody body={m.body} markdown />
          {m.evidence && m.evidence.length > 0 && (
            <ul className="stage-discussion-message-evidence">
              {m.evidence.map((e) => (
                <li key={e} className="stage-discussion-evidence-item">
                  {e}
                </li>
              ))}
            </ul>
          )}
        </li>
      ))}
    </ul>
  )
}
