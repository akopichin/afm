import type { ReactElement } from 'react'
import { MarkdownRenderer } from '../plan-panel'

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
  createdAt: string
}

// authorLabel renders a stable, honest attribution for one message — never defaults an unnamed author to
// looking like "the user".
function authorLabel(m: DiscussionMessage): string {
  if (m.authorId) return `@${m.authorId}`
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

type StageDiscussionProps = {
  // messages — already in commit/append order (pkg/assistants.RestoreOrder's contract); this component never
  // re-sorts by createdAt (TestRound_OrderRestoredFromIdsNotTimestamps's UI-facing half).
  messages: DiscussionMessage[]
}

// StageDiscussion renders one stage's discussion log: every message carries its author, role and evidence
// explicitly (dev-plan.md T6.1's "у сообщения есть автор ... роль ... ссылки на evidence") — a participant
// who never replied simply has no entry here, never a synthesized "agreed" placeholder
// (TestRound_SilentParticipantNotShownAsAgreeing). Message bodies render through the SAME safe markdown
// renderer the plan panel/helper task view use — no second dangerouslySetInnerHTML surface
// (TestDiscussion_MarkdownViaSafeRenderer).
export function StageDiscussion({ messages }: StageDiscussionProps): ReactElement {
  if (messages.length === 0) {
    return <div className="stage-discussion-empty empty-hint">No discussion yet</div>
  }
  return (
    <ul className="stage-discussion">
      {messages.map((m) => (
        <li key={m.id} className="stage-discussion-message" data-author-kind={m.authorKind} data-round={m.round}>
          <div className="stage-discussion-message-header">
            <span className="stage-discussion-message-author">{authorLabel(m)}</span>
            {m.role && <span className="stage-discussion-message-role">{m.role}</span>}
          </div>
          <MarkdownRenderer source={m.body} />
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
