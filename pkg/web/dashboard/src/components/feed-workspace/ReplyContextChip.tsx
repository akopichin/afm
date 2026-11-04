import type { ReactElement } from 'react'
import type { ComposerMode, ReplyContext } from './composer-draft'

type ReplyContextChipProps = {
  context: ReplyContext
  mode: ComposerMode
  // closed — the conversation of the message answered was closed while the input was being written.
  closed: boolean
  // canUseRestart — Restart with note is available, so a closed conversation can still go out as a restart note.
  canUseRestart: boolean
  onRemove: () => void
  onUseRestart: () => void
}

// ReplyContextChip — what the one input answers. A thought is quoted on send in both modes; a conversation
// reply is Chat's only, so Restart shows it as not sent and keeps it for the way back to Chat.
export function ReplyContextChip({ context, mode, closed, canUseRestart, onRemove, onUseRestart }: ReplyContextChipProps): ReactElement {
  return (
    <div className="feed-reply-context" role="group" aria-label="Reply context">
      {context.kind === 'thought' ? (
        <div className="feed-reply-chip">
          <span className="feed-reply-chip-label">↩ In reply to</span>
          <span className="feed-reply-chip-src">{context.text}</span>
          <button type="button" className="feed-reply-chip-cancel" aria-label="Remove reply to thought" onClick={onRemove}>
            ✕
          </button>
        </div>
      ) : closed && mode === 'chat' ? (
        <div className="feed-reply-chip feed-reply-chip--conversation is-closed">
          <span className="feed-reply-chip-label">↩ Reply in conversation</span>
          <span className="feed-reply-chip-src">{context.target.preview}</span>
          <span className="feed-reply-chip-note">This conversation is closed.</span>
          <span className="feed-reply-chip-actions">
            <button type="button" className="mesh-recipients-use-revise" onClick={onRemove}>
              Remove reply
            </button>
            {canUseRestart && (
              <button type="button" className="mesh-recipients-use-revise" onClick={onUseRestart}>
                Use Restart with note
              </button>
            )}
          </span>
        </div>
      ) : (
        <div className={`feed-reply-chip feed-reply-chip--conversation${mode === 'restart' ? ' is-unused' : ''}`}>
          <span className="feed-reply-chip-label">↩ Reply in conversation</span>
          <span className="feed-reply-chip-src">{context.target.preview}</span>
          <button type="button" className="feed-reply-chip-cancel" aria-label="Remove reply in conversation" onClick={onRemove}>
            ✕
          </button>
          {mode === 'restart' && <span className="feed-reply-chip-note">Not sent with a restart — switch to Chat to reply in the conversation</span>}
        </div>
      )}
    </div>
  )
}
