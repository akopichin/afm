import type { ReactElement } from 'react'
import type { AssistantProfile } from '../../types/assistant-profile'

export type AssistantMentionMenuProps = {
  // profiles — already filtered by the caller (mention-match's filterMentionProfiles); this component only
  // renders and dispatches selection/hover, it does not know about the query text.
  profiles: AssistantProfile[]
  highlightIndex: number
  onHover: (index: number) => void
  onSelect: (profile: AssistantProfile) => void
}

// AssistantMentionMenu — the `@mention` suggestion popup under the side-agent composer (dev-plan.md T4.2). A
// mention always addresses a ROLE (profile), never a running task instance — see pkg/assistants.Registry.Resolve's
// doc comment; addressing an already-running task is a separate mechanism (task_id, T4.7).
export function AssistantMentionMenu({ profiles, highlightIndex, onHover, onSelect }: AssistantMentionMenuProps): ReactElement | null {
  if (profiles.length === 0) return null
  return (
    <ul className="assistant-mention-menu" role="listbox" aria-label="Mention an assistant profile">
      {profiles.map((p, i) => (
        <li
          key={p.id}
          role="option"
          aria-selected={i === highlightIndex}
          className={`assistant-mention-item${i === highlightIndex ? ' active' : ''}`}
        >
          <button
            type="button"
            className="assistant-mention-option"
            onMouseEnter={() => onHover(i)}
            // onMouseDown (not onClick): the textarea's blur would otherwise fire first on a click sequence
            // in some browsers, but preventDefault here keeps focus in the textarea so the caret position
            // this selection relies on stays valid.
            onMouseDown={(e) => {
              e.preventDefault()
              onSelect(p)
            }}
          >
            <span className="assistant-mention-id">@{p.id}</span>
            <span className="assistant-mention-title">{p.title}</span>
            {!p.available && (
              <span className="assistant-mention-unavailable" title={p.unavailableReason}>
                Unavailable
              </span>
            )}
          </button>
        </li>
      ))}
    </ul>
  )
}
